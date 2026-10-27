//go:build linux || darwin

package toolfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type mountTestResult struct {
	info MountInfo
	err  error
}

func awaitMountTest[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("mount lifecycle operation did not finish")
		var zero T
		return zero
	}
}

func requireLifecycleLocked(t *testing.T, s *MountSet) {
	t.Helper()
	if s.lifecycleMu.TryLock() {
		s.lifecycleMu.Unlock()
		t.Fatal("mount construction did not hold the lifecycle lock")
	}
}

func TestMountConcurrentRequestsShareOneMountAndListStaysAvailable(t *testing.T) {
	s := failingMountSet(t, func(context.Context) (*AccessSnapshot, error) {
		return nil, errors.New("test mount bypasses FUSE")
	})
	entered, resume := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.mount = func(_ context.Context, _ IdentityResolver, _ PermissionProvider, _, _, point string, _ AgentCapabilities, _ MountConfig) (MountHandle, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-resume
		}
		return &cleanupMountHandle{point: point}, nil
	}
	first, second := make(chan mountTestResult, 1), make(chan mountTestResult, 1)
	go func() {
		info, err := s.Mount(t.Context(), "7", "photos", nil, nil)
		first <- mountTestResult{info, err}
	}()
	awaitMountTest(t, entered)
	t.Cleanup(func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	})
	requireLifecycleLocked(t, s)
	listed := make(chan []MountInfo, 1)
	go func() { listed <- s.List() }()
	if entries := awaitMountTest(t, listed); len(entries) != 0 {
		t.Fatalf("pending mount appeared in ledger: %+v", entries)
	}
	started := make(chan struct{})
	go func() {
		close(started)
		info, err := s.Mount(t.Context(), "7", "photos", nil, nil)
		second <- mountTestResult{info, err}
	}()
	awaitMountTest(t, started)
	close(resume)
	a, b := awaitMountTest(t, first), awaitMountTest(t, second)
	if a.err != nil || !errors.Is(b.err, ErrMountExists) || a.info != b.info || calls.Load() != 1 {
		t.Fatalf("duplicate construction: first=%+v second=%+v calls=%d", a, b, calls.Load())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMountConcurrentUnmountWaitsForMountPublication(t *testing.T) {
	s := failingMountSet(t, func(context.Context) (*AccessSnapshot, error) { return nil, errors.New("unused") })
	entered, resume := make(chan struct{}), make(chan struct{})
	s.mount = func(_ context.Context, _ IdentityResolver, _ PermissionProvider, _, _, point string, _ AgentCapabilities, _ MountConfig) (MountHandle, error) {
		close(entered)
		<-resume
		return &cleanupMountHandle{point: point}, nil
	}
	mounted, unmounted := make(chan error, 1), make(chan error, 1)
	go func() { _, err := s.Mount(t.Context(), "7", "photos", nil, nil); mounted <- err }()
	awaitMountTest(t, entered)
	t.Cleanup(func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	})
	requireLifecycleLocked(t, s)
	started := make(chan struct{})
	go func() { close(started); unmounted <- s.Unmount("7", "photos") }()
	awaitMountTest(t, started)
	close(resume)
	if err := awaitMountTest(t, mounted); err != nil {
		t.Fatal(err)
	}
	if err := awaitMountTest(t, unmounted); err != nil {
		t.Fatalf("release raced ahead of publication: %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatal("released mount remains in ledger")
	}
	if _, err := os.Stat(filepath.Join(s.config.Root, "u7")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released mount left runtime directories: %v", err)
	}
}

func TestMountConcurrentFailureDoesNotRemoveOtherApp(t *testing.T) {
	failure := errors.New("failed mount")
	s := failingMountSet(t, func(context.Context) (*AccessSnapshot, error) { return nil, failure })
	entered, resume := make(chan struct{}), make(chan struct{})
	s.mount = func(_ context.Context, _ IdentityResolver, _ PermissionProvider, _, app, point string, _ AgentCapabilities, _ MountConfig) (MountHandle, error) {
		if app == "photos" {
			close(entered)
			<-resume
			return nil, failure
		}
		return &cleanupMountHandle{point: point}, nil
	}
	failed, succeeded := make(chan error, 1), make(chan mountTestResult, 1)
	go func() { _, err := s.Mount(t.Context(), "7", "photos", nil, nil); failed <- err }()
	awaitMountTest(t, entered)
	t.Cleanup(func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	})
	requireLifecycleLocked(t, s)
	started := make(chan struct{})
	go func() {
		close(started)
		info, err := s.Mount(t.Context(), "7", "blue", nil, nil)
		succeeded <- mountTestResult{info, err}
	}()
	awaitMountTest(t, started)
	close(resume)
	if err := awaitMountTest(t, failed); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	other := awaitMountTest(t, succeeded)
	if other.err != nil {
		t.Fatal(other.err)
	}
	if _, err := os.Stat(other.info.MountPoint); err != nil {
		t.Fatalf("other app's mount path was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.config.Root, "u7", "photos")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed app directory remains: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMountRejectsOverlappingPhysicalWorkspaces(t *testing.T) {
	for _, tc := range []struct{ firstID, firstApp, secondID, secondApp string }{
		{"7", "photos", "alias", "photos"},
		{"7", "", "7", "workspace"},
		{"7", "workspace", "7", ""},
	} {
		t.Run(tc.firstApp+"/"+tc.secondID+"/"+tc.secondApp, func(t *testing.T) {
			s := failingMountSet(t, func(context.Context) (*AccessSnapshot, error) { return nil, errors.New("unused") })
			s.resolver = NewStaticIdentityResolver(map[string]ZimaIdentity{
				"7": {UserID: 7, Username: "alice"}, "alias": {UserID: 7, Username: "alice"},
			})
			calls := 0
			s.mount = func(_ context.Context, _ IdentityResolver, _ PermissionProvider, _, _, point string, _ AgentCapabilities, _ MountConfig) (MountHandle, error) {
				calls++
				return &cleanupMountHandle{point: point}, nil
			}
			info, err := s.Mount(t.Context(), tc.firstID, tc.firstApp, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Mount(t.Context(), tc.secondID, tc.secondApp, nil, nil); err == nil || !strings.Contains(err.Error(), "overlaps") {
				t.Fatalf("overlapping live workspace accepted: %v", err)
			}
			if calls != 1 || len(s.List()) != 1 {
				t.Fatal("overlap reached mount construction")
			}
			if _, err := os.Stat(info.MountPoint); err != nil {
				t.Fatalf("existing workspace changed: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
