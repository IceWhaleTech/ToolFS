//go:build linux || darwin

package toolfs

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

func TestDirectoryIdentityRevalidatesBackingAndRevokedGrant(t *testing.T) {
	base, resolver, provider := twoUserFixture(t)
	ctx := context.Background()
	view, err := BuildInstanceViewForUser(ctx, resolver, provider, "agent-user-1", "", capsBase(base))
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
	docs, eno := root.Lookup(ctx, "Documents", &out)
	if eno != 0 {
		t.Fatal(eno)
	}
	again, eno := root.Lookup(ctx, "Documents", &out)
	if eno != 0 || docs != again {
		t.Fatal("unchanged grant directory lost its inode")
	}
	host := filepath.Join(base, "Documents", "nested")
	if err := os.Mkdir(host, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := docs.Operations().(*instanceDir)
	old, eno := dir.Lookup(ctx, "nested", &out)
	if eno != 0 {
		t.Fatal(eno)
	}
	if err := os.Rename(host, host+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(host, 0o755); err != nil {
		t.Fatal(err)
	}
	next, eno := dir.Lookup(ctx, "nested", &out)
	if eno != 0 || old == next {
		t.Fatal("replaced backing directory reused the previous inode")
	}
	provider.SetGrants("alice", nil)
	fresh, err := BuildInstanceViewForIdentity(ctx, provider, view.Identity, "", capsBase(base))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = view.reproject(fresh); err != nil {
		t.Fatal(err)
	}
	if _, eno = root.Lookup(ctx, "Documents", &out); eno != syscall.ENOENT {
		t.Fatalf("revoked root lookup = %v", eno)
	}
	var attr fuse.AttrOut
	if eno = dir.Getattr(ctx, nil, &attr); eno != syscall.ENOENT {
		t.Fatalf("held directory getattr after revocation = %v", eno)
	}
}

func TestStorageProjectionKeepsDirectoryInodesAndRefreshesSyntheticChildren(t *testing.T) {
	base, view := expansionFixture(t, "user")
	home := filepath.Join(base, "Disk", ".home")
	if err := os.MkdirAll(filepath.Join(home, "alice"), 0700); err != nil {
		t.Fatal(err)
	}
	view.Caps.ViewAliases = append(view.Caps.ViewAliases, PathAlias{Host: home, View: "/Home"})
	view.Access = NewAccessSnapshot("alice", 7, "user", []Grant{{Path: filepath.Join(base, "Disk")}, {Path: filepath.Join(base, "Backup")}, {Path: filepath.Join(home, "alice")}}, 1)
	var err error
	view, err = BuildInstanceView(view.Identity, view.Access, view.Caps)
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
	disk, eno := root.Lookup(t.Context(), "Disk", &out)
	if eno != 0 {
		t.Fatal(eno)
	}
	homeInode, eno := root.Lookup(t.Context(), "Home", &out)
	if eno != 0 {
		t.Fatal(eno)
	}
	heldHome := homeInode.Operations().(*synthDir)
	if err := os.Mkdir(filepath.Join(home, "bob"), 0700); err != nil {
		t.Fatal(err)
	}
	nextAccess := NewAccessSnapshot("alice", 7, "user", []Grant{{Path: filepath.Join(base, "Disk")}, {Path: filepath.Join(base, "Backup")}, {Path: filepath.Join(home, "alice")}, {Path: filepath.Join(home, "bob")}}, 1)
	next, err := BuildInstanceView(view.Identity, nextAccess, view.Caps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := view.reproject(next); err != nil {
		t.Fatal(err)
	}
	if got, eno := root.Lookup(t.Context(), "Disk", &out); eno != 0 || got != disk {
		t.Fatal("unaffected disk inode detached")
	}
	if got, eno := root.Lookup(t.Context(), "Home", &out); eno != 0 || got != homeInode {
		t.Fatal("synthetic Home inode detached")
	}
	if _, eno := heldHome.Lookup(t.Context(), "bob", &out); eno != 0 {
		t.Fatalf("held synthetic directory retained old child tree: %v", eno)
	}
	next, err = BuildInstanceView(view.Identity, NewAccessSnapshot("alice", 7, "user", []Grant{{Path: filepath.Join(base, "Disk")}, {Path: filepath.Join(base, "Backup")}}, 1), view.Caps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := view.reproject(next); err != nil {
		t.Fatal(err)
	}
	if _, eno := heldHome.Lookup(t.Context(), "alice", &out); eno != syscall.ENOENT {
		t.Fatalf("retired synthetic directory retained children: %v", eno)
	}
}

func TestStoragePermissionCacheSeparatesSameBasenameEntries(t *testing.T) {
	base := t.TempDir()
	read, write := filepath.Join(base, "a", "shared"), filepath.Join(base, "b", "shared")
	access := NewAccessSnapshot("alice", 7, "user", []Grant{{Path: read, ReadOnly: true}, {Path: write}}, 1)
	view, err := BuildInstanceView(&ZimaIdentity{Username: "alice", UserID: 7}, access, capsBase(base))
	if err != nil {
		t.Fatal(err)
	}
	if view.Allows(entryFor(t, view, "a/shared"), "file", OpModify) || !view.Allows(entryFor(t, view, "b/shared"), "file", OpModify) {
		t.Fatal("same basename entries shared a permission cache key")
	}
}
