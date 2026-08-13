//go:build linux || darwin
// +build linux darwin

package toolfs

// Protected mutations on the RBAC mount.
//
// Every destructive operation the agent can reach goes through here, which is
// the whole point: an agent running `rm` inside its mount takes the same path
// as one calling an API, because both land on the same FUSE handler.
//
// Two rules shape the semantics:
//
//   - Deletion is withheld unless the instance holds AllowDelete. ZimaOS only
//     distinguishes read-only from read-write, so without this split "writable"
//     would imply "deletable".
//   - When deletion *is* permitted, it is still recoverable: the entry is
//     renamed into the grant's state area rather than unlinked.

import (
	"context"
	"sync"
	"syscall"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

var (
	_ gofs.NodeCreater  = (*instanceDir)(nil)
	_ gofs.NodeMkdirer  = (*instanceDir)(nil)
	_ gofs.NodeUnlinker = (*instanceDir)(nil)
	_ gofs.NodeRmdirer  = (*instanceDir)(nil)
	_ gofs.NodeRenamer  = (*instanceDir)(nil)
)

// writable reports whether this directory's grant permits mutation at all.
func (d *instanceDir) writable() bool {
	return !d.entry.ReadOnly && d.entry.protection != nil
}

// Create makes a new file and returns an open handle for it.
func (d *instanceDir) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (
	*gofs.Inode, gofs.FileHandle, uint32, syscall.Errno) {

	childRel := joinRel(d.rel, name)
	if isReservedRel(childRel) {
		// The state area is not part of the namespace and must not be writable
		// through it, or an agent could reach other users' recoverable content.
		return nil, nil, 0, syscall.EACCES
	}
	if !d.writable() {
		return nil, nil, 0, syscall.EROFS
	}
	if !d.view.Allows(d.entry, childRel, OpCreate) {
		return nil, nil, 0, syscall.EACCES
	}

	// Creating over an existing file replaces its content, so preserve the old
	// content first — this is the same protection an overwrite gets.
	if _, err := d.entry.root.Stat(childRel); err == nil {
		if !d.view.Allows(d.entry, childRel, OpModify) {
			return nil, nil, 0, syscall.EACCES
		}
		if _, err := d.entry.protection.CaptureVersion(d.view.Identity.Username, childRel); err != nil {
			return nil, nil, 0, errnoFor(err)
		}
	}

	if err := d.entry.root.WriteFile(childRel, nil, 0o644); err != nil {
		return nil, nil, 0, errnoFor(err)
	}

	child := &instanceFile{view: d.view, entry: d.entry, rel: childRel}
	out.SetEntryTimeout(0)
	out.SetAttrTimeout(0)
	out.Mode = syscall.S_IFREG | fileMode(d.entry)
	inode := d.NewInode(ctx, child, gofs.StableAttr{Mode: syscall.S_IFREG})

	return inode, &instanceWriteHandle{
		view: d.view, entry: d.entry, rel: childRel,
		// Create has just made the file, so a later ReadFile would succeed and
		// wrongly suggest it pre-existed. Record the truth here instead.
		isNew: true,
	}, fuse.FOPEN_DIRECT_IO, 0
}

// Mkdir creates a directory.
func (d *instanceDir) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (
	*gofs.Inode, syscall.Errno) {

	childRel := joinRel(d.rel, name)
	if isReservedRel(childRel) {
		return nil, syscall.EACCES
	}
	if !d.writable() {
		return nil, syscall.EROFS
	}
	if !d.view.Allows(d.entry, childRel, OpCreate) {
		return nil, syscall.EACCES
	}
	if err := d.entry.root.Mkdir(childRel, 0o755); err != nil {
		return nil, errnoFor(err)
	}
	d.entry.protection.journal(d.view, ProtectedChange{Op: MutationCreate, Path: childRel})

	child := &instanceDir{view: d.view, entry: d.entry, rel: childRel}
	out.SetEntryTimeout(0)
	out.SetAttrTimeout(0)
	out.Mode = syscall.S_IFDIR | entryMode(d.entry)
	return d.NewInode(ctx, child, gofs.StableAttr{Mode: syscall.S_IFDIR}), 0
}

// Unlink moves a file into the grant's trash.
func (d *instanceDir) Unlink(ctx context.Context, name string) syscall.Errno {
	return d.trash(joinRel(d.rel, name))
}

// Rmdir moves a directory into the grant's trash.
//
// This renames only the directory itself, so removing a deep tree stays O(1)
// instead of walking every entry beneath it.
func (d *instanceDir) Rmdir(ctx context.Context, name string) syscall.Errno {
	return d.trash(joinRel(d.rel, name))
}

// trash is the shared deletion path for files and directories.
func (d *instanceDir) trash(childRel string) syscall.Errno {
	if isReservedRel(childRel) {
		return syscall.EACCES
	}
	if !d.writable() {
		return syscall.EROFS
	}
	// Delete is withheld by default. Reporting EPERM rather than EACCES says
	// "this instance may not delete", not "you cannot reach this path".
	if !d.view.Caps.AllowDelete {
		return syscall.EPERM
	}
	if !d.view.Allows(d.entry, childRel, OpDelete) {
		return syscall.EACCES
	}
	entry, err := d.entry.protection.Trash(d.view.Identity.Username, childRel)
	if err != nil {
		return errnoFor(err)
	}
	d.entry.protection.journal(d.view, ProtectedChange{
		Op: MutationDelete, Path: childRel, Recovery: entry.ID,
	})
	return 0
}

// Rename moves an entry within the same grant.
//
// Cross-grant renames are refused with EXDEV. The two grants may carry
// different permissions and even live on different filesystems, and the kernel
// falls back to copy-then-delete when it sees EXDEV, which routes the deletion
// back through the protected path.
func (d *instanceDir) Rename(ctx context.Context, name string, newParent gofs.InodeEmbedder,
	newName string, flags uint32) syscall.Errno {

	target, ok := newParent.(*instanceDir)
	if !ok || target.entry != d.entry {
		return syscall.EXDEV
	}

	oldRel := joinRel(d.rel, name)
	newRel := joinRel(target.rel, newName)
	if isReservedRel(oldRel) || isReservedRel(newRel) {
		return syscall.EACCES
	}
	if !d.writable() {
		return syscall.EROFS
	}
	// A rename removes the source name, so it needs the same right as deletion.
	if !d.view.Caps.AllowDelete {
		return syscall.EPERM
	}
	if !d.view.Allows(d.entry, oldRel, OpDelete) || !d.view.Allows(d.entry, newRel, OpCreate) {
		return syscall.EACCES
	}

	// Overwriting an existing target destroys its content; preserve it first.
	if _, err := d.entry.root.Stat(newRel); err == nil {
		if _, err := d.entry.protection.CaptureVersion(d.view.Identity.Username, newRel); err != nil {
			return errnoFor(err)
		}
	}

	if err := d.entry.root.Rename(oldRel, newRel); err != nil {
		return errnoFor(err)
	}
	size, mtime := d.entry.protection.afterState(newRel)
	d.entry.protection.journal(d.view, ProtectedChange{
		Op: MutationRename, Path: newRel, FromPath: oldRel,
		AfterSize: size, AfterMtime: mtime,
	})
	return 0
}

// instanceWriteHandle streams writes straight to the backing file.
//
// It deliberately does not buffer the file in memory. An earlier version did,
// growing a slice on every write — that is quadratic in file size and puts the
// whole file in RAM, which showed up immediately under load.
//
// The previous content is preserved exactly once, before the first byte is
// accepted, rather than on every partial write.
type instanceWriteHandle struct {
	view  *InstanceView
	entry *NamespaceEntry
	rel   string

	// mu guards the lazily-opened file: the kernel may issue concurrent writes
	// against one handle.
	mu        sync.Mutex
	file      WritableFile
	captured  bool
	versionID string
	dirty     bool
	journaled bool
	// isNew marks a handle returned from Create, so the undo of this write is
	// a removal rather than a restore.
	isNew bool
}

var (
	_ gofs.FileWriter   = (*instanceWriteHandle)(nil)
	_ gofs.FileFlusher  = (*instanceWriteHandle)(nil)
	_ gofs.FileReleaser = (*instanceWriteHandle)(nil)
)

// ensureOpen captures the previous content once, then opens the backing file.
// Callers must hold h.mu.
func (h *instanceWriteHandle) ensureOpen() syscall.Errno {
	if h.file != nil {
		return 0
	}
	if !h.captured {
		// A newly created file has no previous content worth preserving. Skipping
		// the capture is not just an optimization: Create has already made an
		// empty file, so the capture would see it as "existing" and store an
		// empty version for every single create — and version pruning scans all
		// metadata, making bulk file creation quadratic.
		if h.isNew {
			h.captured = true
			return h.openBacking()
		}

		version, err := h.entry.protection.CaptureVersion(h.view.Identity.Username, h.rel)
		if err != nil {
			return errnoFor(err)
		}
		if version != nil {
			h.versionID = version.ID
		} else {
			// The capture may have been debounced, or already taken by the
			// truncate that preceded this open. Either way the previous content
			// is preserved — point the journal at that capture.
			h.versionID = h.entry.protection.latestVersionID(h.rel)
		}
		h.captured = true
	}
	return h.openBacking()
}

// openBacking opens the backing file for writing. Callers must hold h.mu.
func (h *instanceWriteHandle) openBacking() syscall.Errno {
	file, err := h.entry.root.OpenWrite(h.rel, 0o644)
	if err != nil {
		return errnoFor(err)
	}
	h.file = file
	return 0
}

func (h *instanceWriteHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	if !h.view.Allows(h.entry, h.rel, OpModify) {
		return 0, syscall.EACCES
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if errno := h.ensureOpen(); errno != 0 {
		return 0, errno
	}
	written, err := h.file.WriteAt(data, off)
	if err != nil {
		return uint32(written), errnoFor(err)
	}
	h.dirty = true
	return uint32(written), 0
}

// Flush pushes pending data out and records the change.
//
// Both happen here rather than in Release because RELEASE is asynchronous:
// close() only triggers FLUSH synchronously, and the kernel may deliver RELEASE
// well after close() returns. Journalling from Release therefore loses the
// record for anything read back promptly — which is exactly what an agent does
// when it finishes a task and immediately asks what changed.
//
// A journaled flag keeps it to one entry, since Flush can fire several times
// for one open.
func (h *instanceWriteHandle) Flush(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.file == nil {
		return 0
	}
	// Deliberately no Sync() here. FUSE delivers FSYNC as its own operation, so
	// forcing a flush-to-disk on every close would fsync once per file — which
	// dominates small-file workloads (measured ~7ms per create) for a guarantee
	// the caller did not ask for.
	if !h.dirty || h.journaled {
		return 0
	}
	h.journaled = true

	op := MutationModify
	if h.isNew {
		op = MutationCreate
	}
	size, mtime := h.entry.protection.afterState(h.rel)
	h.entry.protection.journal(h.view, ProtectedChange{
		Op: op, Path: h.rel, Recovery: h.versionID,
		AfterSize: size, AfterMtime: mtime,
	})
	return 0
}

// Release closes the backing descriptor. The change was already recorded by
// Flush; see the note there on why it cannot be done here.
func (h *instanceWriteHandle) Release(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.file == nil {
		return 0
	}
	err := h.file.Close()
	h.file = nil
	return errnoFor(err)
}

var _ gofs.NodeSetattrer = (*instanceFile)(nil)

// Setattr handles truncation, which is how an overwrite usually begins:
// os.WriteFile opens with O_TRUNC, and without this the kernel gets ENOTSUP and
// the whole write fails.
//
// Truncation destroys content, so it takes the same protection as a write —
// the previous content is preserved before the file is shortened.
func (f *instanceFile) Setattr(ctx context.Context, fh gofs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	size, hasSize := in.GetSize()
	if !hasSize {
		// Mode, owner and timestamps are not modelled: ZimaOS stores no POSIX
		// permission semantics for this data, so accepting them would imply a
		// guarantee that does not exist. Report success so tools that set them
		// opportunistically do not fail.
		return f.Getattr(ctx, fh, out)
	}

	if isReservedRel(f.rel) {
		return syscall.EACCES
	}
	if f.entry.ReadOnly || f.entry.protection == nil {
		return syscall.EROFS
	}
	if !f.view.Allows(f.entry, f.rel, OpModify) {
		return syscall.EACCES
	}

	if _, err := f.entry.protection.CaptureVersion(f.view.Identity.Username, f.rel); err != nil {
		return errnoFor(err)
	}

	content, err := f.entry.root.ReadFile(f.rel)
	if err != nil {
		return errnoFor(err)
	}
	switch {
	case size < uint64(len(content)):
		content = content[:size]
	case size > uint64(len(content)):
		grown := make([]byte, size)
		copy(grown, content)
		content = grown
	}
	if err := f.entry.root.WriteFile(f.rel, content, 0o644); err != nil {
		return errnoFor(err)
	}

	return f.Getattr(ctx, fh, out)
}
