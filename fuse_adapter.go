//go:build linux || darwin
// +build linux darwin

package toolfs

import (
	"context"
	"strings"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// ToolFSRoot is the root node for the ToolFS FUSE filesystem
type ToolFSRoot struct {
	fs.Inode
	toolfs *ToolFS
}

// Ensure ToolFSRoot implements the NodeOnAdder interface
var _ fs.NodeOnAdder = (*ToolFSRoot)(nil)

// NewToolFSRoot creates a new root node for FUSE filesystem
func NewToolFSRoot(toolfs *ToolFS) *ToolFSRoot {
	return &ToolFSRoot{
		toolfs: toolfs,
	}
}

// OnAdd is called when the filesystem is mounted
func (r *ToolFSRoot) OnAdd(ctx context.Context) {
	// Add memory directory
	memNode := &ToolFSDir{
		toolfs: r.toolfs,
		path:   r.toolfs.rootPath + "/memory",
	}
	memInode := r.NewPersistentInode(ctx, memNode, fs.StableAttr{
		Mode: syscall.S_IFDIR | 0o755,
	})
	r.AddChild("memory", memInode, false)

	// Add RAG directory
	ragNode := &ToolFSDir{
		toolfs: r.toolfs,
		path:   r.toolfs.rootPath + "/rag",
	}
	ragInode := r.NewPersistentInode(ctx, ragNode, fs.StableAttr{
		Mode: syscall.S_IFDIR | 0o755,
	})
	r.AddChild("rag", ragInode, false)

	// Add mounted directories
	for mountPoint := range r.toolfs.mounts {
		relPath := r.toolfs.normalizeMountPoint(mountPoint)
		if relPath != "" {
			mountNode := &ToolFSDir{
				toolfs: r.toolfs,
				path:   mountPoint,
			}
			mountInode := r.NewPersistentInode(ctx, mountNode, fs.StableAttr{
				Mode: syscall.S_IFDIR | 0o755,
			})
			r.AddChild(relPath, mountInode, false)
		}
	}

	// Add skill mounts
	for mountPoint := range r.toolfs.skillMounts {
		relPath := r.toolfs.normalizeMountPoint(mountPoint)
		if relPath != "" {
			skillNode := &ToolFSDir{
				toolfs: r.toolfs,
				path:   mountPoint,
			}
			skillInode := r.NewPersistentInode(ctx, skillNode, fs.StableAttr{
				Mode: syscall.S_IFDIR | 0o755,
			})
			r.AddChild(relPath, skillInode, false)
		}
	}
}

// normalizeMountPoint extracts the relative path from a full mount point
func (fs *ToolFS) normalizeMountPoint(mountPoint string) string {
	mountPoint = normalizeVirtualPath(mountPoint)
	root := normalizeVirtualPath(fs.rootPath)

	if mountPoint == root {
		return ""
	}

	if strings.HasPrefix(mountPoint, root+"/") {
		rel := strings.TrimPrefix(mountPoint, root+"/")
		if idx := strings.Index(rel, "/"); idx != -1 {
			return rel[:idx]
		}
		return rel
	}

	// Extract last component
	parts := strings.Split(strings.TrimPrefix(mountPoint, root), "/")
	if len(parts) > 0 && parts[0] != "" {
		return parts[0]
	}

	return ""
}

// ToolFSDir represents a directory in the ToolFS FUSE filesystem
type ToolFSDir struct {
	fs.Inode
	toolfs *ToolFS
	path   string
}

// Ensure ToolFSDir implements the required interfaces
var (
	_ fs.NodeReaddirer = (*ToolFSDir)(nil)
	_ fs.NodeLookuper  = (*ToolFSDir)(nil)
)

// Readdir implements NodeReaddirer interface
func (d *ToolFSDir) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, err := d.toolfs.ListDir(d.path)
	if err != nil {
		return nil, syscall.EIO
	}

	var dirEntries []fuse.DirEntry
	for _, name := range entries {
		// Determine if it's a directory
		isDir := strings.HasSuffix(name, "/")
		if isDir {
			name = strings.TrimSuffix(name, "/")
		}

		mode := uint32(syscall.S_IFREG | 0o644)
		if isDir {
			mode = syscall.S_IFDIR | 0o755
		}

		dirEntries = append(dirEntries, fuse.DirEntry{
			Name: name,
			Mode: mode,
			Ino:  0, // Inode number (0 for virtual filesystem)
		})
	}

	return fs.NewListDirStream(dirEntries), 0
}

// Lookup implements NodeLookuper interface
func (d *ToolFSDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	childPath := d.path + "/" + name

	// Check if it's a file or directory
	info, err := d.toolfs.Stat(childPath)
	if err != nil {
		return nil, syscall.ENOENT
	}

	if info.IsDir {
		childNode := &ToolFSDir{
			toolfs: d.toolfs,
			path:   childPath,
		}
		childInode := d.NewPersistentInode(ctx, childNode, fs.StableAttr{
			Mode: syscall.S_IFDIR | 0o755,
		})
		return childInode, 0
	} else {
		childNode := &ToolFSFile{
			toolfs: d.toolfs,
			path:   childPath,
		}
		childInode := d.NewPersistentInode(ctx, childNode, fs.StableAttr{
			Mode: syscall.S_IFREG | 0o644,
		})
		return childInode, 0
	}
}

// ToolFSFile represents a file in the ToolFS FUSE filesystem
type ToolFSFile struct {
	fs.Inode
	toolfs *ToolFS
	path   string
}

// Ensure ToolFSFile implements the required interfaces
var (
	_ fs.NodeOpener    = (*ToolFSFile)(nil)
	_ fs.NodeGetattrer = (*ToolFSFile)(nil)
)

// Open implements NodeOpener interface
func (f *ToolFSFile) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return &ToolFSFileHandle{
		toolfs: f.toolfs,
		path:   f.path,
	}, fuse.FOPEN_KEEP_CACHE, 0
}

// Getattr implements NodeGetattrer interface
func (f *ToolFSFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	info, err := f.toolfs.Stat(f.path)
	if err != nil {
		return syscall.ENOENT
	}

	out.Size = uint64(info.Size)
	out.Mtime = uint64(info.ModTime.Unix())
	out.Mode = syscall.S_IFREG | 0o644

	return 0
}

// ToolFSFileHandle is a file handle for ToolFS files
type ToolFSFileHandle struct {
	toolfs *ToolFS
	path   string
}

// Ensure ToolFSFileHandle implements the required interfaces
var (
	_ fs.FileReader = (*ToolFSFileHandle)(nil)
	_ fs.FileWriter = (*ToolFSFileHandle)(nil)
)

// Read implements FileReader interface
func (fh *ToolFSFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	data, err := fh.toolfs.ReadFile(fh.path)
	if err != nil {
		return nil, syscall.EIO
	}

	if off >= int64(len(data)) {
		return fuse.ReadResultData(nil), 0
	}

	end := int(off) + len(dest)
	if end > len(data) {
		end = len(data)
	}

	// Copy data to a new slice to ensure it's not invalidated
	resultLen := end - int(off)
	if resultLen <= 0 {
		return fuse.ReadResultData(nil), 0
	}

	result := make([]byte, resultLen)
	copy(result, data[off:end])

	return fuse.ReadResultData(result), 0
}

// Write implements FileWriter interface
func (fh *ToolFSFileHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	// Read existing file
	existing, err := fh.toolfs.ReadFile(fh.path)
	if err != nil {
		existing = []byte{}
	}

	// Resize if needed
	newSize := int(off) + len(data)
	if newSize > len(existing) {
		temp := make([]byte, newSize)
		copy(temp, existing)
		existing = temp
	}

	// Write new data
	copy(existing[off:], data)

	// Write back
	if err := fh.toolfs.WriteFile(fh.path, existing); err != nil {
		return 0, syscall.EIO
	}

	return uint32(len(data)), 0
}

// MountToolFS mounts a ToolFS instance as a FUSE filesystem at the specified mount point
// This allows accessing ToolFS through standard filesystem operations like cat, ls, etc.
//
// Example:
//
//	fs := NewToolFS("/toolfs")
//	err := MountToolFS(fs, "/mnt/toolfs")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	// Now you can: cat /mnt/toolfs/memory/entry1
func MountToolFS(toolfs *ToolFS, mountPoint string, options *fuse.MountOptions) error {
	opts := &fs.Options{}
	if options != nil {
		opts.MountOptions = *options
	} else {
		opts.MountOptions = fuse.MountOptions{
			Options: []string{"default_permissions"},
		}
	}

	root := NewToolFSRoot(toolfs)
	server, err := fs.Mount(mountPoint, root, opts)
	if err != nil {
		return err
	}

	// Note: fs.Mount() automatically starts the serving loop in the background.
	// We should NOT call server.Serve() again, as it will panic with "Serve() must only be called once".
	// The server is already serving requests, so we can just return.
	// We don't call WaitMount() here to avoid blocking.
	// WaitMount() waits for the first request, which may never come if the mount point
	// isn't accessed immediately. The mount should still work without it.
	// If you need to ensure the mount is ready, access the mount point after calling MountToolFS.
	_ = server // Keep reference to server

	return nil
}

// MountToolFSWithSession mounts ToolFS with a specific session for access control
func MountToolFSWithSession(toolfs *ToolFS, mountPoint string, session *Session, options *fuse.MountOptions) error {
	// Note: Session-based access control would need to be integrated into the FUSE handlers
	// For now, this is a placeholder for future implementation
	return MountToolFS(toolfs, mountPoint, options)
}
