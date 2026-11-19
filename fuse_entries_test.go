//go:build linux || darwin

package toolfs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/IceWhaleTech/ZimaOS-Common/v2/permission"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

func TestEntriesFUSEOnlyTraversesEffectiveDescendants(t *testing.T) {
	_, _, base, view := entriesFixture(t)
	if err := view.openRoots(); err != nil {
		t.Fatal(err)
	}
	defer view.closeRoots()
	for _, rel := range []string{"Backup/.toolfs-state", "Backup/Projects/.toolfs-state", "System/.home/.toolfs-state", "System/.home/bob/.toolfs-state"} {
		if _, err := os.Stat(filepath.Join(base, rel)); !os.IsNotExist(err) {
			t.Fatalf("navigation touched %s: %v", rel, err)
		}
	}
	root := &instanceRoot{view: view}
	gofs.NewNodeFS(root, &gofs.Options{})
	var out fuse.EntryOut
	home, eno := root.Lookup(t.Context(), "Home", &out)
	if eno != 0 {
		t.Fatal(eno)
	}
	bob, eno := home.Operations().(*synthDir).Lookup(t.Context(), "bob", &out)
	if eno != 0 {
		t.Fatal(eno)
	}
	if _, eno := bob.Operations().(*synthDir).Lookup(t.Context(), "private", &out); eno != syscall.ENOENT {
		t.Fatal("unshared sibling visible", eno)
	}
	if _, eno := home.Operations().(*synthDir).Lookup(t.Context(), "carol", &out); eno != syscall.ENOENT {
		t.Fatal("unshared owner visible", eno)
	}
	if _, eno := bob.Operations().(*synthDir).Lookup(t.Context(), "Photos", &out); eno != 0 {
		t.Fatal(eno)
	}
}

func TestEntriesFUSEAllowDenyDeepAllowNavigation(t *testing.T) {
	a, p, base, _ := entriesFixture(t)
	a.change(func(allow, deny map[string][]string) {
		allow["/media/Backup"] = []string{"read", "write"}
		deny["/media/Backup/Projects"] = []string{"read", "write", "delete"}
	})
	view, err := BuildInstanceViewForIdentity(t.Context(), p, &ZimaIdentity{Username: "alice", UserID: 7}, "", AgentCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	if err := view.openRoots(); err != nil {
		t.Fatal(err)
	}
	defer view.closeRoots()
	root := &instanceRoot{view: view}
	gofs.NewNodeFS(root, &gofs.Options{})
	var out fuse.EntryOut
	backup, eno := root.Lookup(t.Context(), "Backup", &out)
	if eno != 0 {
		t.Fatal(eno)
	}
	projects, eno := backup.Operations().(*instanceDir).Lookup(t.Context(), "Projects", &out)
	if eno != 0 {
		t.Fatalf("deny ancestor interrupted navigation: %v", eno)
	}
	synthetic, ok := projects.Operations().(*synthDir)
	if !ok {
		t.Fatal("denied parent has real backing")
	}
	if _, eno := synthetic.Lookup(t.Context(), "hidden", &out); eno != syscall.ENOENT {
		t.Fatal("denied sibling visible", eno)
	}
	if _, eno := synthetic.Lookup(t.Context(), "A", &out); eno != 0 {
		t.Fatal(eno)
	}
	if _, err := os.Stat(filepath.Join(base, "Backup/Projects/.toolfs-state")); !os.IsNotExist(err) {
		t.Fatal("protection created in denied parent")
	}
}

func TestEntriesFUSEMetadataTopologyKeepsStableInodes(t *testing.T) {
	a, p, base, _ := entriesFixture(t)
	a.mu.Lock()
	a.pathAliases = append(a.pathAliases, permission.PathAlias{Canonical: "/media", Alias: base})
	a.mu.Unlock()
	a.change(func(allow, deny map[string][]string) {
		allow["/media"] = []string{"read"}
		deny["/media/Home"] = []string{"read", "write", "delete"}
	})
	view, err := BuildInstanceViewForIdentity(t.Context(), p, &ZimaIdentity{Username: "alice", UserID: 7}, "", AgentCapabilities{})
	if err != nil {
		t.Fatal(err)
	}

	if err := view.openRoots(); err != nil {
		t.Fatal(err)
	}
	defer view.closeRoots()
	root := &instanceRoot{view: view}
	gofs.NewNodeFS(root, &gofs.Options{})
	var out fuse.EntryOut
	home, eno := root.Lookup(t.Context(), "Home", &out)
	if eno != 0 {
		t.Fatal(eno)
	}
	if err := os.MkdirAll(filepath.Join(base, "Extra"), 0700); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.entries = append(a.entries, PermissionEntry{Kind: "storage", Path: "/media/Extra", ViewPath: "/Extra", DefaultPath: "/media/Extra"})
	a.mu.Unlock()
	// Use an existing broad cached rule; only the metadata membership changes.
	next, err := BuildInstanceViewForIdentity(t.Context(), p, view.Identity, "", view.Caps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := view.reproject(next); err != nil {
		t.Fatal(err)
	}
	if _, eno := root.Lookup(t.Context(), "Extra", &out); eno != 0 {
		t.Fatal("same-generation metadata disk absent", eno)
	}
	if got, eno := root.Lookup(t.Context(), "Home", &out); eno != 0 || got != home {
		t.Fatal("unaffected Home inode detached", eno)
	}
}
