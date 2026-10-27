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
	"io"
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
	// View is the live InstanceView this mount serves, so the mount set can
	// reproject it in place when the authority's grants change.
	View() *InstanceView
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

	// CacheTimeout, when greater than zero, lets the kernel cache directory
	// entries, attributes and file content for that long.
	//
	// It defaults to zero — no caching — because a cached entry outliving a
	// revoked grant would serve data the user is no longer allowed to see.
	// Enabling it is only safe once revocation is event-driven, so that a
	// permission change can actively invalidate rather than wait for a TTL.
	CacheTimeout time.Duration

	// ZeroCopyRead hands the kernel the backing descriptor so it can splice
	// straight into the reply, skipping the bounce through user space.
	//
	// Off by default: measured on a memory-backed store it is *slower* than a
	// plain copy, because the data is already in the page cache and splice only
	// adds bookkeeping. It is expected to pay off on real disks, where the copy
	// it removes is the expensive one — but that has not been established here,
	// so it stays opt-in.
	ZeroCopyRead bool
}

type fuseMountServer interface {
	Unmount() error
	Wait()
}

// mountHandle is the concrete MountHandle.
type mountHandle struct {
	mountPoint string
	identity   *ZimaIdentity
	server     fuseMountServer
	view       *InstanceView
	unmounted  bool
}

func (h *mountHandle) MountPoint() string      { return h.mountPoint }
func (h *mountHandle) Identity() *ZimaIdentity { return h.identity }
func (h *mountHandle) View() *InstanceView     { return h.view }
func (h *mountHandle) Wait()                   { h.server.Wait() }

// Unmount is safe to call repeatedly; the second call is a no-op.
func (h *mountHandle) Unmount() error {
	if h.unmounted {
		return nil
	}
	if err := h.server.Unmount(); err != nil {
		return err
	}
	h.unmounted = true
	h.view.closeRoots()
	return nil
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
	app string,
	mountPoint string,
	caps AgentCapabilities,
	config MountConfig,
) (MountHandle, error) {
	view, err := BuildInstanceViewForUser(ctx, resolver, provider, agentUserID, app, caps)
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
	view.CacheTimeout = config.CacheTimeout
	view.ZeroCopyRead = config.ZeroCopyRead
	root := &instanceRoot{view: view}

	ttl := config.CacheTimeout
	opts := &gofs.Options{
		EntryTimeout:    &ttl,
		AttrTimeout:     &ttl,
		NegativeTimeout: &ttl,
	}
	// go-fuse defaults MaxWrite to 64 KiB, which costs a round trip every
	// 64 KiB of sequential IO. Raising it to 1 MiB — the modern kernel's
	// FUSE_MAX_PAGES ceiling — cuts that by 16x. MaxReadAhead lets the kernel
	// fetch ahead in equally large chunks, and MaxBackground raises how many
	// requests may be in flight at once.
	opts.MountOptions.MaxWrite = 1 << 20
	opts.MountOptions.MaxReadAhead = 1 << 20
	opts.MountOptions.MaxBackground = 64

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
	_ gofs.NodeGetattrer = (*instanceRoot)(nil)
)

// Getattr reports the mount root. It is a virtual directory with no backing
// path; the projection time is its honest timestamp.
func (r *instanceRoot) Getattr(ctx context.Context, fh gofs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | 0o555
	built := r.view.BuiltAt
	out.SetTimes(&built, &built, &built)
	out.Nlink = 1
	out.SetTimeout(r.view.CacheTimeout)
	return 0
}

// Readdir lists the grant-derived entries. It is computed from the current
// view on every call rather than fixed at mount time, so a rebuilt view takes
// effect without remounting.
func (r *instanceRoot) Readdir(ctx context.Context) (gofs.DirStream, syscall.Errno) {
	names := r.view.Entries()
	entries := make([]fuse.DirEntry, 0, len(names)+1)
	// The control view is the one entry not derived from a grant. It leaks
	// nothing beyond them: its contents are filtered by the same grants at
	// read time (§14.9).
	entries = append(entries, fuse.DirEntry{Name: toolfsControlDir, Mode: syscall.S_IFDIR})
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
	// Reserved names win over user data. A grant literally named ".toolfs"
	// must not be able to shadow the control view; sanitizeEntryName already
	// refuses to produce one, and this is the second half of that guarantee.
	if name == toolfsControlDir {
		child := &controlDir{view: r.view}
		out.SetEntryTimeout(0)
		out.SetAttrTimeout(0)
		out.Mode = syscall.S_IFDIR | 0o555
		built := r.view.BuiltAt
		out.SetTimes(&built, &built, &built)
		out.Nlink = 1
		return r.NewInode(ctx, child, gofs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}

	node, ok := r.view.rootChild(name)
	if !ok {
		return nil, syscall.ENOENT
	}
	return childInode(ctx, &r.Inode, r.view, node, out)
}

// childInode builds the FUSE inode for a resolved view-tree child. A grant node
// becomes a host-backed directory; a synthetic node becomes a traversal-only
// directory that lists only its granted descendants.
func childInode(
	ctx context.Context,
	parent *gofs.Inode,
	view *InstanceView,
	node *viewNode,
	out *fuse.EntryOut,
) (*gofs.Inode, syscall.Errno) {
	out.SetEntryTimeout(0)
	out.SetAttrTimeout(0)
	if node.entry != nil {
		entry := node.entry
		if entry.fileName != "" {
			// A grant on a single file: the leaf is the file itself, served from
			// the parent root under fileName. Its decision key is the full path,
			// so Allows uses fileName as the relative path (HostPath is the
			// parent for a file grant).
			if !view.Allows(entry, entry.fileName, OpRead) {
				return nil, syscall.ENOENT
			}
			child := &instanceFile{view: view, entry: entry, rel: entry.fileName}
			out.Mode = syscall.S_IFREG | fileMode(entry)
			if info, err := entry.root.Stat(entry.fileName); err == nil {
				out.Size = uint64(info.Size())
				stampAttrTimes(&out.Attr, info)
			}
			return parent.NewInode(ctx, child, gofs.StableAttr{Mode: syscall.S_IFREG}), 0
		}
		if !view.Allows(entry, "", OpList) {
			return nil, syscall.ENOENT
		}
		child := &instanceDir{view: view, entry: entry, rel: ""}
		out.Mode = syscall.S_IFDIR | entryMode(entry)
		if info, err := entry.root.Stat(""); err == nil {
			stampAttrTimes(&out.Attr, info)
		}
		return parent.NewInode(ctx, child, gofs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}
	child := &synthDir{view: view, node: node}
	out.Mode = syscall.S_IFDIR | 0o555
	stampVirtualTimes(&out.Attr, view)
	return parent.NewInode(ctx, child, gofs.StableAttr{Mode: syscall.S_IFDIR}), 0
}

// synthDir is a synthetic, traversal-only directory: one of the path components
// the alias table places above a grant (/Home, an owner name, /ZimaOS-HD). It
// has no host backing, so it lists only its children in the view tree — never a
// real directory — and grants nothing itself. Every actual permission decision
// still happens at the grant leaves it leads to.
type synthDir struct {
	gofs.Inode
	view *InstanceView
	node *viewNode
}

var (
	_ gofs.NodeReaddirer = (*synthDir)(nil)
	_ gofs.NodeLookuper  = (*synthDir)(nil)
	_ gofs.NodeGetattrer = (*synthDir)(nil)
)

func (d *synthDir) Getattr(ctx context.Context, fh gofs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | 0o555
	stampVirtualTimes(&out.Attr, d.view)
	out.SetTimeout(d.view.CacheTimeout)
	return 0
}

func (d *synthDir) Readdir(ctx context.Context) (gofs.DirStream, syscall.Errno) {
	names := d.node.childNames()
	entries := make([]fuse.DirEntry, 0, len(names))
	for _, name := range names {
		mode := uint32(syscall.S_IFDIR)
		// A child that is a grant on a single file lists as a regular file, not
		// a directory; everything else is a directory (a grant dir or synthetic).
		if child, ok := d.node.child(name); ok && child.entry != nil && child.entry.fileName != "" {
			mode = syscall.S_IFREG
		}
		entries = append(entries, fuse.DirEntry{Name: name, Mode: mode})
	}
	return gofs.NewListDirStream(entries), 0
}

func (d *synthDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	child, ok := d.node.child(name)
	if !ok {
		return nil, syscall.ENOENT
	}
	return childInode(ctx, &d.Inode, d.view, child, out)
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
		// A child the alias table relocates elsewhere is not shown here: .home
		// under a /DATA grant belongs to /Home, so /ZimaOS-HD must not also
		// expose it. Its canonical location lists it.
		if d.view.childDiverges(d.entry, d.rel, item.Name()) {
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
	// Relocated by the alias table (e.g. .home under /DATA → /Home): not
	// reachable here, only at its canonical view path.
	if d.view.childDiverges(d.entry, d.rel, name) {
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
		stampAttrTimes(&out.Attr, info)
		return d.NewInode(ctx, child, gofs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}

	child := &instanceFile{view: d.view, entry: d.entry, rel: childRel}
	out.Mode = syscall.S_IFREG | fileMode(d.entry)
	out.Size = uint64(info.Size())
	stampAttrTimes(&out.Attr, info)
	return d.NewInode(ctx, child, gofs.StableAttr{Mode: syscall.S_IFREG}), 0
}

func (d *instanceDir) Getattr(ctx context.Context, fh gofs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | entryMode(d.entry)
	if info, err := d.entry.root.Stat(d.rel); err == nil {
		stampAttrTimes(&out.Attr, info)
	}
	out.SetTimeout(d.view.CacheTimeout)
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
	stampAttrTimes(&out.Attr, info)
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

	file, err := f.entry.root.OpenRead(f.rel)
	if err != nil {
		return nil, 0, errnoFor(err)
	}
	// Without caching, FOPEN_DIRECT_IO keeps the kernel from serving a page
	// cache hit that would survive a revoked grant. With caching deliberately
	// enabled, that protection moves to event-driven invalidation instead.
	if f.view.CacheTimeout > 0 {
		return &instanceFileHandle{file: file, zeroCopy: f.view.ZeroCopyRead}, fuse.FOPEN_KEEP_CACHE, 0
	}
	return &instanceFileHandle{file: file, zeroCopy: f.view.ZeroCopyRead}, fuse.FOPEN_DIRECT_IO, 0
}

// instanceFileHandle streams from the backing file.
//
// It holds a descriptor rather than the file's content: buffering would put the
// whole file in memory on every open.
type instanceFileHandle struct {
	file     ReadableFile
	zeroCopy bool
	// data serves generated content (the control view), which is small and has
	// no backing descriptor.
	data []byte
}

var (
	_ gofs.FileReader   = (*instanceFileHandle)(nil)
	_ gofs.FileReleaser = (*instanceFileHandle)(nil)
)

// Release closes the descriptor, if this handle holds one.
func (h *instanceFileHandle) Release(ctx context.Context) syscall.Errno {
	if h.file != nil {
		err := h.file.Close()
		h.file = nil
		return errnoFor(err)
	}
	return 0
}

// Read serves a slice of the snapshot taken at open time.
//
// The returned slice must not exceed len(dest): the kernel sized its reply
// buffer to exactly that, and handing back more makes the write of the reply
// fail with EINVAL — which surfaces as a file that reads as empty. Any file
// larger than one read block is affected, so this bound is load-bearing.
func (h *instanceFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off < 0 {
		return fuse.ReadResultData(nil), 0
	}

	if h.file != nil {
		// Zero copy where possible: hand the kernel our descriptor and let it
		// splice straight from the backing file into the reply, skipping the
		// bounce through user space entirely.
		if h.zeroCopy {
			if provider, ok := h.file.(interface{ Fd() uintptr }); ok {
				return fuse.ReadResultFd(provider.Fd(), off, len(dest)), 0
			}
		}
		read, err := h.file.ReadAt(dest, off)
		if err != nil && read == 0 && !errors.Is(err, io.EOF) {
			return nil, errnoFor(err)
		}
		return fuse.ReadResultData(dest[:read]), 0
	}

	// Generated content. The returned slice must not exceed len(dest): the
	// kernel sized its reply buffer to exactly that, and handing back more
	// makes the reply write fail with EINVAL — which surfaces as a file that
	// reads as empty.
	if off >= int64(len(h.data)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(h.data)) {
		end = int64(len(h.data))
	}
	return fuse.ReadResultData(h.data[off:end]), 0
}

// stampAttrTimes copies the backing file's times onto a FUSE attr.
//
// Without this every node reports the zero time and ls shows 1970 — the
// backing mtime was in the Stat result all along, it just never made the
// trip. Nlink is set to 1 for the same reason: zero reads as broken, and the
// view has no hard links to count.
func stampAttrTimes(attr *fuse.Attr, info fs.FileInfo) {
	modified := info.ModTime()
	attr.SetTimes(&modified, &modified, &modified)
	attr.Nlink = 1
}

// stampVirtualTimes marks a node that has no backing file — the control view
// and its children — with the projection time, the honest stand-in for
// generated content.
func stampVirtualTimes(attr *fuse.Attr, view *InstanceView) {
	built := view.BuiltAt
	attr.SetTimes(&built, &built, &built)
	attr.Nlink = 1
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
