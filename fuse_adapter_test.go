//go:build linux || darwin
// +build linux darwin

package toolfs

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFUSEAdapterCompilation tests that the FUSE adapter compiles correctly
// This is a basic compilation test - actual FUSE mounting requires root/admin privileges
func TestFUSEAdapterCompilation(t *testing.T) {
	// Create a temporary ToolFS instance
	fs := NewToolFS("/toolfs")
	
	// Create a temporary directory for mounting (we won't actually mount)
	tmpDir, err := os.MkdirTemp("", "toolfs-fuse-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	
	// Test that we can create the root node
	root := NewToolFSRoot(fs)
	if root == nil {
		t.Fatal("NewToolFSRoot returned nil")
	}
	
	if root.toolfs != fs {
		t.Error("Root node's toolfs reference is incorrect")
	}
}

// TestNormalizeMountPoint tests the normalizeMountPoint function
func TestNormalizeMountPoint(t *testing.T) {
	fs := NewToolFS("/toolfs")
	
	tests := []struct {
		mountPoint string
		expected   string
	}{
		{"/toolfs/data", "data"},
		{"/toolfs/memory", "memory"},
		{"/toolfs/rag", "rag"},
		{"/toolfs", ""},
		{"/toolfs/plugins/myplugin", "plugins"},
	}
	
	for _, tt := range tests {
		t.Run(tt.mountPoint, func(t *testing.T) {
			result := fs.normalizeMountPoint(tt.mountPoint)
			if result != tt.expected {
				t.Errorf("normalizeMountPoint(%q) = %q, want %q", tt.mountPoint, result, tt.expected)
			}
		})
	}
}

// TestFUSEDirStructure tests that directory structure is correctly represented
func TestFUSEDirStructure(t *testing.T) {
	fs := NewToolFS("/toolfs")
	
	// Mount a test directory
	tmpDir, err := os.MkdirTemp("", "toolfs-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	
	// Create a test file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	
	// Mount the directory
	err = fs.MountLocal("/toolfs/test", tmpDir, false)
	if err != nil {
		t.Fatalf("Failed to mount: %v", err)
	}
	
	// Test that we can list the directory
	entries, err := fs.ListDir("/toolfs/test")
	if err != nil {
		t.Fatalf("Failed to list directory: %v", err)
	}
	
	if len(entries) == 0 {
		t.Error("Expected at least one entry in mounted directory")
	}
	
	// Test that we can read the file
	content, err := fs.ReadFile("/toolfs/test/test.txt")
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}
	
	if string(content) != "test content" {
		t.Errorf("File content mismatch: got %q, want %q", string(content), "test content")
	}
}

// TestFUSEMemoryAccess tests that memory entries can be accessed
func TestFUSEMemoryAccess(t *testing.T) {
	fs := NewToolFS("/toolfs")
	
	// Write to memory
	err := fs.WriteFile("/toolfs/memory/test-entry", []byte("memory content"))
	if err != nil {
		t.Fatalf("Failed to write to memory: %v", err)
	}
	
	// Read from memory
	content, err := fs.ReadFile("/toolfs/memory/test-entry")
	if err != nil {
		t.Fatalf("Failed to read from memory: %v", err)
	}
	
	if string(content) == "" {
		t.Error("Memory content is empty")
	}
	
	// List memory entries
	entries, err := fs.ListDir("/toolfs/memory")
	if err != nil {
		t.Fatalf("Failed to list memory: %v", err)
	}
	
	found := false
	for _, entry := range entries {
		if entry == "test-entry" || entry == "test-entry/" {
			found = true
			break
		}
	}
	
	if !found {
		t.Errorf("Memory entry not found in list: %v", entries)
	}
}


