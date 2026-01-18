//go:build linux
// +build linux

package main

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/IceWhaleTech/toolfs"
)

func main() {
	log.Println("=== ToolFS FUSE Quick Test ===")

	// Create ToolFS instance
	fs := toolfs.NewToolFS("/toolfs")
	log.Println("✓ ToolFS instance created")

	// Write test data to memory
	err := fs.WriteFile("/toolfs/memory/test-entry", []byte("Hello from ToolFS Memory!"))
	if err != nil {
		log.Fatalf("Error writing to memory: %v", err)
	}
	log.Println("✓ Test data written to memory")

	// Get mount point
	mountPoint := os.Getenv("MOUNT_POINT")
	if mountPoint == "" {
		// Generate random mount point to avoid conflicts
		rand.Seed(time.Now().UnixNano())
		randomID := rand.Intn(100000)
		mountPoint = filepath.Join(os.TempDir(), fmt.Sprintf("toolfs_test_%d", randomID))
	}

	// Clean up and create mount point
	os.RemoveAll(mountPoint)
	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		log.Fatalf("Failed to create mount point: %v", err)
	}

	// Mount FUSE filesystem
	err = toolfs.MountToolFS(fs, mountPoint, nil)
	if err != nil {
		log.Fatalf("Failed to mount FUSE filesystem: %v", err)
	}

	// Wait a moment for mount to stabilize
	time.Sleep(2 * time.Second)

	// Check if mount point exists and is accessible
	// Check if mount point exists and is accessible
	if _, err := os.Stat(mountPoint); err != nil {
		log.Printf("✗ Mount point directory error: %v", err)
		os.Exit(1)
	}

	// Try to list directory
	dirEntries, err := os.ReadDir(mountPoint)
	if err != nil {
		log.Printf("✗ Error reading mount point: %v", err)
		log.Println("This might mean the mount failed or isn't ready yet")
		os.Exit(1)
	}

	log.Printf("✓ Mount point is accessible! Contains %d entries:", len(dirEntries))
	for _, entry := range dirEntries {
		entryType := "directory"
		if !entry.IsDir() {
			info, _ := entry.Info()
			entryType = fmt.Sprintf("file (%d bytes)", info.Size())
		}
		log.Printf("  - %s (%s)", entry.Name(), entryType)
	}

	// Test reading memory entry
	memFile := filepath.Join(mountPoint, "memory", "test-entry")
	content, err := os.ReadFile(memFile)
	if err != nil {
		log.Printf("✗ Error reading memory entry: %v", err)
		os.Exit(1)
	}

	expected := "Hello from ToolFS Memory!"
	if string(content) != expected {
		log.Printf("✗ Content mismatch! Expected: %q, Got: %q", expected, string(content))
		os.Exit(1)
	}
	log.Printf("✓ Memory entry readable: %s", string(content))

	log.Println("\n=== All Tests Passed! ===")
	log.Printf("Filesystem is mounted and working at: %s", mountPoint)
	log.Println("You can now test manually:")
	log.Printf("  ls %s", mountPoint)
	log.Printf("  cat %s/memory/test-entry", mountPoint)
	log.Println("\nExiting in 5 seconds...")

	time.Sleep(5 * time.Second)
	log.Println("Done. Unmount manually with: fusermount -u", mountPoint)
}
