//go:build linux || darwin

package toolfs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

func newSecureBackend() SecurePathBackend { return &noFollowBackend{} }

type noFollowBackend struct{}

func (b *noFollowBackend) OpenRoot(name string) (RootHandle, error) {
	if strings.ContainsRune(name, 0) {
		return nil, securePathError("openroot", name, fs.ErrInvalid)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return nil, securePathError("openroot", name, fs.ErrInvalid)
		}
	}
	absolute, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	absolute = secureSystemPath(absolute)
	root, err := openSecureDir("/")
	if err != nil {
		return nil, securePathError("openroot", name, err)
	}
	dir, err := walkSecureDirs(root, strings.Split(strings.TrimPrefix(absolute, "/"), "/"), false, 0)
	if err != nil {
		return nil, securePathError("openroot", name, err)
	}
	return &secureRootHandle{dir: dir}, nil
}

// The lock covers descriptor duplication and close, not whole operations. Each
// operation owns its root descriptor, so a concurrent Close cannot recycle an
// fd out from under an openat and redirect it into an unrelated directory.
type secureRootHandle struct {
	mu  sync.Mutex
	dir *os.File
}

func securePathError(op, path string, err error) error {
	return &os.PathError{Op: op, Path: path, Err: err}
}

func openSecureDir(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openSecureAt(parent *os.File, name string, flags int, perm os.FileMode) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, secureMode(perm))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func secureMode(perm os.FileMode) uint32 {
	mode := uint32(perm.Perm())
	if perm&os.ModeSetuid != 0 {
		mode |= unix.S_ISUID
	}
	if perm&os.ModeSetgid != 0 {
		mode |= unix.S_ISGID
	}
	if perm&os.ModeSticky != 0 {
		mode |= unix.S_ISVTX
	}
	return mode
}

func (h *secureRootHandle) acquire() (*os.File, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dir == nil {
		return nil, os.ErrClosed
	}
	// Opening "." gives an independent directory offset for concurrent ReadDir.
	return openSecureAt(h.dir, ".", unix.O_RDONLY|unix.O_DIRECTORY, 0)
}

// Validate before cleaning: cleaning "link/../file" would hide an attempted
// traversal. Empty components and "." are harmless, but ".." is never valid.
func secureParts(rel string) ([]string, error) {
	if strings.HasPrefix(rel, "/") || strings.ContainsRune(rel, 0) {
		return nil, fs.ErrInvalid
	}
	var parts []string
	for _, p := range strings.Split(rel, "/") {
		if p == ".." {
			return nil, fs.ErrInvalid
		}
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	return parts, nil
}

// walkSecureDirs consumes start on both success and error. Even if an attacker
// swaps a checked directory name for a link, the next operation uses the fd
// already acquired, never a pathname assembled from the original root.
func walkSecureDirs(start *os.File, parts []string, create bool, perm os.FileMode) (*os.File, error) {
	current := start
	for _, part := range parts {
		if part == "" {
			continue
		}
		next, err := openSecureAt(current, part, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if create && errors.Is(err, unix.ENOENT) {
			err = unix.Mkdirat(int(current.Fd()), part, secureMode(perm))
			if err == nil || errors.Is(err, unix.EEXIST) {
				next, err = openSecureAt(current, part, unix.O_RDONLY|unix.O_DIRECTORY, 0)
			}
		}
		_ = current.Close()
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}

func (h *secureRootHandle) parent(rel string) (*os.File, string, error) {
	parts, err := secureParts(rel)
	if err != nil {
		return nil, "", err
	}
	root, err := h.acquire()
	if err != nil {
		return nil, "", err
	}
	if len(parts) == 0 {
		return root, ".", nil
	}
	parent, err := walkSecureDirs(root, parts[:len(parts)-1], false, 0)
	return parent, parts[len(parts)-1], err
}

// Only regular files are data streams. O_NONBLOCK prevents a hostile FIFO from
// hanging an open before we can reject it. Truncation happens after fstat, on
// the opened descriptor, never as a side effect of resolving an unsafe path.
func (h *secureRootHandle) openData(rel string, flags int, perm os.FileMode) (*os.File, error) {
	parent, name, err := h.parent(rel)
	if err != nil {
		return nil, securePathError("open", rel, err)
	}
	defer parent.Close()
	f, err := openSecureAt(parent, name, flags|unix.O_NONBLOCK, perm)
	if err != nil {
		return nil, securePathError("open", rel, err)
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = unix.EINVAL
	}
	if err != nil {
		_ = f.Close()
		return nil, securePathError("open", rel, err)
	}
	return f, nil
}

func (h *secureRootHandle) OpenRead(rel string) (ReadableFile, error) {
	return h.openData(rel, unix.O_RDONLY, 0)
}

func (h *secureRootHandle) OpenWrite(rel string, perm os.FileMode) (WritableFile, error) {
	return h.openData(rel, unix.O_WRONLY|unix.O_CREAT, perm)
}

func (h *secureRootHandle) ReadFile(rel string) ([]byte, error) {
	f, err := h.openData(rel, unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (h *secureRootHandle) WriteFile(rel string, data []byte, perm os.FileMode) error {
	f, err := h.openData(rel, unix.O_WRONLY|unix.O_CREAT, perm)
	if err != nil {
		return err
	}
	if err = f.Truncate(0); err == nil {
		_, err = f.Write(data)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (h *secureRootHandle) Create(rel string, perm os.FileMode) error {
	f, err := h.openData(rel, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, perm)
	if err != nil {
		return err
	}
	return f.Close()
}

func secureStatAt(parent *os.File, name string) (fs.FileInfo, error) {
	f, err := openSecureMetadata(parent, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}

func (h *secureRootHandle) Stat(rel string) (fs.FileInfo, error) {
	parent, name, err := h.parent(rel)
	if err != nil {
		return nil, securePathError("stat", rel, err)
	}
	defer parent.Close()
	info, err := secureStatAt(parent, name)
	if err != nil {
		return nil, securePathError("stat", rel, err)
	}
	return info, nil
}

func (h *secureRootHandle) ReadDir(rel string) ([]fs.DirEntry, error) {
	parts, err := secureParts(rel)
	if err != nil {
		return nil, securePathError("readdir", rel, err)
	}
	root, err := h.acquire()
	if err != nil {
		return nil, securePathError("readdir", rel, err)
	}
	dir, err := walkSecureDirs(root, parts, false, 0)
	if err != nil {
		return nil, securePathError("readdir", rel, err)
	}
	defer dir.Close()
	// os.File.ReadDir returns entries whose Info may later Lstat an absolute
	// path, after the directory was closed/replaced. Snapshot metadata using the
	// directory fd instead. Readdirnames avoids its unknown-d_type fallback too.
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	entries := make([]fs.DirEntry, 0, len(names))
	for _, name := range names {
		info, err := secureStatAt(dir, name)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return nil, securePathError("readdir", rel, err)
		}
		entries = append(entries, fs.FileInfoToDirEntry(info))
	}
	return entries, nil
}

func (h *secureRootHandle) Mkdir(rel string, perm os.FileMode) error {
	parent, name, err := h.parent(rel)
	if err != nil {
		return securePathError("mkdir", rel, err)
	}
	defer parent.Close()
	return secureWrap("mkdir", rel, unix.Mkdirat(int(parent.Fd()), name, secureMode(perm)))
}

func (h *secureRootHandle) MkdirAll(rel string, perm os.FileMode) error {
	parts, err := secureParts(rel)
	if err != nil {
		return securePathError("mkdirall", rel, err)
	}
	root, err := h.acquire()
	if err != nil {
		return securePathError("mkdirall", rel, err)
	}
	dir, err := walkSecureDirs(root, parts, true, perm)
	if err != nil {
		return securePathError("mkdirall", rel, err)
	}
	return dir.Close()
}

func secureWrap(op, path string, err error) error {
	if err == nil {
		return nil
	}
	return securePathError(op, path, err)
}

func (h *secureRootHandle) Remove(rel string) error {
	parent, name, err := h.parent(rel)
	if err != nil {
		return securePathError("remove", rel, err)
	}
	defer parent.Close()
	// Neither unlinkat form follows the final component. No Lstat/type check
	// followed by a path-based operation, including when a link is substituted.
	err = unix.Unlinkat(int(parent.Fd()), name, 0)
	if errors.Is(err, unix.EISDIR) || errors.Is(err, unix.EPERM) {
		err = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
	}
	return secureWrap("remove", rel, err)
}

func (h *secureRootHandle) Rename(oldRel, newRel string) error {
	oldParent, oldName, err := h.parent(oldRel)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldRel, New: newRel, Err: err}
	}
	defer oldParent.Close()
	newParent, newName, err := h.parent(newRel)
	if err == nil {
		defer newParent.Close()
		err = unix.Renameat(int(oldParent.Fd()), oldName, int(newParent.Fd()), newName)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldRel, New: newRel, Err: err}
	}
	return nil
}

func (h *secureRootHandle) CloneFile(srcRel, dstRel string, perm os.FileMode) error {
	src, err := h.openData(srcRel, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer src.Close()
	parent, name, err := h.parent(dstRel)
	if err != nil {
		return securePathError("clone", dstRel, err)
	}
	defer parent.Close()
	dst, err := openSecureAt(parent, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, perm)
	if err != nil {
		return securePathError("clone", dstRel, err)
	}
	// Keep both descriptors pinned through fallback. Never unlink by name on
	// failure: a concurrent replacement at that name is not ours to remove.
	if err := cloneOrCopy(dst, src, cloneFile); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

func cloneOrCopy(dst, src *os.File, clone func(*os.File, *os.File) error) error {
	err := clone(dst, src)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrCloneUnsupported) && !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENOTTY) && !errors.Is(err, unix.EXDEV) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) {
		return err
	}
	if err := dst.Truncate(0); err != nil {
		return err
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	return err
}

func (h *secureRootHandle) cloneAvailable() bool {
	root, err := h.acquire()
	if err != nil {
		return false
	}
	defer root.Close()
	return probeSecureClone(root)
}

func (h *secureRootHandle) FreeBytes() (uint64, error) {
	dir, err := h.acquire()
	if err != nil {
		return 0, err
	}
	defer dir.Close()
	var stat unix.Statfs_t
	if err := unix.Fstatfs(int(dir.Fd()), &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}

func (h *secureRootHandle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dir == nil {
		return os.ErrClosed
	}
	err := h.dir.Close()
	h.dir = nil
	return err
}
