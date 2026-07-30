//go:build linux || darwin
// +build linux darwin

package toolfs

// Per-user RBAC mount view.
//
// This is a separate node tree from the legacy ToolFSRoot on purpose. The
// legacy root exposes memory, rag and skill mounts, none of which belong in an
// authorization-derived namespace: holding a file grant must never imply
// access to a virtual backend. Keeping the trees apart also means the RBAC
// path cannot regress existing behaviour.
//
// Caching is disabled throughout. Entry, attribute and negative TTLs are zero
// and file handles do not request FOPEN_KEEP_CACHE, because a cached entry
// outliving a revoked grant would serve data the user is no longer allowed to
// see. The cost is performance; the permission *decision* may be cached in
// user space instead, keyed by generation.

import (
	"context"
	"errors"
	"io/fs"
	"syscall"
	"time"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// MountHandle controls the lifetime of one user's mount.
type MountHandle interface {
	// MountPoint is the path the view is served at.
	MountPoint() string
	// Identity is the ZimaOS subject this mount is bound to.
	Identity() *ZimaIdentity
	// Unmount tears the mount down and releases descriptors. Idempotent.
	Unmount() error
	// Wait blocks until the mount is torn down.
	Wait()
}

// MountConfig tunes a per-user mount.
type MountConfig struct {
	// AllowOther exposes the mount to other local users. Left false: under
	// host-orchestrated containers the only consumer is the one container the
	// mount is bound into, so cross-user local access is never needed.
	AllowOther bool
	// Debug enables go-fuse protocol logging.
	Debug bool
}

// mountHandle is the concrete MountHandle.
type mountHandle struct {
	mountPoint string
	identity   *ZimaIdentity
	server     *fuse.Server
	view       *InstanceView
	unmounted  bool
}

func (h *mountHandle) MountPoint() string      { return h.mountPoint }
func (h *mountHandle) Identity() *ZimaIdentity { return h.identity }
func (h *mountHandle) Wait()                   { h.server.Wait() }

// Unmount is safe to call repeatedly; the second call is a no-op.
func (h *mountHandle) Unmount() error {
	if h.unmounted {
		return nil
	}
	h.unmounted = true
	err := h.server.Unmount()
	h.view.closeRoots()
	return err
}

// MountForIdentity serves a user's authorized namespace at mountPoint.
//
// The mount point carries the identity: whichever instance is given this
// mount is confined to this user's grants, and the instance itself stays
// identity-agnostic — it only ever sees a directory.
//
// Note the expected layout (see the design doc §6.6): mountPoint should be a
// directory *below* the stable bind source, e.g. /mnt/toolfs/<uid>/workspace,
// so the daemon can remount without the consumer silently retaining the old
// view.
func MountForIdentity(
	ctx context.Context,
	resolver IdentityResolver,
	provider PermissionProvider,
	agentUserID string,
	mountPoint string,
	caps AgentCapabilities,
	config MountConfig,
) (MountHandle, error) {
	view, err := BuildInstanceViewForUser(ctx, resolver, provider, agentUserID, caps)
	if err != nil {
		return nil, err
	}
	if err := view.openRoots(); err != nil {
		return nil, err
	}
	return mountView(mountPoint, view, config)
}

// mountView serves an already-built view whose roots are already open.
//
// Split out from MountForIdentity so a caller that needs to configure the view
// first — stamping the agent-side session and task labels, for instance — can
// still reach the same mount path.
func mountView(mountPoint string, view *InstanceView, config MountConfig) (MountHandle, error) {
	root := &instanceRoot{view: view}

	zero := time.Duration(0)
	opts := &gofs.Options{
		EntryTimeout:    &zero,
		AttrTimeout:     &zero,
		NegativeTimeout: &zero,
	}
	opts.MountOptions.Name = "toolfs"
	opts.MountOptions.FsName = "toolfs-" + view.Identity.Username
	opts.MountOptions.AllowOther = config.AllowOther
	opts.MountOptions.Debug = config.Debug

	server, err := gofs.Mount(mountPoint, root, opts)
	if err != nil {
		view.closeRoots()
		return nil, err
	}

	return &mountHandle{
		mountPoint: mountPoint,
		identity:   view.Identity,
		server:     server,
		view:       view,
	}, nil
}

// instanceRoot is the mount root. Its children are exactly the grants.
type instanceRoot struct {
	gofs.Inode
	view *InstanceView
}

var (
	_ gofs.NodeReaddirer = (*instanceRoot)(nil)
	_ gofs.NodeLookuper  = (*instanceRoot)(nil)
)

// Readdir lists the grant-derived entries. It is computed from the current
// view on every call rather than fixed at mount time, so a rebuilt view takes
// effect without remounting.
func (r *instanceRoot) Readdir(ctx context.Context) (gofs.DirStream, syscall.Errno) {
	names := r.view.Entries()
	entries := make([]fuse.DirEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, fuse.DirEntry{Name: name, Mode: syscall.S_IFDIR})
	}
	return gofs.NewListDirStream(entries), 0
}

// Lookup resolves a top-level grant name.
//
// An unknown name yields ENOENT, not EACCES: reporting "permission denied"
// would confirm the path exists and leak the namespace shape.
func (r *instanceRoot) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	entry, ok := r.view.Entry(name)
	if !ok {
		return nil, syscall.ENOENT
	}
	if !r.view.Allows(entry, "", OpList) {
		return nil, syscall.ENOENT
	}

	child := &instanceDir{view: r.view, entry: entry, rel: ""}
	out.SetEntryTimeout(0)
	out.SetAttrTimeout(0)
	out.Mode = syscall.S_IFDIR | entryMode(entry)
	return r.NewInode(ctx, child, gofs.StableAttr{Mode: syscall.S_IFDIR}), 0
}

// instanceDir is a directory beneath one grant.
type instanceDir struct {
	gofs.Inode
	view  *InstanceView
	entry *NamespaceEntry
	rel   string
}

var (
	_ gofs.NodeReaddirer = (*instanceDir)(nil)
	_ gofs.NodeLookuper  = (*instanceDir)(nil)
	_ gofs.NodeGetattrer = (*instanceDir)(nil)
)

func (d *instanceDir) Readdir(ctx context.Context) (gofs.DirStream, syscall.Errno) {
	if !d.view.Allows(d.entry, d.rel, OpList) {
		return nil, syscall.ENOENT
	}
	items, err := d.entry.root.ReadDir(d.rel)
	if err != nil {
		return nil, errnoFor(err)
	}

	entries := make([]fuse.DirEntry, 0, len(items))
	for _, item := range items {
		childRel := joinRel(d.rel, item.Name())
		// The protection area is not part of the namespace: exposing it would
		// let an agent read content that was deleted precisely to protect it.
		if isReservedRel(childRel) {
			continue
		}
		// Hide anything the current grants do not cover, so a nested
		// read-only or denied subtree is not even enumerated.
		if !d.view.Allows(d.entry, childRel, OpRead) {
			continue
		}
		mode := uint32(syscall.S_IFREG)
		if item.IsDir() {
			mode = syscall.S_IFDIR
		}
		entries = append(entries, fuse.DirEntry{Name: item.Name(), Mode: mode})
	}
	return gofs.NewListDirStream(entries), 0
}

func (d *instanceDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	childRel := joinRel(d.rel, name)
	if isReservedRel(childRel) {
		return nil, syscall.ENOENT
	}
	if !d.view.Allows(d.entry, childRel, OpRead) {
		return nil, syscall.ENOENT
	}

	info, err := d.entry.root.Stat(childRel)
	if err != nil {
		return nil, errnoFor(err)
	}

	out.SetEntryTimeout(0)
	out.SetAttrTimeout(0)

	if info.IsDir() {
		child := &instanceDir{view: d.view, entry: d.entry, rel: childRel}
		out.Mode = syscall.S_IFDIR | entryMode(d.entry)
		return d.NewInode(ctx, child, gofs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}

	child := &instanceFile{view: d.view, entry: d.entry, rel: childRel}
	out.Mode = syscall.S_IFREG | fileMode(d.entry)
	out.Size = uint64(info.Size())
	return d.NewInode(ctx, child, gofs.StableAttr{Mode: syscall.S_IFREG}), 0
}

func (d *instanceDir) Getattr(ctx context.Context, fh gofs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | entryMode(d.entry)
	out.SetTimeout(0)
	return 0
}

// instanceFile is a regular file beneath one grant.
type instanceFile struct {
	gofs.Inode
	view  *InstanceView
	entry *NamespaceEntry
	rel   string
}

var (
	_ gofs.NodeOpener    = (*instanceFile)(nil)
	_ gofs.NodeGetattrer = (*instanceFile)(nil)
)

func (f *instanceFile) Getattr(ctx context.Context, fh gofs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if !f.view.Allows(f.entry, f.rel, OpRead) {
		return syscall.ENOENT
	}
	info, err := f.entry.root.Stat(f.rel)
	if err != nil {
		return errnoFor(err)
	}
	out.Mode = syscall.S_IFREG | fileMode(f.entry)
	out.Size = uint64(info.Size())
	out.SetTimeout(0)
	return 0
}

// Open re-checks authorization rather than trusting that Lookup already did.
//
// A handle must not cache an "authorized" boolean: a grant revoked between
// lookup and open has to take effect immediately.
func (f *instanceFile) Open(ctx context.Context, flags uint32) (gofs.FileHandle, uint32, syscall.Errno) {
	wantsWrite := flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_APPEND|syscall.O_TRUNC) != 0

	if !f.view.Allows(f.entry, f.rel, OpRead) {
		return nil, 0, syscall.ENOENT
	}
	if wantsWrite {
		if f.entry.ReadOnly || f.entry.protection == nil {
			return nil, 0, syscall.EROFS
		}
		if !f.view.Allows(f.entry, f.rel, OpModify) {
			return nil, 0, syscall.EACCES
		}
		// Writes go through the protection chain, which preserves the previous
		// content before the first byte is accepted.
		return &instanceWriteHandle{view: f.view, entry: f.entry, rel: f.rel},
			fuse.FOPEN_DIRECT_IO, 0
	}

	data, err := f.entry.root.ReadFile(f.rel)
	if err != nil {
		return nil, 0, errnoFor(err)
	}
	// FOPEN_DIRECT_IO keeps the kernel from caching content, so revoking a
	// grant is not defeated by a page cache hit.
	return &instanceFileHandle{data: data}, fuse.FOPEN_DIRECT_IO, 0
}

// instanceFileHandle serves a snapshot of the file taken at open time.
type instanceFileHandle struct {
	data []byte
}

var _ gofs.FileReader = (*instanceFileHandle)(nil)

func (h *instanceFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off < 0 || off > int64(len(h.data)) {
		return fuse.ReadResultData(nil), 0
	}
	return fuse.ReadResultData(h.data[off:]), 0
}

// entryMode renders directory permission bits from the grant.
func entryMode(entry *NamespaceEntry) uint32 {
	if entry.ReadOnly {
		return 0o555
	}
	return 0o755
}

// fileMode renders file permission bits from the grant.
func fileMode(entry *NamespaceEntry) uint32 {
	if entry.ReadOnly {
		return 0o444
	}
	return 0o644
}

// joinRel appends a component to a grant-relative path.
func joinRel(base, name string) string {
	if base == "" {
		return name
	}
	return base + "/" + name
}

// errnoFor maps Go errors onto FUSE errnos.
//
// Authorization failures deliberately surface as ENOENT so that probing does
// not reveal which paths exist.
func errnoFor(err error) syscall.Errno {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrNotAuthorized), errors.Is(err, fs.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, ErrReadOnlyMount):
		return syscall.EROFS
	case errors.Is(err, fs.ErrPermission):
		return syscall.EACCES
	case errors.Is(err, ErrPathTraversal), errors.Is(err, ErrPathEscapesRoot), errors.Is(err, ErrInvalidPath):
		return syscall.ENOENT
	default:
		var errno syscall.Errno
		if errors.As(err, &errno) {
			return errno
		}
		return syscall.EIO
	}
}
