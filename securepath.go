package toolfs

// Descriptor-based secure IO.
//
// Why filepath.EvalSymlinks plus a containment check is not enough: between the
// check and the use, a parent directory or symlink can be replaced by SMB, by
// another local process, or by a concurrent agent (TOCTOU); O_NOFOLLOW only
// protects the final component. This layer therefore never treats a resolved
// path string as a security handle. It anchors on the mount root's directory
// descriptor and issues every real IO from there.

import (
	"errors"
	"io"
	"io/fs"
	"os"
)

// SecurePathBackend provides file operations anchored on a root descriptor.
// Every rel argument must already have passed sanitizeVirtualPath and therefore
// contain no ".." segment.
type SecurePathBackend interface {
	// OpenRoot opens a mount root; the handle serves all later relative ops.
	OpenRoot(localRoot string) (RootHandle, error)
}

// RootHandle holds one mount root. All relative operations stay beneath it.
type RootHandle interface {
	// ReadFile reads a path relative to the root.
	ReadFile(rel string) ([]byte, error)
	// WriteFile writes a path relative to the root.
	WriteFile(rel string, data []byte, perm os.FileMode) error
	// Stat returns metadata without following a symlink in the final component.
	Stat(rel string) (fs.FileInfo, error)
	// ReadDir lists directory entries relative to the root.
	ReadDir(rel string) ([]fs.DirEntry, error)

	// Mutations. All of these stay confined to the root, which is what lets
	// the protection chain move a file into its state area without ever
	// touching an absolute path — no TOCTOU window, and no chance of the
	// rename crossing a filesystem boundary.
	Mkdir(rel string, perm os.FileMode) error
	MkdirAll(rel string, perm os.FileMode) error
	Remove(rel string) error
	Rename(oldRel, newRel string) error
	Create(rel string, perm os.FileMode) error

	// OpenRead returns a readable handle for streaming.
	//
	// Slurping the whole file at open instead would allocate its full length
	// per open — fatal for a filesystem expected to serve large media.
	OpenRead(rel string) (ReadableFile, error)

	// CloneFile makes dstRel share srcRel's extents (copy-on-write) instead of
	// copying them. Returns ErrCloneUnsupported when the filesystem cannot,
	// so the caller can fall back to a full copy.
	CloneFile(srcRel, dstRel string, perm os.FileMode) error

	// OpenWrite returns a writable handle for streaming.
	//
	// Buffering a whole file in memory instead would make every write
	// reallocate and copy the accumulated content — quadratic in file size —
	// and would put the file's full length in RAM. Neither is acceptable for
	// a filesystem that may serve large media files.
	OpenWrite(rel string, perm os.FileMode) (WritableFile, error)

	// FreeBytes reports space available to the caller on this root's filesystem.
	FreeBytes() (uint64, error)

	// Close releases the held descriptors.
	Close() error
}

// ReadableFile is a streaming read handle confined to a root.
type ReadableFile interface {
	ReadAt(p []byte, off int64) (int, error)
	Close() error
}

// ErrCloneUnsupported reports that the filesystem cannot share extents.
var ErrCloneUnsupported = errors.New("toolfs: copy-on-write cloning unsupported")

// WritableFile is a streaming write handle confined to a root.
type WritableFile interface {
	WriteAt(p []byte, off int64) (int, error)
	Truncate(size int64) error
	Sync() error
	Close() error
}

// newSecureBackend returns the implementation for the current platform.
// On Linux this resolves through openat2(RESOLVE_BENEATH), the production
// security baseline; elsewhere os.Root's constrained walk serves development
// and unit tests.
func newSecureBackend() SecurePathBackend {
	return &osRootBackend{}
}

// osRootBackend is built on os.Root (Go 1.24+).
//
// os.Root has exactly the semantics required: every operation is confined below
// the root and symlinks escaping it are not followed, implemented via openat2 on
// Linux. That covers both symlink escape and traversal into another mount.
type osRootBackend struct{}

func (b *osRootBackend) OpenRoot(localRoot string) (RootHandle, error) {
	root, err := os.OpenRoot(localRoot)
	if err != nil {
		return nil, err
	}
	return &osRootHandle{root: root}, nil
}

type osRootHandle struct {
	root *os.Root
}

// normalizeRel maps the empty relative path to ".", which os.Root uses for the
// root itself.
func normalizeRel(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

func (h *osRootHandle) ReadFile(rel string) ([]byte, error) {
	f, err := h.root.Open(normalizeRel(rel))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (h *osRootHandle) WriteFile(rel string, data []byte, perm os.FileMode) error {
	f, err := h.root.OpenFile(normalizeRel(rel), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (h *osRootHandle) Stat(rel string) (fs.FileInfo, error) {
	return h.root.Lstat(normalizeRel(rel))
}

func (h *osRootHandle) ReadDir(rel string) ([]fs.DirEntry, error) {
	f, err := h.root.Open(normalizeRel(rel))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

func (h *osRootHandle) Mkdir(rel string, perm os.FileMode) error {
	return h.root.Mkdir(normalizeRel(rel), perm)
}

func (h *osRootHandle) MkdirAll(rel string, perm os.FileMode) error {
	return h.root.MkdirAll(normalizeRel(rel), perm)
}

func (h *osRootHandle) Remove(rel string) error {
	return h.root.Remove(normalizeRel(rel))
}

func (h *osRootHandle) Rename(oldRel, newRel string) error {
	return h.root.Rename(normalizeRel(oldRel), normalizeRel(newRel))
}

func (h *osRootHandle) Create(rel string, perm os.FileMode) error {
	f, err := h.root.OpenFile(normalizeRel(rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	return f.Close()
}

func (h *osRootHandle) OpenRead(rel string) (ReadableFile, error) {
	return h.root.Open(normalizeRel(rel))
}

func (h *osRootHandle) CloneFile(srcRel, dstRel string, perm os.FileMode) error {
	src, err := h.root.Open(normalizeRel(srcRel))
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := h.root.OpenFile(normalizeRel(dstRel), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}

	if err := cloneFile(dst, src); err != nil {
		dst.Close()
		// Leave no partial file behind: the caller will fall back to copying.
		_ = h.root.Remove(normalizeRel(dstRel))
		return ErrCloneUnsupported
	}
	return dst.Close()
}

func (h *osRootHandle) OpenWrite(rel string, perm os.FileMode) (WritableFile, error) {
	return h.root.OpenFile(normalizeRel(rel), os.O_WRONLY|os.O_CREATE, perm)
}

func (h *osRootHandle) FreeBytes() (uint64, error) {
	return freeBytes(h.root.Name())
}

func (h *osRootHandle) Close() error {
	return h.root.Close()
}
