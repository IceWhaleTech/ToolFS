//go:build linux || darwin

package toolfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMountFailurePreparationRollsBackPartialPrefix(t *testing.T) {
	for _, app := range []string{".", "..", "../outside", "photos/nested", "bad\x00name"} {
		t.Run(app, func(t *testing.T) {
			called := false
			s := failingMountSet(t, func(context.Context) (*AccessSnapshot, error) {
				called = true
				return nil, errors.New("provider should not be reached")
			})
			if _, err := s.Mount(t.Context(), "7", app, nil, nil); err == nil {
				t.Fatal("invalid directory preparation succeeded")
			}
			if called || len(s.List()) != 0 {
				t.Fatal("invalid preparation reached mount construction")
			}
			if _, err := os.Stat(filepath.Join(s.config.Root, "u7")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partially created prefix remains: %v", err)
			}
		})
	}
}

func TestMountFailureDoesNotRemoveReplacedDirectory(t *testing.T) {
	failure := errors.New("mount failed")
	var point string
	s := failingMountSet(t, func(context.Context) (*AccessSnapshot, error) {
		if err := os.Rename(point, point+"-original"); err != nil {
			return nil, err
		}
		if err := os.Mkdir(point, 0700); err != nil {
			return nil, err
		}
		return nil, failure
	})
	point = filepath.Join(s.config.Root, "u7", "photos", "workspace")
	_, err := s.Mount(t.Context(), "7", "photos", nil, nil)
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("replaced directory was not refused: %v", err)
	}
	for _, p := range []string{point, point + "-original"} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("rollback deleted a replaced or moved directory: %s: %v", p, err)
		}
	}
}

func TestMountFailureDoesNotTraverseReplacedParent(t *testing.T) {
	failure := errors.New("mount failed")
	outside := t.TempDir()
	moved := filepath.Join(outside, "original")
	var user string
	s := failingMountSet(t, func(context.Context) (*AccessSnapshot, error) {
		if err := os.Rename(user, moved); err != nil {
			return nil, err
		}
		if err := os.Symlink(moved, user); err != nil {
			return nil, err
		}
		return nil, failure
	})
	user = filepath.Join(s.config.Root, "u7")
	_, err := s.Mount(t.Context(), "7", "photos", nil, nil)
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("replaced ancestor was not refused: %v", err)
	}
	if info, err := os.Lstat(user); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("replacement symlink was removed: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(moved, "photos", "workspace")); err != nil {
		t.Fatalf("rollback traversed a replacement parent: %v", err)
	}
}

func TestMountFailureCanceledRequestCreatesNoDirectories(t *testing.T) {
	called := false
	s := failingMountSet(t, func(context.Context) (*AccessSnapshot, error) {
		called = true
		return nil, errors.New("should not be called")
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Mount(ctx, "7", "photos", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	if called {
		t.Fatal("canceled request reached provider")
	}
	if _, err := os.Stat(filepath.Join(s.config.Root, "u7")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled request created runtime directory: %v", err)
	}
}
