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
