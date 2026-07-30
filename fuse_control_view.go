//go:build linux || darwin
// +build linux darwin

package toolfs

// The .toolfs/ read-only control view.
//
// It exists so an agent can answer "what did I just change" and "what can this
// file go back to" with a plain read, instead of needing an out-of-band API.
// That matters in practice: the agent already has a filesystem in front of it,
// and shelling out to `cat` is the path of least resistance.
//
// Three properties are load-bearing:
//
//   - Read-only. Writes return EROFS. This is a window onto the protection
//     area, not a way into it.
//   - Filtered by *current* grants, evaluated at read time. A revoked path
//     stops appearing immediately, so the view cannot become a side channel
//     for content the user may no longer see.
//   - Generated per read and served with direct IO. Caching would let a stale
//     listing survive a revocation.
//
// The name is reserved globally: real data called ".toolfs" must not shadow it,
// and Lookup on the grants never reaches it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"syscall"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// Control view file names.
const (
	controlReadme        = "README"
	controlChanges       = "changes.jsonl"
	controlTrash         = "trash.jsonl"
	controlRestorePoints = "restore-points.jsonl"
)

var controlFiles = []string{controlReadme, controlChanges, controlTrash, controlRestorePoints}

const controlReadmeText = `ToolFS control view (read-only)

  changes.jsonl        every protected change this instance made, oldest first
  trash.jsonl          deleted entries that can still be restored
  restore-points.jsonl states each path can be restored to, newest first

Entries are filtered by the grants held right now, so a path you can no longer
access will not appear here either.

This directory is read-only. To restore something, ask ToolFS to do it: undo a
whole task by its task id, or flash a single path back to one of the restore
point ids listed above. Copying content out of the protection area by hand is
not possible from here.
`

// controlDir is the .toolfs directory at the mount root.
type controlDir struct {
	gofs.Inode
	view *InstanceView
}

var (
	_ gofs.NodeReaddirer = (*controlDir)(nil)
	_ gofs.NodeLookuper  = (*controlDir)(nil)
	_ gofs.NodeGetattrer = (*controlDir)(nil)
)

func (c *controlDir) Readdir(ctx context.Context) (gofs.DirStream, syscall.Errno) {
	entries := make([]fuse.DirEntry, 0, len(controlFiles))
	for _, name := range controlFiles {
		entries = append(entries, fuse.DirEntry{Name: name, Mode: syscall.S_IFREG})
	}
	return gofs.NewListDirStream(entries), 0
}

func (c *controlDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	known := false
	for _, candidate := range controlFiles {
		if candidate == name {
			known = true
			break
		}
	}
	if !known {
		return nil, syscall.ENOENT
	}

	child := &controlFile{view: c.view, name: name}
	out.SetEntryTimeout(0)
	out.SetAttrTimeout(0)
	out.Mode = syscall.S_IFREG | 0o444
	return c.NewInode(ctx, child, gofs.StableAttr{Mode: syscall.S_IFREG}), 0
}

func (c *controlDir) Getattr(ctx context.Context, fh gofs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | 0o555
	out.SetTimeout(0)
	return 0
}

// controlFile is one generated file inside .toolfs.
type controlFile struct {
	gofs.Inode
	view *InstanceView
	name string
}

var (
	_ gofs.NodeOpener    = (*controlFile)(nil)
	_ gofs.NodeGetattrer = (*controlFile)(nil)
)

func (f *controlFile) Getattr(ctx context.Context, fh gofs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	// Size is reported as the current rendering. It is recomputed rather than
	// remembered, because the underlying data changes as the agent works.
	content, errno := f.render()
	if errno != 0 {
		return errno
	}
	out.Mode = syscall.S_IFREG | 0o444
	out.Size = uint64(len(content))
	out.SetTimeout(0)
	return 0
}

// Open refuses any write intent. The control view is a window, not a door.
func (f *controlFile) Open(ctx context.Context, flags uint32) (gofs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_APPEND|syscall.O_TRUNC|syscall.O_CREAT) != 0 {
		return nil, 0, syscall.EROFS
	}
	content, errno := f.render()
	if errno != 0 {
		return nil, 0, errno
	}
	// Direct IO: the content is derived from live authorization, so a cached
	// copy could outlive the grants that justified it.
	return &instanceFileHandle{data: content}, fuse.FOPEN_DIRECT_IO, 0
}

// render produces the file's current content.
func (f *controlFile) render() ([]byte, syscall.Errno) {
	switch f.name {
	case controlReadme:
		return []byte(controlReadmeText), 0
	case controlChanges:
		return f.renderChanges()
	case controlTrash:
		return f.renderTrash()
	case controlRestorePoints:
		return f.renderRestorePoints()
	default:
		return nil, syscall.ENOENT
	}
}

// renderChanges lists journal entries the caller may still see.
func (f *controlFile) renderChanges() ([]byte, syscall.Errno) {
	var buf bytes.Buffer
	for _, name := range f.view.Entries() {
		entry, ok := f.view.Entry(name)
		if !ok || entry.protection == nil {
			continue
		}
		changes, err := entry.protection.ListChanges(ChangeFilter{})
		if err != nil {
			return nil, errnoFor(err)
		}
		for _, change := range changes {
			// Re-check against current grants: a change recorded while the path
			// was accessible must disappear once it is not.
			if !f.view.Allows(entry, change.Path, OpRead) {
				continue
			}
			writeJSONLine(&buf, map[string]any{
				"seq":        change.Seq,
				"at":         change.At,
				"op":         change.Op,
				"path":       name + "/" + change.Path,
				"from_path":  optionalPath(name, change.FromPath),
				"session_id": change.SessionID,
				"task_id":    change.TaskID,
				"undoable":   change.Recovery != "" || change.Op == MutationCreate || change.Op == MutationRename,
			})
		}
	}
	return buf.Bytes(), 0
}

// renderTrash lists recoverable deletions.
func (f *controlFile) renderTrash() ([]byte, syscall.Errno) {
	var buf bytes.Buffer
	for _, name := range f.view.Entries() {
		entry, ok := f.view.Entry(name)
		if !ok || entry.protection == nil {
			continue
		}
		items, err := entry.protection.ListTrash()
		if err != nil {
			return nil, errnoFor(err)
		}
		for _, item := range items {
			if !f.view.Allows(entry, item.OriginalPath, OpRead) {
				continue
			}
			writeJSONLine(&buf, map[string]any{
				"id":         item.ID,
				"path":       name + "/" + item.OriginalPath,
				"is_dir":     item.IsDir,
				"size":       item.Size,
				"deleted_at": item.DeletedAt,
			})
		}
	}
	return buf.Bytes(), 0
}

// renderRestorePoints lists what each changed path can go back to.
//
// It enumerates only paths this instance actually touched, not the whole grant.
// Walking the grant would turn the control view into a directory listing of
// content the caller might not otherwise enumerate.
func (f *controlFile) renderRestorePoints() ([]byte, syscall.Errno) {
	var buf bytes.Buffer
	for _, name := range f.view.Entries() {
		entry, ok := f.view.Entry(name)
		if !ok || entry.protection == nil {
			continue
		}

		changes, err := entry.protection.ListChanges(ChangeFilter{})
		if err != nil {
			return nil, errnoFor(err)
		}
		seen := make(map[string]bool, len(changes))
		for _, change := range changes {
			if seen[change.Path] || !f.view.Allows(entry, change.Path, OpRead) {
				continue
			}
			seen[change.Path] = true

			points, err := entry.protection.ListRestorePoints(change.Path)
			if err != nil {
				return nil, errnoFor(err)
			}
			for _, point := range points {
				writeJSONLine(&buf, map[string]any{
					"id":   point.ID,
					"kind": point.Kind,
					"path": name + "/" + point.Path,
					"size": point.Size,
					"at":   point.At,
				})
			}
		}
	}
	return buf.Bytes(), 0
}

// optionalPath prefixes a grant-relative path when it is set.
func optionalPath(entryName, rel string) any {
	if rel == "" {
		return nil
	}
	return entryName + "/" + rel
}

// writeJSONLine appends one JSON object followed by a newline.
func writeJSONLine(buf *bytes.Buffer, payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		// A record that cannot be rendered is reported inline rather than
		// dropped, so the listing never silently omits something.
		fmt.Fprintf(buf, "{\"error\":%q}\n", err.Error())
		return
	}
	buf.Write(data)
	buf.WriteByte('\n')
}

// The control view refuses every mutation explicitly.
//
// Not implementing these handlers is not enough: go-fuse's defaults do not
// uniformly reject, and an unimplemented Unlink was observed to succeed. Saying
// EROFS out loud is both correct and self-documenting.
var (
	_ gofs.NodeUnlinker  = (*controlDir)(nil)
	_ gofs.NodeRmdirer   = (*controlDir)(nil)
	_ gofs.NodeCreater   = (*controlDir)(nil)
	_ gofs.NodeMkdirer   = (*controlDir)(nil)
	_ gofs.NodeRenamer   = (*controlDir)(nil)
	_ gofs.NodeSetattrer = (*controlFile)(nil)
)

func (c *controlDir) Unlink(ctx context.Context, name string) syscall.Errno { return syscall.EROFS }

func (c *controlDir) Rmdir(ctx context.Context, name string) syscall.Errno { return syscall.EROFS }

func (c *controlDir) Create(ctx context.Context, name string, flags, mode uint32, out *fuse.EntryOut) (
	*gofs.Inode, gofs.FileHandle, uint32, syscall.Errno) {
	return nil, nil, 0, syscall.EROFS
}

func (c *controlDir) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (
	*gofs.Inode, syscall.Errno) {
	return nil, syscall.EROFS
}

func (c *controlDir) Rename(ctx context.Context, name string, newParent gofs.InodeEmbedder,
	newName string, flags uint32) syscall.Errno {
	return syscall.EROFS
}

// Setattr rejects truncation, which is how an overwrite begins.
func (f *controlFile) Setattr(ctx context.Context, fh gofs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	return syscall.EROFS
}
