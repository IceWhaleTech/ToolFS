//go:build linux || darwin

package toolfs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMountAgentAuthorizationLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name     string
		zeroCopy bool
	}{{"copy", false}, {"zero-copy", true}} {
		t.Run(tc.name, func(t *testing.T) {
			testMountAgentAuthorizationLifecycle(t, tc.zeroCopy)
		})
	}
}

func testMountAgentAuthorizationLifecycle(t *testing.T, zeroCopy bool) {
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
	mounts, err := NewMountSet(MountSetConfig{Root: t.TempDir(), MountOptions: MountConfig{ZeroCopyRead: zeroCopy}}, NewStaticIdentityResolver(nil), p)
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
	mounts.mu.Lock()
	live := mounts.mounts[mountKey("7", "photos")]
	handle := live.handle
	mountedView := handle.View()
	mounts.mu.Unlock()
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
					mounts.mu.Lock()
					current := mounts.mounts[mountKey("7", "photos")]
					unchanged := len(mounts.mounts) == 1 && current == live && !current.unmounted && current.handle == handle && current.handle.View() == mountedView && current.bindSource == mount.BindSource && current.handle.MountPoint() == mount.MountPoint
					mounts.mu.Unlock()
					if !unchanged {
						t.Fatal("permission change replaced the live mount or view")
					}
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
	before := make([]byte, 1)
	if _, err := file.ReadAt(before, 0); err != nil || string(before) != "A" {
		t.Fatalf("granted open descriptor: %q, %v", before, err)
	}
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

func TestMountAgentDeleteRevocation(t *testing.T) {
	requireFUSE(t)
	root := t.TempDir()
	for _, name := range []string{"A", "B", "A/empty", "A/empty-delete"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"A/delete", "A/keep", "A/overwrite", "A/move", "B/target", "A/replace", "A/replaced"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, a := newAgentScopeProvider(t)
	a.mu.Lock()
	a.root, a.owner = root, true
	a.grants["/media/Docs"] = []string{"read", "write"}
	a.mu.Unlock()
	mounts, err := NewMountSet(MountSetConfig{Root: t.TempDir()}, NewStaticIdentityResolver(nil), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mounts.Close(); err != nil {
			t.Errorf("unmount fixture: %v", err)
		}
	})
	identity := ZimaIdentity{Username: "alice", UserID: 7}
	mount, err := mounts.Mount(t.Context(), "7", "photos", &identity, nil)
	if err != nil {
		t.Fatal(err)
	}
	mounts.mu.Lock()
	live := mounts.mounts[mountKey("7", "photos")]
	handle, view := live.handle, live.handle.View()
	mounts.mu.Unlock()
	projected := make(chan int64, 8)
	p.OnChange(func(generation int64) {
		mounts.Reproject(t.Context())
		projected <- generation
	})
	change := func(relations []string) {
		t.Helper()
		a.change(t, p, "path", func() { a.grants["/media/Docs"] = relations })
		a.mu.Lock()
		generation := a.generation
		a.mu.Unlock()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case got := <-projected:
				if got == generation {
					mounts.mu.Lock()
					current := mounts.mounts[mountKey("7", "photos")]
					unchanged := len(mounts.mounts) == 1 && current == live && !current.unmounted && current.handle == handle && handle.View() == view && current.bindSource == mount.BindSource && handle.MountPoint() == mount.MountPoint
					mounts.mu.Unlock()
					if !unchanged {
						t.Fatal("delete permission change replaced the mount or view")
					}
					return
				}
			case <-deadline:
				t.Fatal("delete permission change did not reproject")
			}
		}
	}
	path := func(name string) string { return filepath.Join(mount.MountPoint, "Docs", name) }
	if err := os.Remove(path("A/delete")); err == nil {
		t.Fatal("read/write implicitly allowed delete")
	}
	if err := os.WriteFile(path("A/created"), []byte("created"), 0600); err != nil {
		t.Fatalf("read/write did not permit creation: %v", err)
	}
	reader, err := os.Open(path("A/keep"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := os.OpenFile(path("A/keep"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	before := make([]byte, len("A/keep"))
	if _, err := reader.ReadAt(before, 0); err != nil || string(before) != "A/keep" {
		t.Fatalf("granted old read handle: %q, %v", before, err)
	}
	change([]string{"read", "write", "delete"})
	if err := os.Remove(path("A/delete")); err != nil {
		t.Fatalf("explicit delete failed: %v", err)
	}
	if err := os.Remove(path("A/empty-delete")); err != nil {
		t.Fatalf("explicit directory delete failed: %v", err)
	}
	if err := os.Rename(path("A/move"), path("B/moved")); err != nil {
		t.Fatalf("explicit delete did not permit moving across directories: %v", err)
	}
	if err := os.Rename(path("A/replace"), path("A/replaced")); err != nil {
		t.Fatalf("explicit delete did not permit overwriting: %v", err)
	}
	change([]string{"read", "write"})
	for _, name := range []string{"A/keep", "A/empty"} {
		if err := os.Remove(path(name)); err == nil {
			t.Fatalf("revoked delete still allowed removal: %s", name)
		}
	}
	if err := os.Rename(path("A/keep"), path("B/after-revoke")); err == nil {
		t.Fatal("revoked delete still allowed moving across directories")
	}
	if err := os.Rename(path("A/keep"), path("A/overwrite")); err == nil {
		t.Fatal("revoked delete still allowed overwriting a target")
	}
	for _, name := range []string{"A/keep", "A/overwrite", "B/target"} {
		if data, err := os.ReadFile(path(name)); err != nil || string(data) != name {
			t.Fatalf("denied deletion/rename changed %s: %q, %v", name, data, err)
		}
	}
	if _, err := writer.WriteAt([]byte("update"), 0); err != nil {
		t.Fatalf("delete revocation removed old handle write: %v", err)
	}
	data := make([]byte, 6)
	if _, err := reader.ReadAt(data, 0); err != nil || string(data) != "update" {
		t.Fatalf("delete revocation removed old handle read: %q, %v", data, err)
	}
	if err := os.WriteFile(path("A/keep"), []byte("updated"), 0600); err != nil {
		t.Fatalf("delete revocation removed write: %v", err)
	}
	if err := os.Rename(path("A/keep"), path("A/renamed")); err != nil {
		t.Fatalf("non-overwriting same-directory rename must retain its existing semantics: %v", err)
	}
	if data, err := os.ReadFile(path("A/renamed")); err != nil || string(data) != "updated" {
		t.Fatalf("read/write changed after delete revocation: %q, %v", data, err)
	}
	change(nil)
	assertDirNames(t, "fully revoked agent", mount.MountPoint, nil)
	if _, err := reader.ReadAt(data, 0); err == nil {
		t.Fatal("old handle retained read after clearing scope")
	}
	if _, err := writer.WriteAt([]byte("denied"), 0); err == nil {
		t.Fatal("old handle retained write after clearing scope")
	}
}
