//go:build linux || darwin

package toolfs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMountAgentAuthorizationLifecycle(t *testing.T) {
	requireFUSE(t)
	root := t.TempDir()
	for _, name := range []string{"A", "B", "private"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "marker"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, a := newAgentScopeProvider(t)
	a.mu.Lock()
	a.root = root
	a.mu.Unlock()
	identity := ZimaIdentity{Username: "alice", UserID: 7}
	mounts, err := NewMountSet(MountSetConfig{Root: t.TempDir()}, NewStaticIdentityResolver(nil), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mounts.Close(); err != nil {
			t.Errorf("unmount fixture: %v", err)
		}
	})
	projected := make(chan int64, 16)
	p.OnChange(func(generation int64) {
		mounts.Reproject(t.Context())
		projected <- generation
	})
	mount, err := mounts.Mount(t.Context(), "7", "photos", &identity, nil)
	if err != nil {
		t.Fatalf("mount authority fixture: %v", err)
	}
	change := func(namespace string, mutate func()) {
		t.Helper()
		a.change(t, p, namespace, mutate)
		a.mu.Lock()
		generation := a.generation
		a.mu.Unlock()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case got := <-projected:
				if got == generation {
					return
				}
			case <-deadline:
				t.Fatalf("generation %d did not reproject the mounted view", generation)
			}
		}
	}
	assertPaths := func(wantA, wantB bool) {
		t.Helper()
		for name, want := range map[string]bool{"A": wantA, "B": wantB, "private": false} {
			data, err := os.ReadFile(filepath.Join(mount.MountPoint, "Docs", name, "marker"))
			if want && (err != nil || string(data) != name) {
				t.Errorf("granted %s: %q, %v", name, data, err)
			} else if !want && err == nil {
				t.Errorf("ungranted %s remains readable: %q", name, data)
			}
		}
		if !wantA && !wantB {
			assertDirNames(t, "empty agent", mount.MountPoint, nil)
		}
	}
	assertPaths(false, false)
	change("agent", func() { a.owner = true })
	assertPaths(false, false)
	change("path", func() { a.grants["/media/Docs/A"] = []string{"read"} })
	assertPaths(true, false)
	file, err := os.Open(filepath.Join(mount.MountPoint, "Docs/A/marker"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	dir, err := os.Open(filepath.Join(mount.MountPoint, "Docs/A"))
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	change("path", func() { a.grants["/media/Docs/B"] = []string{"read"} })
	assertPaths(true, true)
	change("path", func() { delete(a.grants, "/media/Docs/A") })
	assertPaths(false, true)
	data := make([]byte, 1)
	if _, err := file.ReadAt(data, 0); err == nil {
		t.Fatalf("revoked open descriptor remains readable: %q", data)
	}
	if names, err := dir.Readdirnames(-1); err == nil {
		t.Fatalf("revoked open directory remains readable: %v", names)
	}
	change("path", func() { delete(a.grants, "/media/Docs/B") })
	assertPaths(false, false)
	change("agent", func() { a.owner = false })
	assertPaths(false, false)
	change("agent", func() {
		a.owner = true
		a.grants["/media/Docs/A"] = []string{"read"}
	})
	assertPaths(true, false)
	change("agent", func() { a.owner = false })
	assertPaths(false, false)
	for _, name := range []string{"A", "B", "private"} {
		data, err := os.ReadFile(filepath.Join(root, name, "marker"))
		if err != nil || string(data) != name {
			t.Errorf("backing fixture changed: %q, %v", data, err)
		}
	}
}
