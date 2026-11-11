package toolfs

// Descriptor-based secure IO. A checked path string is never a security
// handle: all data components are opened without following symbolic links.

import (
	"errors"
	"io/fs"
	"os"
)

// SecurePathBackend provides file operations anchored on a root descriptor.
// Implementations also validate relative paths defensively.
type SecurePathBackend interface {
	// OpenRoot opens a root without following data symlinks, including in its
	// parents. Only fixed OS path aliases (such as Darwin /var) are normalized.
	// Authority-declared aliases must be mapped to a physical root by the caller.
	OpenRoot(localRoot string) (RootHandle, error)
}

// RootHandle holds one mount root. All relative operations use fixed directory
// descriptors. A renamed root remains the same root; paths are not reopened.
// This does not isolate pre-existing hard links or mount points.
type RootHandle interface {
	ReadFile(rel string) ([]byte, error)
	WriteFile(rel string, data []byte, perm os.FileMode) error
	// Stat does not follow the final symlink; parent symlinks are rejected.
	Stat(rel string) (fs.FileInfo, error)
	ReadDir(rel string) ([]fs.DirEntry, error)
	Mkdir(rel string, perm os.FileMode) error
	MkdirAll(rel string, perm os.FileMode) error
	// Remove and Rename act on a final symlink itself, never its target.
	Remove(rel string) error
	Rename(oldRel, newRel string) error
	Create(rel string, perm os.FileMode) error
	OpenRead(rel string) (ReadableFile, error)
	// CloneFile preserves content with reflink when available, otherwise a
	// descriptor-pinned copy. On failure an incomplete destination may remain.
	CloneFile(srcRel, dstRel string, perm os.FileMode) error
	OpenWrite(rel string, perm os.FileMode) (WritableFile, error)
	FreeBytes() (uint64, error)
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
