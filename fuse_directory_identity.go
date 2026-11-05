//go:build linux || darwin

package toolfs

import (
	"context"
	"os"
	"syscall"

	gofs "github.com/hanwen/go-fuse/v2/fs"
)

// directoryInode reuses the live go-fuse child only after Lookup has rechecked
// authorization and backing identity. Allocating a new inode on every lookup
// unhashes an unchanged directory held as cwd; getcwd then returns ENOENT.
// The child tree owns this cache and go-fuse forgets it with kernel references.
func directoryInode(ctx context.Context, parent *gofs.Inode, view *InstanceView, name string, ops gofs.InodeEmbedder) *gofs.Inode {
	view.inodeMu.Lock()
	defer view.inodeMu.Unlock()
	if child := parent.GetChild(name); child != nil && sameDirectory(child.Operations(), ops) {
		return child
	}
	child := parent.NewInode(ctx, ops, gofs.StableAttr{Mode: syscall.S_IFDIR})
	// Publish before unlocking so concurrent lookups cannot each return a new
	// inode. The bridge subsequently attaches this same child to this name.
	parent.AddChild(name, child, true)
	return child
}

func sameDirectory(old, next gofs.InodeEmbedder) bool {
	switch a := old.(type) {
	case *instanceDir:
		b, ok := next.(*instanceDir)
		return ok && a.view == b.view && a.entry == b.entry && a.rel == b.rel &&
			a.backing != nil && b.backing != nil && os.SameFile(a.backing, b.backing)
	case *synthDir:
		b, ok := next.(*synthDir)
		return ok && a.view == b.view && a.node == b.node
	case *controlDir:
		b, ok := next.(*controlDir)
		return ok && a.view == b.view
	}
	return false
}
