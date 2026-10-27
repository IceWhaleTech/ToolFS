//go:build linux || darwin

package toolfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type cleanupMountHandle struct {
	MountHandle
	point string
	err   error
	calls int
}

func (h *cleanupMountHandle) MountPoint() string { return h.point }
func (h *cleanupMountHandle) Identity() *ZimaIdentity {
	return &ZimaIdentity{UserID: 7, Username: "alice"}
}
func (h *cleanupMountHandle) Unmount() error { h.calls++; return h.err }

func cleanupMountSet(t *testing.T) (*MountSet, *cleanupMountHandle, string) {
	t.Helper()
	root := t.TempDir()
	bind := filepath.Join(root, "u7", "photos")
	point := filepath.Join(bind, "workspace")
	if err := os.MkdirAll(point, 0700); err != nil {
		t.Fatal(err)
	}
	h := &cleanupMountHandle{point: point}
	s := &MountSet{config: MountSetConfig{Root: root}, mounts: map[string]*liveMount{
		mountKey("7", "photos"): {agentUserID: "7", app: "photos", handle: h, bindSource: bind},
	}}
	return s, h, bind
}

func TestUnmountFailureKeepsMountForRetry(t *testing.T) {
	s, h, bind := cleanupMountSet(t)
	h.err = errors.New("busy")
	if err := s.Unmount("7", "photos"); err == nil {
		t.Fatal("unmount failure was ignored")
	}
	if len(s.mounts) != 1 {
		t.Fatal("failed unmount was removed from the ledger")
	}
	if _, err := os.Stat(bind); err != nil {
		t.Fatal("failed unmount lost its directories")
	}
	h.err = nil
	if err := s.Unmount("7", "photos"); err != nil {
		t.Fatal(err)
	}
	if h.calls != 2 || len(s.mounts) != 0 {
		t.Fatalf("failed mount was not retried: calls=%d, mounts=%d", h.calls, len(s.mounts))
	}
	if _, err := os.Stat(bind); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unmounted empty directories remain: %v", err)
	}
}

func TestUnmountRemovesOnlyItsEmptyDirectories(t *testing.T) {
	s, _, bind := cleanupMountSet(t)
	other := filepath.Join(filepath.Dir(bind), "blue", "workspace")
	if err := os.MkdirAll(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Unmount("7", "photos"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bind); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty instance directory remains: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("another app's directory was removed")
	}
}

func TestUnmountDoesNotRecursivelyRemoveUnexpectedFiles(t *testing.T) {
	s, h, _ := cleanupMountSet(t)
	keep := filepath.Join(h.point, "keep")
	if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Unmount("7", "photos"); err == nil {
		t.Fatal("unexpected contents did not prevent cleanup")
	}
	if content, err := os.ReadFile(keep); err != nil || string(content) != "keep" {
		t.Fatalf("unrelated contents were deleted: %q, %v", content, err)
	}
	if err := os.Remove(keep); err != nil {
		t.Fatal(err)
	}
	if err := s.Unmount("7", "photos"); err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 {
		t.Fatalf("cleanup retry repeated a completed unmount: %d", h.calls)
	}
}

func TestUnmountCloseCleansDirectories(t *testing.T) {
	s, _, bind := cleanupMountSet(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bind); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Close left instance directories behind: %v", err)
	}
}

func TestUnmountRefusesSymlinkedInstanceDirectory(t *testing.T) {
	s, h, bind := cleanupMountSet(t)
	outside := t.TempDir()
	marker := filepath.Join(outside, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(h.point); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(bind); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, bind); err != nil {
		t.Fatal(err)
	}
	if err := s.Unmount("7", "photos"); err == nil {
		t.Fatal("symlinked instance directory accepted")
	}
	if len(s.mounts) != 1 {
		t.Fatal("unsafe cleanup lost its retry ledger")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("cleanup touched symlink target: %q, %v", data, err)
	}
}

func TestReprojectSkipsUnmountedCleanupPendingMount(t *testing.T) {
	s, h, _ := cleanupMountSet(t)
	if err := os.WriteFile(filepath.Join(h.point, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Unmount("7", "photos"); err == nil {
		t.Fatal("expected pending directory cleanup")
	}
	// The fake's embedded View would panic if called. An unmounted entry must
	// remain only for cleanup retry, not participate in further projections.
	s.Reproject(t.Context())
	if h.calls != 1 {
		t.Fatalf("unexpected unmount calls: %d", h.calls)
	}
}
