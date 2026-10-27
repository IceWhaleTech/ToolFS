//go:build linux || darwin

package toolfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type mountFailureProvider struct {
	grants func(context.Context) (*AccessSnapshot, error)
}

func (p mountFailureProvider) GrantsFor(ctx context.Context, _ *ZimaIdentity, _ string) (*AccessSnapshot, error) {
	return p.grants(ctx)
}
func (p mountFailureProvider) FreshGrantsFor(ctx context.Context, id *ZimaIdentity, app string) (*AccessSnapshot, error) {
	return p.GrantsFor(ctx, id, app)
}
func (p mountFailureProvider) Invalidate(string) {}

func failingMountSet(t *testing.T, fail func(context.Context) (*AccessSnapshot, error)) *MountSet {
	t.Helper()
	s, err := NewMountSet(MountSetConfig{Root: filepath.Join(t.TempDir(), "mounts")},
		NewStaticIdentityResolver(map[string]ZimaIdentity{
			"7": {UserID: 7, Username: "alice"},
		}), mountFailureProvider{grants: fail})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMountFailureRemovesOnlyNewDirectories(t *testing.T) {
	for _, app := range []string{"photos", ""} {
		for _, existing := range []string{"none", "user", "bind", "workspace"} {
			t.Run(app+"/"+existing, func(t *testing.T) {
				failure := errors.New("permission declarations changed")
				s := failingMountSet(t, func(context.Context) (*AccessSnapshot, error) { return nil, failure })
				user := filepath.Join(s.config.Root, "u7")
				bind := filepath.Join(user, app)
				point := filepath.Join(bind, "workspace")
				preexisting := map[string]bool{s.config.Root: true}
				if existing != "none" {
					last := user
					if existing == "bind" || existing == "workspace" {
						last = bind
					}
					if existing == "workspace" {
						last = point
					}
					if err := os.MkdirAll(last, 0700); err != nil {
						t.Fatal(err)
					}
					for p := last; p != s.config.Root; p = filepath.Dir(p) {
						preexisting[p] = true
					}
				}
				for attempt := 0; attempt < 2; attempt++ {
					_, err := s.Mount(t.Context(), "7", app, nil, nil)
					if !errors.Is(err, failure) {
						t.Fatalf("lost original mount error: %v", err)
					}
					if len(s.List()) != 0 || !errors.Is(s.Unmount("7", app), ErrMountNotFound) {
						t.Fatal("failed mount acquired a live ledger entry")
					}
					for _, p := range []string{s.config.Root, user, bind, point} {
						_, err := os.Lstat(p)
						if preexisting[p] && err != nil {
							t.Errorf("removed preexisting directory %s: %v", p, err)
						}
						if !preexisting[p] && !errors.Is(err, os.ErrNotExist) {
							t.Errorf("new directory survived failed mount: %s (%v)", p, err)
						}
					}
				}
			})
		}
	}
}

func TestMountFailurePreservesOtherAppAndDirectoryMode(t *testing.T) {
	failure := errors.New("authority unavailable")
	s := failingMountSet(t, func(context.Context) (*AccessSnapshot, error) { return nil, failure })
	user := filepath.Join(s.config.Root, "u7")
	other := filepath.Join(user, "blue", "workspace")
	if err := os.MkdirAll(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(user, 0710); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(other, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mount(t.Context(), "7", "photos", nil, nil); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(user, "photos")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed app left runtime directories: %v", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("other app changed: %q, %v", data, err)
	}
	if info, err := os.Stat(user); err != nil || info.Mode().Perm() != 0710 {
		t.Fatalf("preexisting user directory mode changed: %v, %v", info, err)
	}
}

func TestMountFailureNeverTraversesManagedSymlinksOrFiles(t *testing.T) {
	for _, level := range []string{"user", "bind", "workspace"} {
		for _, kind := range []string{"symlink", "file"} {
			t.Run(level+"/"+kind, func(t *testing.T) {
				called := false
				s := failingMountSet(t, func(context.Context) (*AccessSnapshot, error) {
					called = true
					return nil, errors.New("provider should not be reached")
				})
				user := filepath.Join(s.config.Root, "u7")
				bind := filepath.Join(user, "photos")
				target := map[string]string{"user": user, "bind": bind, "workspace": filepath.Join(bind, "workspace")}[level]
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				outside := t.TempDir()
				marker := filepath.Join(outside, "keep")
				if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				if kind == "symlink" {
					err = os.Symlink(outside, target)
				} else {
					err = os.WriteFile(target, []byte("keep"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Mount(t.Context(), "7", "photos", nil, nil); err == nil {
					t.Fatal("unsafe mount layout accepted")
				}
				if called {
					t.Fatal("unsafe layout reached the permission provider")
				}
				if _, err := os.Lstat(target); err != nil {
					t.Fatalf("preexisting path was removed: %v", err)
				}
				entries, err := os.ReadDir(outside)
				if err != nil || len(entries) != 1 || entries[0].Name() != "keep" {
					t.Fatalf("mount traversed symlink target: %v, %v", entries, err)
				}
			})
		}
	}
}

func TestMountFailurePreservesUnexpectedContentsAndBothErrors(t *testing.T) {
	failure := errors.New("permission fetch failed")
	var point string
	s := failingMountSet(t, func(context.Context) (*AccessSnapshot, error) {
		if err := os.WriteFile(filepath.Join(point, "keep"), []byte("keep"), 0600); err != nil {
			return nil, err
		}
		return nil, failure
	})
	point = filepath.Join(s.config.Root, "u7", "photos", "workspace")
	_, err := s.Mount(t.Context(), "7", "photos", nil, nil)
	if !errors.Is(err, failure) || (!errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST)) {
		t.Fatalf("mount and cleanup errors must both remain inspectable: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(point, "keep")); err != nil || string(data) != "keep" {
		t.Fatalf("unexpected data removed: %q, %v", data, err)
	}
}
