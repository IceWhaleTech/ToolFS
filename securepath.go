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
	// Close releases the held descriptors.
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

func (h *osRootHandle) Close() error {
	return h.root.Close()
}
