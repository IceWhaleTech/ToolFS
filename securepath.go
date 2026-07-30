package toolfs

// descriptor-based 安全 IO（设计稿 §4.2）。
//
// 为什么不能只用 filepath.EvalSymlinks + containment 校验：
// 校验与使用之间存在窗口，父目录或软链可被 SMB、其他本机进程或并发 agent 替换（TOCTOU）；
// O_NOFOLLOW 也只保护最终一个分量。因此本层不把「解析后的路径字符串」当作安全句柄，
// 而是以挂载点根目录的 dirfd 为锚，所有实际 IO 都从该 fd 出发。

import (
	"io"
	"io/fs"
	"os"
)

// SecurePathBackend 提供以 root dirfd 为锚的文件操作。
// 所有方法的 rel 必须是【已经过 sanitizeVirtualPath】的相对路径，不含 ".." 段。
type SecurePathBackend interface {
	// OpenRoot 打开挂载点根目录，返回的句柄用于后续所有相对操作。
	OpenRoot(localRoot string) (RootHandle, error)
}

// RootHandle 是一个挂载点根目录的持有者。所有相对操作都被约束在其之下。
type RootHandle interface {
	// ReadFile 读取 root 之下的相对路径。
	ReadFile(rel string) ([]byte, error)
	// WriteFile 写入 root 之下的相对路径。
	WriteFile(rel string, data []byte, perm os.FileMode) error
	// Stat 取 root 之下相对路径的元信息，不跟随最终分量的符号链接。
	Stat(rel string) (fs.FileInfo, error)
	// ReadDir 列出 root 之下相对路径的目录项。
	ReadDir(rel string) ([]fs.DirEntry, error)
	// Close 释放持有的 fd。
	Close() error
}

// newSecureBackend 返回当前平台的实现。
// Linux 使用 openat2(RESOLVE_BENEATH)，是生产安全基线；
// 其他平台使用 os.Root 的逐段受限解析，用于开发与单测。
func newSecureBackend() SecurePathBackend {
	return &osRootBackend{}
}

// osRootBackend 基于 Go 1.24+ 的 os.Root。
//
// os.Root 的语义正是我们需要的：所有操作被限制在根目录之下，
// 不会跟随逃逸出根的符号链接，且由 runtime 在 Linux 上使用 openat2 实现。
// 这同时覆盖 S2（软链逃逸）与 S3（经 ".." 落到别的挂载点）。
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

// normalizeRel 把空相对路径映射为 "."，os.Root 用 "." 表示根本身。
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
