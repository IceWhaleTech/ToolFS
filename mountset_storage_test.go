//go:build linux || darwin

package toolfs

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type storageMountHandle struct {
	cleanupMountHandle
	view *InstanceView
}

func (h *storageMountHandle) View() *InstanceView { return h.view }

type storageTestProvider struct {
	PermissionProvider
	load func(context.Context) (*AccessSnapshot, error)
}

func (p storageTestProvider) FreshGrantsFor(ctx context.Context, _ *ZimaIdentity, _ string) (*AccessSnapshot, error) {
	return p.load(ctx)
}

func storageMountSet(t *testing.T, view *InstanceView, provider PermissionProvider) *MountSet {
	t.Helper()
	if err := view.openRoots(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(view.closeRoots)
	s, h, _ := cleanupMountSet(t)
	s.mounts[mountKey("7", "photos")].handle = &storageMountHandle{cleanupMountHandle: *h, view: view}
	s.provider = provider
	return s
}

func TestStorageWatcherDiscoversInitiallyEmptyRootAndStops(t *testing.T) {
	base := t.TempDir()
	identity := &ZimaIdentity{Username: "alice", UserID: 7}
	access := NewAccessSnapshot("alice", 7, "user", nil, 1)
	view, err := BuildInstanceView(identity, access, capsBase(base))
	if err != nil {
		t.Fatal(err)
	}
	var loads atomic.Int32
	seen := make(chan struct{}, 16)
	s := storageMountSet(t, view, storageTestProvider{load: func(context.Context) (*AccessSnapshot, error) {
		loads.Add(1)
		select {
		case seen <- struct{}{}:
		default:
		}
		next := NewAccessSnapshot("alice", 7, "user", nil, 1)
		if _, err := os.Stat(filepath.Join(base, "New")); err == nil {
			next = NewAccessSnapshot("alice", 7, "user", []Grant{{Path: filepath.Join(base, "New")}}, 1)
		}
		return next, nil
	}})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.WatchStorage(ctx, 5*time.Millisecond) }()
	t.Cleanup(func() { cancel(); awaitMountTest(t, done) })
	awaitMountTest(t, seen)
	if err := os.Mkdir(filepath.Join(base, "New"), 0700); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for !hasEntry(view, "New") {
		select {
		case <-seen:
		case <-deadline:
			t.Fatal("watcher did not discover disk without a permission event")
		}
	}
	cancel()
	awaitMountTest(t, done)
	before := loads.Load()
	if view.Generation != 1 || !view.Allows(entryFor(t, view, "New"), "", OpRead) || loads.Load() != before {
		t.Fatal("watcher changed authority state or did not stop")
	}
}

func TestStorageReprojectsSerializeWithoutHoldingMountTable(t *testing.T) {
	_, view := expansionFixture(t, "user")
	access := view.Access
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	t.Cleanup(unblock)
	var calls atomic.Int32
	s := storageMountSet(t, view, storageTestProvider{load: func(ctx context.Context) (*AccessSnapshot, error) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-resume:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return access, nil
	}})
	first, second := make(chan struct{}), make(chan struct{})
	go func() { defer close(first); s.Reproject(t.Context()) }()
	awaitMountTest(t, entered)
	if s.reprojectMu.TryLock() {
		s.reprojectMu.Unlock()
		t.Fatal("authority read is not serialized")
	}
	listed := make(chan []MountInfo, 1)
	go func() { listed <- s.List() }()
	if len(awaitMountTest(t, listed)) != 1 {
		t.Fatal("building candidate held mount table lock")
	}
	go func() { defer close(second); s.Reproject(t.Context()) }()
	unblock()
	awaitMountTest(t, first)
	awaitMountTest(t, second)
	if calls.Load() != 2 {
		t.Fatalf("concurrent projection calls = %d", calls.Load())
	}
}

func TestStorageReprojectDoesNotReviveUnmountedInstance(t *testing.T) {
	_, view := expansionFixture(t, "user")
	access := view.Access
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	t.Cleanup(unblock)
	s := storageMountSet(t, view, storageTestProvider{load: func(context.Context) (*AccessSnapshot, error) {
		close(entered)
		<-resume
		return access, nil
	}})
	done := make(chan struct{})
	go func() { defer close(done); s.Reproject(t.Context()) }()
	awaitMountTest(t, entered)
	if err := s.Unmount("7", "photos"); err != nil {
		t.Fatal(err)
	}
	unblock()
	awaitMountTest(t, done)
	if len(s.List()) != 0 {
		t.Fatal("projection republished an unmounted instance")
	}
}
