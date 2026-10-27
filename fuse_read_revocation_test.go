//go:build linux || darwin

package toolfs

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Exercise the real Open/Read handle without a kernel mount as well, so this
// security boundary is tested even where requireFUSE skips integration tests.
func TestOpenFileHandleRechecksReadAfterRevocation(t *testing.T) {
	for _, zeroCopy := range []bool{false, true} {
		name := "copy"
		if zeroCopy {
			name = "zero-copy"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "marker"), []byte("allowed"), 0600); err != nil {
				t.Fatal(err)
			}
			identity := &ZimaIdentity{Username: "alice", UserID: 7}
			access := NewAccessSnapshot("alice", 7, "user", []Grant{{ID: 1, Path: root, ReadOnly: true}}, 1)
			view, err := BuildInstanceView(identity, access, AgentCapabilities{ViewAliases: []PathAlias{{Host: root, View: "/Docs"}}})
			if err != nil {
				t.Fatal(err)
			}
			view.ZeroCopyRead = zeroCopy
			if err := view.openRoots(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(view.closeRoots)
			entry, _, err := view.Resolve("Docs")
			if err != nil || entry == nil {
				t.Fatalf("resolve fixture: %v", err)
			}
			node := &instanceFile{view: view, entry: entry, rel: "marker"}
			opened, _, errno := node.Open(context.Background(), syscall.O_RDONLY)
			if errno != 0 {
				t.Fatalf("open granted file: %v", errno)
			}
			handle := opened.(*instanceFileHandle)
			t.Cleanup(func() { handle.Release(context.Background()) })
			dest := make([]byte, 64)
			result, errno := handle.Read(context.Background(), dest, 0)
			if errno != 0 {
				t.Fatalf("positive read: %v", errno)
			}
			data, status := result.Bytes(dest)
			if status != 0 || string(data) != "allowed" {
				t.Fatalf("positive content: %q, %v", data, status)
			}
			result.Done()
			view.mu.Lock()
			view.Access = NewAccessSnapshot("alice", 7, "user", nil, 2)
			view.mu.Unlock()
			result, errno = handle.Read(context.Background(), dest, 0)
			if errno != syscall.ENOENT {
				if result != nil {
					result.Done()
				}
				t.Fatalf("opened descriptor survived read revocation: %v", errno)
			}
			if result != nil {
				result.Done()
				t.Fatal("revoked read returned a data/descriptor result")
			}
		})
	}
}
