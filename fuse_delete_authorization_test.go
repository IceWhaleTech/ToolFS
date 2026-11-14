//go:build linux || darwin

package toolfs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Exercise the actual mutation callbacks without a kernel mount. Real FUSE
// coverage remains in TestMountAgentDeleteRevocation.
func TestDeleteHandlersUseEffectiveAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        string
		allowDelete bool
		allowed     bool
	}{
		{"agent-default", "agent", false, true},
		{"agent-capability", "agent", true, true},
		{"user-default", "user", false, false},
		{"user-capability", "user", true, true},
		{"static-default", "static", false, false},
		{"static-capability", "static", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"A/empty", "B"} {
				if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"A/delete", "A/move", "A/replace", "A/target"} {
				write(t, root, name, name)
			}
			identity := &ZimaIdentity{Username: "alice", UserID: 7}
			caps := AgentCapabilities{AllowDelete: tc.allowDelete}
			var access *AccessSnapshot
			if tc.mode == "static" {
				access = NewAccessSnapshot("alice", 7, "user", []Grant{{ID: 1, Path: root}}, 1)
				caps.ViewAliases = []PathAlias{{Host: root, View: "/Docs"}}
			} else {
				p, a := newAgentScopeProvider(t)
				a.mu.Lock()
				a.root, a.owner = root, true
				a.grants["/media/Docs"] = []string{"read", "write", "delete"}
				a.mu.Unlock()
				app := "photos"
				if tc.mode == "user" {
					app = ""
				}
				access = scopeAccess(t, p, app)
			}
			view, err := BuildInstanceView(identity, access, caps)
			if err != nil {
				t.Fatal(err)
			}
			if err := view.openRoots(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(view.closeRoots)
			entry, _, err := view.Resolve("Docs")
			if err != nil {
				t.Fatal(err)
			}
			source := &instanceDir{view: view, entry: entry, rel: "A"}
			target := &instanceDir{view: view, entry: entry, rel: "B"}
			want := syscall.EACCES
			if tc.allowed {
				want = 0
			}
			for _, op := range []struct {
				name string
				run  func() syscall.Errno
			}{
				{"unlink", func() syscall.Errno { return source.Unlink(t.Context(), "delete") }},
				{"rmdir", func() syscall.Errno { return source.Rmdir(t.Context(), "empty") }},
				{"move", func() syscall.Errno { return source.Rename(t.Context(), "move", target, "moved", 0) }},
				{"overwrite", func() syscall.Errno { return source.Rename(t.Context(), "replace", source, "target", 0) }},
			} {
				if got := op.run(); got != want {
					t.Errorf("%s = %v, want %v", op.name, got, want)
				}
			}
			if tc.allowed {
				items, err := entry.protection.ListTrash()
				if err != nil || len(items) != 2 {
					t.Fatalf("unlink/rmdir must preserve recoverable entries: %v, %v", items, err)
				}
			} else {
				for _, name := range []string{"A/delete", "A/move", "A/replace", "A/target"} {
					if data, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(data) != name {
						t.Errorf("denied operation changed %s: %q, %v", name, data, err)
					}
				}
				if err := source.Rename(t.Context(), "move", source, "renamed", 0); err != 0 {
					t.Fatalf("non-overwriting same-directory rename: %v", err)
				}
			}
		})
	}
}

func TestDeleteHandlersRecheckLiveRelations(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"A/empty", "B"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"A/delete", "A/keep", "A/move", "A/replace", "B/target"} {
		write(t, root, name, name)
	}
	p, a := newAgentScopeProvider(t)
	a.mu.Lock()
	a.root, a.owner = root, true
	a.grants["/media/Docs"] = []string{"read", "write"}
	a.mu.Unlock()
	view, err := BuildInstanceView(&ZimaIdentity{Username: "alice", UserID: 7}, scopeAccess(t, p, "photos"), AgentCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	if err := view.openRoots(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(view.closeRoots)
	entry, _, err := view.Resolve("Docs")
	if err != nil {
		t.Fatal(err)
	}
	source := &instanceDir{view: view, entry: entry, rel: "A"}
	target := &instanceDir{view: view, entry: entry, rel: "B"}
	changed := make(chan int64, 8)
	p.OnChange(func(g int64) { changed <- g })
	change := func(mutate func()) {
		t.Helper()
		a.change(t, p, "path", mutate)
		a.mu.Lock()
		generation := a.generation
		a.mu.Unlock()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case got := <-changed:
				if got == generation {
					return
				}
			case <-deadline:
				t.Fatal("permission generation was not re-warmed")
			}
		}
	}
	if got := source.Unlink(t.Context(), "delete"); got != syscall.EACCES {
		t.Fatalf("read/write deletion = %v", got)
	}
	change(func() { a.grants["/media/Docs"] = []string{"read", "write", "delete"} })
	if got := source.Unlink(t.Context(), "delete"); got != 0 {
		t.Fatalf("granted deletion = %v", got)
	}
	if got := source.Rmdir(t.Context(), "empty"); got != 0 {
		t.Fatalf("granted rmdir = %v", got)
	}
	change(func() { a.denied["/media/Docs/A"] = []string{"delete"} })
	if got := source.Rename(t.Context(), "move", target, "moved", 0); got != syscall.EACCES {
		t.Fatalf("move without source delete = %v", got)
	}
	change(func() {
		delete(a.denied, "/media/Docs/A")
		a.denied["/media/Docs/B"] = []string{"delete"}
	})
	if got := source.Rename(t.Context(), "replace", target, "target", 0); got != syscall.EACCES {
		t.Fatalf("overwrite without target delete = %v", got)
	}
	if got := source.Rename(t.Context(), "move", target, "moved", 0); got != 0 {
		t.Fatalf("non-overwriting move incorrectly requires target delete: %v", got)
	}
	change(func() {
		a.denied = map[string][]string{}
		a.grants["/media/Docs"] = []string{"read", "write"}
	})
	if got := source.Unlink(t.Context(), "keep"); got != syscall.EACCES {
		t.Fatalf("revoked deletion = %v", got)
	}
	if got := source.Rename(t.Context(), "replace", target, "target", 0); got != syscall.EACCES {
		t.Fatalf("revoked overwrite = %v", got)
	}
	for _, name := range []string{"A/keep", "A/replace", "B/target"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(data) != name {
			t.Fatalf("denied mutation changed %s: %q, %v", name, data, err)
		}
	}
	if got := source.Rename(t.Context(), "keep", source, "renamed", 0); got != 0 {
		t.Fatalf("non-overwriting same-directory rename after delete revocation = %v", got)
	}
	change(func() { a.grants = map[string][]string{} })
	if got := source.Unlink(t.Context(), "renamed"); got != syscall.EACCES {
		t.Fatalf("old directory retained access after clearing scope: %v", got)
	}
	if view.Allows(entry, "A/renamed", OpRead|OpModify) {
		t.Fatal("old view retained read/write after clearing scope")
	}
}
