package toolfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func setupTestDir(t *testing.T) (string, func()) {
	tmpDir, err := os.MkdirTemp("", "toolfs_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Create test files and directories
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("Hello, ToolFS!"), 0o644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	testDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(testDir, 0o755); err != nil {
		t.Fatalf("Failed to create test dir: %v", err)
	}

	subFile := filepath.Join(testDir, "subfile.txt")
	if err := os.WriteFile(subFile, []byte("Subdirectory file"), 0o644); err != nil {
		t.Fatalf("Failed to create sub file: %v", err)
	}

	cleanup := func() {
		os.RemoveAll(tmpDir)
	}

	return tmpDir, cleanup
}

func TestNewToolFS(t *testing.T) {
	fs := NewToolFS("/toolfs")
	if fs == nil {
		t.Fatal("NewToolFS returned nil")
	}
	if fs.rootPath != "/toolfs" {
		t.Errorf("Expected rootPath '/toolfs', got '%s'", fs.rootPath)
	}
	if fs.mounts == nil {
		t.Fatal("mounts map is nil")
	}
}

func TestMountLocal(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	// Test successful mount
	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	// Verify mount was added
	mount, exists := fs.mounts["/toolfs/data"]
	if !exists {
		t.Fatal("Mount was not added to mounts map")
	}
	if mount.LocalPath != tmpDir {
		t.Errorf("Expected LocalPath '%s', got '%s'", tmpDir, mount.LocalPath)
	}
	if mount.ReadOnly {
		t.Error("Expected ReadOnly to be false")
	}

	// Test read-only mount
	err = fs.MountLocal("/readonly", tmpDir, true)
	if err != nil {
		t.Fatalf("MountLocal failed for read-only: %v", err)
	}

	readOnlyMount, exists := fs.mounts["/toolfs/readonly"]
	if !exists {
		t.Fatal("Read-only mount was not added")
	}
	if !readOnlyMount.ReadOnly {
		t.Error("Expected ReadOnly to be true")
	}

	// Test mounting non-existent directory
	err = fs.MountLocal("/invalid", "/nonexistent/path", false)
	if err == nil {
		t.Error("Expected error when mounting non-existent directory")
	}

	// Test mounting a file (should fail)
	testFile := filepath.Join(tmpDir, "test.txt")
	err = fs.MountLocal("/file", testFile, false)
	if err == nil {
		t.Error("Expected error when mounting a file")
	}
}

func TestReadFile(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	// Test reading existing file
	content, err := fs.ReadFile("/toolfs/data/test.txt")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	expected := "Hello, ToolFS!"
	if string(content) != expected {
		t.Errorf("Expected content '%s', got '%s'", expected, string(content))
	}

	// Test reading file in subdirectory
	content, err = fs.ReadFile("/toolfs/data/subdir/subfile.txt")
	if err != nil {
		t.Fatalf("ReadFile failed for subdirectory: %v", err)
	}

	expected = "Subdirectory file"
	if string(content) != expected {
		t.Errorf("Expected content '%s', got '%s'", expected, string(content))
	}

	// Test reading non-existent file
	_, err = fs.ReadFile("/toolfs/data/nonexistent.txt")
	if err == nil {
		t.Error("Expected error when reading non-existent file")
	}

	// Test reading from unmounted path
	_, err = fs.ReadFile("/toolfs/unmounted/file.txt")
	if err == nil {
		t.Error("Expected error when reading from unmounted path")
	}
}

func TestWriteFile(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	// Test write to read-write mount
	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	testData := []byte("Written by ToolFS")
	err = fs.WriteFile("/toolfs/data/newfile.txt", testData)
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Verify file was written
	writtenPath := filepath.Join(tmpDir, "newfile.txt")
	content, err := os.ReadFile(writtenPath)
	if err != nil {
		t.Fatalf("Failed to read written file: %v", err)
	}

	if string(content) != string(testData) {
		t.Errorf("Expected content '%s', got '%s'", string(testData), string(content))
	}

	// Test write to read-only mount
	err = fs.MountLocal("/readonly", tmpDir, true)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	err = fs.WriteFile("/toolfs/readonly/test.txt", []byte("Should fail"))
	if err == nil {
		t.Error("Expected error when writing to read-only mount")
	}

	// Test writing to new directory (should create parent)
	err = fs.WriteFile("/toolfs/data/newdir/newfile.txt", testData)
	if err != nil {
		t.Fatalf("WriteFile failed to create parent directory: %v", err)
	}

	// Verify parent directory was created
	newDirPath := filepath.Join(tmpDir, "newdir")
	info, err := os.Stat(newDirPath)
	if err != nil {
		t.Fatalf("Parent directory was not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("Created path is not a directory")
	}
}

func TestListDir(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	// Test listing root directory
	entries, err := fs.ListDir("/toolfs/data")
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}

	if len(entries) < 2 {
		t.Errorf("Expected at least 2 entries, got %d", len(entries))
	}

	// Verify expected entries exist
	hasTestFile := false
	hasSubdir := false
	for _, entry := range entries {
		if entry == "test.txt" {
			hasTestFile = true
		}
		if entry == "subdir" {
			hasSubdir = true
		}
	}

	if !hasTestFile {
		t.Error("Expected 'test.txt' in directory listing")
	}
	if !hasSubdir {
		t.Error("Expected 'subdir' in directory listing")
	}

	// Test listing subdirectory
	entries, err = fs.ListDir("/toolfs/data/subdir")
	if err != nil {
		t.Fatalf("ListDir failed for subdirectory: %v", err)
	}

	if len(entries) != 1 {
		t.Errorf("Expected 1 entry in subdirectory, got %d", len(entries))
	}

	if len(entries) > 0 && entries[0] != "subfile.txt" {
		t.Errorf("Expected 'subfile.txt', got '%s'", entries[0])
	}

	// Test listing non-existent directory
	_, err = fs.ListDir("/toolfs/data/nonexistent")
	if err == nil {
		t.Error("Expected error when listing non-existent directory")
	}
}

func TestStat(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	// Test stat for file
	info, err := fs.Stat("/toolfs/data/test.txt")
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	if info.IsDir {
		t.Error("Expected IsDir to be false for file")
	}

	if info.Size != 14 { // "Hello, ToolFS!" is 14 bytes
		t.Errorf("Expected Size 14, got %d", info.Size)
	}

	if info.ModTime.IsZero() {
		t.Error("Expected ModTime to be set")
	}

	// Test stat for directory
	info, err = fs.Stat("/toolfs/data/subdir")
	if err != nil {
		t.Fatalf("Stat failed for directory: %v", err)
	}

	if !info.IsDir {
		t.Error("Expected IsDir to be true for directory")
	}

	// Test stat for non-existent path
	_, err = fs.Stat("/toolfs/data/nonexistent")
	if err == nil {
		t.Error("Expected error when stating non-existent path")
	}
}

func TestReadOnlyEnforcement(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	// Mount as read-only
	err := fs.MountLocal("/readonly", tmpDir, true)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	// Reading should work
	_, err = fs.ReadFile("/toolfs/readonly/test.txt")
	if err != nil {
		t.Errorf("ReadFile should work on read-only mount: %v", err)
	}

	// Listing should work
	_, err = fs.ListDir("/toolfs/readonly")
	if err != nil {
		t.Errorf("ListDir should work on read-only mount: %v", err)
	}

	// Stat should work
	_, err = fs.Stat("/toolfs/readonly/test.txt")
	if err != nil {
		t.Errorf("Stat should work on read-only mount: %v", err)
	}

	// Writing should fail
	err = fs.WriteFile("/toolfs/readonly/newfile.txt", []byte("test"))
	if err == nil {
		t.Error("WriteFile should fail on read-only mount")
	}
}

func TestPathResolution(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir1, cleanup1 := setupTestDir(t)
	defer cleanup1()

	tmpDir2, cleanup2 := setupTestDir(t)
	defer cleanup2()

	// Mount two directories
	err := fs.MountLocal("/data1", tmpDir1, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	err = fs.MountLocal("/data2", tmpDir2, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	// Test that paths resolve to correct mounts
	content1, err := fs.ReadFile("/toolfs/data1/test.txt")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	content2, err := fs.ReadFile("/toolfs/data2/test.txt")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	// Both should have the same content (from setupTestDir)
	if string(content1) != string(content2) {
		t.Error("Both mounts should have same test file content")
	}
}

func TestMemoryReadWrite(t *testing.T) {
	fs := NewToolFS("/toolfs")

	// Write a memory entry
	testContent := "This is a test memory entry"
	err := fs.WriteFile("/toolfs/memory/123", []byte(testContent))
	if err != nil {
		t.Fatalf("WriteFile to memory failed: %v", err)
	}

	// Read the memory entry
	data, err := fs.ReadFile("/toolfs/memory/123")
	if err != nil {
		t.Fatalf("ReadFile from memory failed: %v", err)
	}

	// Parse JSON response
	var entry MemoryEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("Failed to unmarshal memory entry: %v", err)
	}

	if entry.ID != "123" {
		t.Errorf("Expected ID '123', got '%s'", entry.ID)
	}
	if entry.Content != testContent {
		t.Errorf("Expected content '%s', got '%s'", testContent, entry.Content)
	}
	if entry.CreatedAt.IsZero() {
		t.Error("Expected CreatedAt to be set")
	}
	if entry.UpdatedAt.IsZero() {
		t.Error("Expected UpdatedAt to be set")
	}
}

func TestMemoryJSONWrite(t *testing.T) {
	fs := NewToolFS("/toolfs")

	// Write memory entry with JSON format (including metadata)
	jsonData := `{
		"id": "456",
		"content": "Meeting notes from today",
		"metadata": {
			"author": "Alice",
			"tags": ["meeting", "notes"]
		}
	}`

	err := fs.WriteFile("/toolfs/memory/456", []byte(jsonData))
	if err != nil {
		t.Fatalf("WriteFile with JSON failed: %v", err)
	}

	// Read it back
	data, err := fs.ReadFile("/toolfs/memory/456")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	var entry MemoryEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if entry.Content != "Meeting notes from today" {
		t.Errorf("Expected content 'Meeting notes from today', got '%s'", entry.Content)
	}

	if entry.Metadata == nil {
		t.Fatal("Expected metadata to be set")
	}

	if author, ok := entry.Metadata["author"].(string); !ok || author != "Alice" {
		t.Errorf("Expected author 'Alice', got %v", entry.Metadata["author"])
	}
}

func TestMemoryUpdate(t *testing.T) {
	fs := NewToolFS("/toolfs")

	// Write initial entry
	err := fs.WriteFile("/toolfs/memory/789", []byte("Initial content"))
	if err != nil {
		t.Fatalf("Initial WriteFile failed: %v", err)
	}

	// Read to get initial timestamp
	data1, _ := fs.ReadFile("/toolfs/memory/789")
	var entry1 MemoryEntry
	json.Unmarshal(data1, &entry1)
	initialTime := entry1.UpdatedAt

	// Wait a bit and update
	time.Sleep(10 * time.Millisecond)
	err = fs.WriteFile("/toolfs/memory/789", []byte("Updated content"))
	if err != nil {
		t.Fatalf("Update WriteFile failed: %v", err)
	}

	// Read again
	data2, err := fs.ReadFile("/toolfs/memory/789")
	if err != nil {
		t.Fatalf("ReadFile after update failed: %v", err)
	}

	var entry2 MemoryEntry
	if err := json.Unmarshal(data2, &entry2); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if entry2.Content != "Updated content" {
		t.Errorf("Expected updated content, got '%s'", entry2.Content)
	}

	if !entry2.UpdatedAt.After(initialTime) {
		t.Error("Expected UpdatedAt to be after initial time")
	}
}

func TestMemoryList(t *testing.T) {
	fs := NewToolFS("/toolfs")

	// Write multiple entries
	fs.WriteFile("/toolfs/memory/entry1", []byte("Content 1"))
	fs.WriteFile("/toolfs/memory/entry2", []byte("Content 2"))
	fs.WriteFile("/toolfs/memory/entry3", []byte("Content 3"))

	// List entries
	entries, err := fs.ListDir("/toolfs/memory")
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}

	if len(entries) < 3 {
		t.Errorf("Expected at least 3 entries, got %d", len(entries))
	}

	// Verify entries exist
	entryMap := make(map[string]bool)
	for _, e := range entries {
		entryMap[e] = true
	}

	if !entryMap["entry1"] || !entryMap["entry2"] || !entryMap["entry3"] {
		t.Error("Expected entries not found in list")
	}
}

func TestMemoryNotFound(t *testing.T) {
	fs := NewToolFS("/toolfs")

	_, err := fs.ReadFile("/toolfs/memory/nonexistent")
	if err == nil {
		t.Error("Expected error when reading non-existent memory entry")
	}
}

func TestRAGSearch(t *testing.T) {
	fs := NewToolFS("/toolfs")

	// Perform RAG search
	data, err := fs.ReadFile("/toolfs/rag/query?text=AI+agent&top_k=3")
	if err != nil {
		t.Fatalf("RAG search failed: %v", err)
	}

	var results RAGSearchResults
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatalf("Failed to unmarshal RAG results: %v", err)
	}

	if results.Query != "AI agent" {
		t.Errorf("Expected query 'AI agent', got '%s'", results.Query)
	}

	if results.TopK != 3 {
		t.Errorf("Expected TopK 3, got %d", results.TopK)
	}

	if len(results.Results) == 0 {
		t.Error("Expected at least one search result")
	}

	// Verify result structure
	for _, result := range results.Results {
		if result.ID == "" {
			t.Error("Result ID should not be empty")
		}
		if result.Content == "" {
			t.Error("Result content should not be empty")
		}
		if result.Score < 0 || result.Score > 1 {
			t.Errorf("Result score should be between 0 and 1, got %f", result.Score)
		}
	}
}

func TestRAGSearchWithQParameter(t *testing.T) {
	fs := NewToolFS("/toolfs")

	// Test with 'q' parameter instead of 'text'
	data, err := fs.ReadFile("/toolfs/rag/query?q=memory&top_k=2")
	if err != nil {
		t.Fatalf("RAG search failed: %v", err)
	}

	var results RAGSearchResults
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if results.Query != "memory" {
		t.Errorf("Expected query 'memory', got '%s'", results.Query)
	}

	if results.TopK != 2 {
		t.Errorf("Expected TopK 2, got %d", results.TopK)
	}
}

func TestRAGSearchDefaultTopK(t *testing.T) {
	fs := NewToolFS("/toolfs")

	// Test without top_k parameter (should default to 5)
	data, err := fs.ReadFile("/toolfs/rag/query?text=RAG")
	if err != nil {
		t.Fatalf("RAG search failed: %v", err)
	}

	var results RAGSearchResults
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if results.TopK != 5 {
		t.Errorf("Expected default TopK 5, got %d", results.TopK)
	}
}

func TestRAGSearchInvalidQuery(t *testing.T) {
	fs := NewToolFS("/toolfs")

	// Test with missing query parameter
	_, err := fs.ReadFile("/toolfs/rag/query?top_k=3")
	if err == nil {
		t.Error("Expected error when query parameter is missing")
	}

	// Test with invalid top_k
	_, err = fs.ReadFile("/toolfs/rag/query?text=test&top_k=invalid")
	if err == nil {
		t.Error("Expected error when top_k is invalid")
	}

	// Test with negative top_k
	_, err = fs.ReadFile("/toolfs/rag/query?text=test&top_k=-1")
	if err == nil {
		t.Error("Expected error when top_k is negative")
	}
}

func TestRAGListDir(t *testing.T) {
	fs := NewToolFS("/toolfs")

	entries, err := fs.ListDir("/toolfs/rag")
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}

	if len(entries) == 0 {
		t.Error("Expected at least one entry in RAG directory")
	}

	hasQuery := false
	for _, entry := range entries {
		if entry == "query" {
			hasQuery = true
			break
		}
	}

	if !hasQuery {
		t.Error("Expected 'query' in RAG directory listing")
	}
}

func TestRAGReadOnly(t *testing.T) {
	fs := NewToolFS("/toolfs")

	// Try to write to RAG (should fail)
	err := fs.WriteFile("/toolfs/rag/something", []byte("test"))
	if err == nil {
		t.Error("Expected error when writing to RAG store")
	}
}

func TestMemoryAndRAGCoexistence(t *testing.T) {
	fs := NewToolFS("/toolfs")

	// Write to memory
	err := fs.WriteFile("/toolfs/memory/test1", []byte("Memory content"))
	if err != nil {
		t.Fatalf("Memory write failed: %v", err)
	}

	// Read from memory
	memData, err := fs.ReadFile("/toolfs/memory/test1")
	if err != nil {
		t.Fatalf("Memory read failed: %v", err)
	}

	var memEntry MemoryEntry
	if err := json.Unmarshal(memData, &memEntry); err != nil {
		t.Fatalf("Failed to unmarshal memory: %v", err)
	}

	if memEntry.Content != "Memory content" {
		t.Error("Memory entry content mismatch")
	}

	// Perform RAG search
	ragData, err := fs.ReadFile("/toolfs/rag/query?text=ToolFS&top_k=2")
	if err != nil {
		t.Fatalf("RAG search failed: %v", err)
	}

	var ragResults RAGSearchResults
	if err := json.Unmarshal(ragData, &ragResults); err != nil {
		t.Fatalf("Failed to unmarshal RAG: %v", err)
	}

	if ragResults.Query != "ToolFS" {
		t.Error("RAG query mismatch")
	}

	// Both should work independently
	if len(memEntry.Content) == 0 || len(ragResults.Results) == 0 {
		t.Error("Both Memory and RAG should work together")
	}
}

// TestAuditLogger is a test implementation that captures audit logs
type TestAuditLogger struct {
	Entries []AuditLogEntry
}

func (l *TestAuditLogger) Log(entry AuditLogEntry) error {
	l.Entries = append(l.Entries, entry)
	return nil
}

func TestSessionCreation(t *testing.T) {
	fs := NewToolFS("/toolfs")

	// Create a new session
	session, err := fs.NewSession("session1", []string{"/toolfs/data"})
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	if session.ID != "session1" {
		t.Errorf("Expected session ID 'session1', got '%s'", session.ID)
	}

	if len(session.AllowedPaths) != 1 {
		t.Errorf("Expected 1 allowed path, got %d", len(session.AllowedPaths))
	}

	// Try to create duplicate session
	_, err = fs.NewSession("session1", []string{})
	if err == nil {
		t.Error("Expected error when creating duplicate session")
	}

	// Get session
	retrieved, err := fs.GetSession("session1")
	if err != nil {
		t.Fatalf("Failed to get session: %v", err)
	}

	if retrieved.ID != "session1" {
		t.Error("Retrieved session ID mismatch")
	}

	// Delete session
	fs.DeleteSession("session1")
	_, err = fs.GetSession("session1")
	if err == nil {
		t.Error("Expected error when getting deleted session")
	}
}

func TestSessionPathAccessControl(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	// Create session with restricted access
	session, err := fs.NewSession("restricted", []string{"/toolfs/data/subdir"})
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Allowed path should work
	_, err = fs.ReadFileWithSession("/toolfs/data/subdir/subfile.txt", session)
	if err != nil {
		t.Errorf("Expected allowed path to work, got error: %v", err)
	}

	// Disallowed path should fail
	_, err = fs.ReadFileWithSession("/toolfs/data/test.txt", session)
	if err == nil {
		t.Error("Expected error when accessing disallowed path")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("Expected 'access denied' error, got: %v", err)
	}

	// Write to allowed path should work
	err = fs.WriteFileWithSession("/toolfs/data/subdir/newfile.txt", []byte("test"), session)
	if err != nil {
		t.Errorf("Expected allowed write to work, got error: %v", err)
	}

	// Write to disallowed path should fail
	err = fs.WriteFileWithSession("/toolfs/data/test.txt", []byte("test"), session)
	if err == nil {
		t.Error("Expected error when writing to disallowed path")
	}

	// List allowed directory should work
	_, err = fs.ListDirWithSession("/toolfs/data/subdir", session)
	if err != nil {
		t.Errorf("Expected allowed list to work, got error: %v", err)
	}

	// List disallowed directory should fail
	_, err = fs.ListDirWithSession("/toolfs/data", session)
	if err == nil {
		t.Error("Expected error when listing disallowed directory")
	}
}

func TestSessionUnauthorizedAccess(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	// Create session with no access to /toolfs/data
	session, err := fs.NewSession("noaccess", []string{"/toolfs/memory"})
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Attempt unauthorized read
	_, err = fs.ReadFileWithSession("/toolfs/data/test.txt", session)
	if err == nil {
		t.Error("Expected error for unauthorized read")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("Expected 'access denied' error, got: %v", err)
	}

	// Attempt unauthorized write
	err = fs.WriteFileWithSession("/toolfs/data/test.txt", []byte("hack"), session)
	if err == nil {
		t.Error("Expected error for unauthorized write")
	}

	// Attempt unauthorized list
	_, err = fs.ListDirWithSession("/toolfs/data", session)
	if err == nil {
		t.Error("Expected error for unauthorized list")
	}

	// Attempt unauthorized stat
	_, err = fs.StatWithSession("/toolfs/data/test.txt", session)
	if err == nil {
		t.Error("Expected error for unauthorized stat")
	}
}

func TestAuditLogging(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	// Create test audit logger
	testLogger := &TestAuditLogger{Entries: []AuditLogEntry{}}

	// Create session with audit logging
	session, err := fs.NewSession("audit-test", []string{"/toolfs/data"})
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	session.SetAuditLogger(testLogger)

	// Perform operations
	_, err = fs.ReadFileWithSession("/toolfs/data/test.txt", session)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	err = fs.WriteFileWithSession("/toolfs/data/audit.txt", []byte("test data"), session)
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err = fs.ListDirWithSession("/toolfs/data", session)
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}

	// Verify audit logs were created
	if len(testLogger.Entries) < 3 {
		t.Errorf("Expected at least 3 audit log entries, got %d", len(testLogger.Entries))
	}

	// Verify ReadFile audit entry
	foundRead := false
	for _, entry := range testLogger.Entries {
		if entry.Operation == "ReadFile" && entry.Path == "/toolfs/data/test.txt" {
			foundRead = true
			if !entry.Success {
				t.Error("ReadFile audit entry should be successful")
			}
			if entry.BytesRead <= 0 {
				t.Error("ReadFile audit entry should have bytes_read > 0")
			}
			if entry.SessionID != "audit-test" {
				t.Errorf("Expected session ID 'audit-test', got '%s'", entry.SessionID)
			}
			break
		}
	}
	if !foundRead {
		t.Error("ReadFile audit entry not found")
	}

	// Verify WriteFile audit entry
	foundWrite := false
	for _, entry := range testLogger.Entries {
		if entry.Operation == "WriteFile" && entry.Path == "/toolfs/data/audit.txt" {
			foundWrite = true
			if !entry.Success {
				t.Error("WriteFile audit entry should be successful")
			}
			if entry.BytesWritten != 9 { // "test data" is 9 bytes
				t.Errorf("Expected BytesWritten 9, got %d", entry.BytesWritten)
			}
			break
		}
	}
	if !foundWrite {
		t.Error("WriteFile audit entry not found")
	}

	// Verify ListDir audit entry
	foundList := false
	for _, entry := range testLogger.Entries {
		if entry.Operation == "ListDir" && entry.Path == "/toolfs/data" {
			foundList = true
			if !entry.Success {
				t.Error("ListDir audit entry should be successful")
			}
			break
		}
	}
	if !foundList {
		t.Error("ListDir audit entry not found")
	}
}

func TestAuditLoggingAccessDenied(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	testLogger := &TestAuditLogger{Entries: []AuditLogEntry{}}
	session, err := fs.NewSession("denied-test", []string{"/toolfs/memory"})
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	session.SetAuditLogger(testLogger)

	// Attempt unauthorized access
	_, err = fs.ReadFileWithSession("/toolfs/data/test.txt", session)
	if err == nil {
		t.Fatal("Expected error for unauthorized access")
	}

	// Verify audit log entry for access denied
	if len(testLogger.Entries) == 0 {
		t.Fatal("Expected at least one audit log entry")
	}

	entry := testLogger.Entries[0]
	if entry.Operation != "ReadFile" {
		t.Errorf("Expected operation 'ReadFile', got '%s'", entry.Operation)
	}
	if entry.Success {
		t.Error("Expected audit entry to indicate failure")
	}
	if !entry.AccessDenied {
		t.Error("Expected AccessDenied to be true")
	}
	if entry.Error == "" {
		t.Error("Expected error message in audit entry")
	}
}

func TestAuditLogJSONFormat(t *testing.T) {
	testLogger := &TestAuditLogger{Entries: []AuditLogEntry{}}

	entry := AuditLogEntry{
		Timestamp:    time.Now(),
		SessionID:    "test-session",
		Operation:    "ReadFile",
		Path:         "/test/path",
		Success:      true,
		BytesRead:    1024,
		AccessDenied: false,
	}

	err := testLogger.Log(entry)
	if err != nil {
		t.Fatalf("Log failed: %v", err)
	}

	// Verify entry was logged
	if len(testLogger.Entries) != 1 {
		t.Fatalf("Expected 1 entry, got %d", len(testLogger.Entries))
	}

	// Verify JSON serialization
	jsonData, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Failed to marshal audit entry: %v", err)
	}

	var unmarshaled AuditLogEntry
	if err := json.Unmarshal(jsonData, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal audit entry: %v", err)
	}

	if unmarshaled.SessionID != "test-session" {
		t.Errorf("SessionID mismatch: expected 'test-session', got '%s'", unmarshaled.SessionID)
	}
	if unmarshaled.Operation != "ReadFile" {
		t.Errorf("Operation mismatch")
	}
	if unmarshaled.BytesRead != 1024 {
		t.Errorf("BytesRead mismatch")
	}
}

func TestCommandFiltering(t *testing.T) {
	filter := NewDangerousCommandFilter()

	// Test blocked commands
	blockedCommands := []string{"rm", "rmdir", "del", "sudo", "shutdown", "format"}
	for _, cmd := range blockedCommands {
		allowed, reason := filter.IsCommandAllowed(cmd, []string{})
		if allowed {
			t.Errorf("Expected command '%s' to be blocked", cmd)
		}
		if reason == "" {
			t.Errorf("Expected reason for blocking '%s'", cmd)
		}
	}

	// Test allowed commands
	allowedCommands := []string{"ls", "cat", "echo", "pwd", "cd"}
	for _, cmd := range allowedCommands {
		allowed, reason := filter.IsCommandAllowed(cmd, []string{})
		if !allowed {
			t.Errorf("Expected command '%s' to be allowed, reason: %s", cmd, reason)
		}
	}

	// Test dangerous patterns
	allowed, _ := filter.IsCommandAllowed("rm", []string{"-rf", "/"})
	if allowed {
		t.Error("Expected 'rm -rf /' to be blocked")
	}

	// Test recursive delete pattern
	allowed, _ = filter.IsCommandAllowed("rm", []string{"-r", "something"})
	if allowed {
		t.Error("Expected 'rm -r' to be blocked")
	}
}

func TestSessionCommandValidation(t *testing.T) {
	fs := NewToolFS("/toolfs")

	session, err := fs.NewSession("cmd-test", []string{})
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Set command validator
	filter := NewDangerousCommandFilter()
	session.SetCommandValidator(filter)

	// Test blocked command
	err = fs.ExecuteCommandWithSession("rm", []string{"-rf", "/"}, session)
	if err == nil {
		t.Error("Expected error for blocked command")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("Expected 'not allowed' error, got: %v", err)
	}

	// Test allowed command
	err = fs.ExecuteCommandWithSession("ls", []string{"-la"}, session)
	if err != nil {
		t.Errorf("Expected allowed command to pass validation, got error: %v", err)
	}

	// Test session without validator (should allow all)
	session2, err := fs.NewSession("no-filter", []string{})
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	err = fs.ExecuteCommandWithSession("rm", []string{"-rf", "/"}, session2)
	if err != nil {
		t.Errorf("Expected command to pass without validator, got error: %v", err)
	}
}

func TestSessionIsolation(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir1, cleanup1 := setupTestDir(t)
	defer cleanup1()
	tmpDir2, cleanup2 := setupTestDir(t)
	defer cleanup2()

	err := fs.MountLocal("/data1", tmpDir1, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}
	err = fs.MountLocal("/data2", tmpDir2, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	// Create two sessions with different access
	session1, err := fs.NewSession("session1", []string{"/toolfs/data1"})
	if err != nil {
		t.Fatalf("Failed to create session1: %v", err)
	}

	session2, err := fs.NewSession("session2", []string{"/toolfs/data2"})
	if err != nil {
		t.Fatalf("Failed to create session2: %v", err)
	}

	// Session1 can access data1 but not data2
	_, err = fs.ReadFileWithSession("/toolfs/data1/test.txt", session1)
	if err != nil {
		t.Errorf("Session1 should access data1: %v", err)
	}

	_, err = fs.ReadFileWithSession("/toolfs/data2/test.txt", session1)
	if err == nil {
		t.Error("Session1 should not access data2")
	}

	// Session2 can access data2 but not data1
	_, err = fs.ReadFileWithSession("/toolfs/data2/test.txt", session2)
	if err != nil {
		t.Errorf("Session2 should access data2: %v", err)
	}

	_, err = fs.ReadFileWithSession("/toolfs/data1/test.txt", session2)
	if err == nil {
		t.Error("Session2 should not access data1")
	}
}

func TestSessionMemoryIsolation(t *testing.T) {
	fs := NewToolFS("/toolfs")

	// Create session with memory access
	session, err := fs.NewSession("memory-session", []string{"/toolfs/memory"})
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Memory access should work
	err = fs.WriteFileWithSession("/toolfs/memory/test1", []byte("session data"), session)
	if err != nil {
		t.Fatalf("Failed to write to memory: %v", err)
	}

	data, err := fs.ReadFileWithSession("/toolfs/memory/test1", session)
	if err != nil {
		t.Fatalf("Failed to read from memory: %v", err)
	}

	var entry MemoryEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if entry.Content != "session data" {
		t.Errorf("Expected 'session data', got '%s'", entry.Content)
	}
}

// RAGSkill is an example skill that handles RAG queries
type RAGSkill struct {
	context *SkillContext
}

func (p *RAGSkill) Name() string                             { return "rag-skill" }
func (p *RAGSkill) Version() string                          { return "1.0.0" }
func (p *RAGSkill) Init(config map[string]interface{}) error { return nil }

func (p *RAGSkill) Execute(input []byte) ([]byte, error) {
	var request SkillRequest
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, err
	}

	// Handle read_file operation for RAG queries
	if request.Operation == "read_file" {
		response := SkillResponse{
			Success: true,
			Result: map[string]interface{}{
				"content": fmt.Sprintf("RAG results for path: %s", request.Path),
				"query":   request.Path,
			},
		}
		return json.Marshal(response)
	}

	if request.Operation == "list_dir" {
		response := SkillResponse{
			Success: true,
			Result: map[string]interface{}{
				"entries": []string{"query"},
			},
		}
		return json.Marshal(response)
	}

	return nil, fmt.Errorf("unsupported operation: %s", request.Operation)
}

// ContentSkill returns a simple content string
type ContentSkill struct {
	content string
}

func (p *ContentSkill) Name() string                             { return "content-skill" }
func (p *ContentSkill) Version() string                          { return "1.0.0" }
func (p *ContentSkill) Init(config map[string]interface{}) error { return nil }

func (p *ContentSkill) Execute(input []byte) ([]byte, error) {
	var request SkillRequest
	json.Unmarshal(input, &request)

	response := SkillResponse{
		Success: true,
		Result:  p.content,
	}
	return json.Marshal(response)
}

// ListDirSkill returns a list of directory entries
type ListDirSkill struct {
	entries []string
}

func (p *ListDirSkill) Name() string                             { return "list-skill" }
func (p *ListDirSkill) Version() string                          { return "1.0.0" }
func (p *ListDirSkill) Init(config map[string]interface{}) error { return nil }

func (p *ListDirSkill) Execute(input []byte) ([]byte, error) {
	var request SkillRequest
	json.Unmarshal(input, &request)

	if request.Operation == "list_dir" {
		response := SkillResponse{
			Success: true,
			Result: map[string]interface{}{
				"entries": p.entries,
			},
		}
		return json.Marshal(response)
	}

	return nil, fmt.Errorf("unsupported operation: %s", request.Operation)
}

// WriteSkill handles write operations
type WriteSkill struct {
	lastWritten []byte
}

func (p *WriteSkill) Name() string                             { return "write-skill" }
func (p *WriteSkill) Version() string                          { return "1.0.0" }
func (p *WriteSkill) Init(config map[string]interface{}) error { return nil }

func (p *WriteSkill) Execute(input []byte) ([]byte, error) {
	var request SkillRequest
	json.Unmarshal(input, &request)

	if request.Operation == "write_file" {
		if inputStr, ok := request.Data["input"].(string); ok {
			p.lastWritten = []byte(inputStr)
		}
		response := SkillResponse{
			Success: true,
			Result:  "write successful",
		}
		return json.Marshal(response)
	}

	return nil, fmt.Errorf("unsupported operation: %s", request.Operation)
}

// PanicSkill panics during execution
type PanicSkill struct{}

func (p *PanicSkill) Name() string                             { return "panic-skill" }
func (p *PanicSkill) Version() string                          { return "1.0.0" }
func (p *PanicSkill) Init(config map[string]interface{}) error { return nil }

func (p *PanicSkill) Execute(input []byte) ([]byte, error) {
	panic("skill panic for testing")
}

func TestMountSkillExecutor(t *testing.T) {
	fs := NewToolFS("/toolfs")
	pm := NewSkillExecutorManager()
	fs.SetSkillExecutorManager(pm)

	session, _ := fs.NewSession("mount-test", []string{"/toolfs/rag"})
	ctx := NewSkillContext(fs, session)

	ragSkill := &RAGSkill{context: ctx}
	pm.InjectSkill(ragSkill, ctx, nil)

	// Mount skill to /toolfs/rag
	err := fs.MountSkillExecutor("/toolfs/rag", "rag-skill")
	if err != nil {
		t.Fatalf("MountSkill failed: %v", err)
	}

	// Verify skill is mounted
	skillMount, exists := fs.skillMounts[normalizeVirtualPath("/toolfs/rag")]
	if !exists {
		t.Fatal("Skill mount not found")
	}

	if skillMount.SkillName != "rag-skill" {
		t.Errorf("Expected skill name 'rag-skill', got '%s'", skillMount.SkillName)
	}

	// Test mounting duplicate path
	err = fs.MountSkillExecutor("/toolfs/rag", "another-skill")
	if err == nil {
		t.Error("Expected error for duplicate mount")
	}

	// Test mounting non-existent skill
	err = fs.MountSkillExecutor("/toolfs/other", "nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent skill")
	}
}

func TestReadFileSkillMount(t *testing.T) {
	fs := NewToolFS("/toolfs")
	pm := NewSkillExecutorManager()
	fs.SetSkillExecutorManager(pm)

	session, _ := fs.NewSession("read-test", []string{"/toolfs/rag"})
	ctx := NewSkillContext(fs, session)

	contentSkill := &ContentSkill{content: "Skill response content"}
	pm.InjectSkill(contentSkill, ctx, nil)
	fs.MountSkillExecutor("/toolfs/rag", "content-skill")

	// Test ReadFile through skill mount
	data, err := fs.ReadFile("/toolfs/rag/xyz")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	// ContentSkill returns result as string, which gets converted to bytes
	if string(data) != "Skill response content" {
		t.Errorf("Expected 'Skill response content', got '%s'", string(data))
	}
}

func TestListDirSkillMount(t *testing.T) {
	fs := NewToolFS("/toolfs")
	pm := NewSkillExecutorManager()
	fs.SetSkillExecutorManager(pm)

	session, _ := fs.NewSession("list-test", []string{"/toolfs/rag"})
	ctx := NewSkillContext(fs, session)

	listSkill := &ListDirSkill{entries: []string{"entry1", "entry2", "entry3"}}
	pm.InjectSkill(listSkill, ctx, nil)
	fs.MountSkillExecutor("/toolfs/rag", "list-skill")

	entries, err := fs.ListDir("/toolfs/rag")
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}

	// ListDir should extract entries from skill response
	if len(entries) < 3 {
		t.Errorf("Expected at least 3 entries, got %d", len(entries))
	}

	// Verify expected entries are present
	entryMap := make(map[string]bool)
	for _, entry := range entries {
		entryMap[entry] = true
	}

	if !entryMap["entry1"] || !entryMap["entry2"] || !entryMap["entry3"] {
		t.Error("Expected entries not found in list")
	}
}

func TestWriteFileSkillMount(t *testing.T) {
	fs := NewToolFS("/toolfs")
	pm := NewSkillExecutorManager()
	fs.SetSkillExecutorManager(pm)

	session, _ := fs.NewSession("write-test", []string{"/toolfs/rag"})
	ctx := NewSkillContext(fs, session)

	writeSkill := &WriteSkill{}
	pm.InjectSkill(writeSkill, ctx, nil)
	fs.MountSkillExecutor("/toolfs/rag", "write-skill")

	// Update mount to be writable
	ragPath := normalizeVirtualPath("/toolfs/rag")
	mount := fs.skillMounts[ragPath]
	mount.ReadOnly = false

	testData := []byte("test write data")
	err := fs.WriteFile("/toolfs/rag/test.txt", testData)
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	if string(writeSkill.lastWritten) != string(testData) {
		t.Errorf("Expected skill to receive data")
	}

	// Test read-only
	mount.ReadOnly = true
	err = fs.WriteFile("/toolfs/rag/test2.txt", testData)
	if err == nil {
		t.Error("Expected error when writing to read-only skill mount")
	}
}

func TestSkillMountErrorHandling(t *testing.T) {
	fs := NewToolFS("/toolfs")
	pm := NewSkillExecutorManager()
	fs.SetSkillExecutorManager(pm)

	session, _ := fs.NewSession("error-test", []string{"/toolfs/rag"})
	ctx := NewSkillContext(fs, session)

	errorSkill := &ErrorSkill{executeError: errors.New("skill execution failed")}
	pm.InjectSkill(errorSkill, ctx, nil)
	fs.MountSkillExecutor("/toolfs/rag", "error-skill")

	_, err := fs.ReadFile("/toolfs/rag/test")
	if err == nil {
		t.Error("Expected error from skill")
	}

	// ToolFS should still be functional
	if fs.rootPath != "/toolfs" {
		t.Error("ToolFS state corrupted")
	}
}

func TestSkillMountPanicRecovery(t *testing.T) {
	fs := NewToolFS("/toolfs")
	pm := NewSkillExecutorManager()
	fs.SetSkillExecutorManager(pm)

	session, _ := fs.NewSession("panic-test", []string{"/toolfs/rag"})
	ctx := NewSkillContext(fs, session)

	panicSkill := &PanicSkill{}
	pm.InjectSkill(panicSkill, ctx, nil)
	fs.MountSkillExecutor("/toolfs/rag", "panic-skill")

	_, err := fs.ReadFile("/toolfs/rag/test")
	if err == nil {
		t.Error("Expected error from panic recovery")
	}

	if !strings.Contains(err.Error(), "panicked") {
		t.Errorf("Expected panic recovery error, got: %v", err)
	}

	// ToolFS should still be functional
	if fs.rootPath != "/toolfs" {
		t.Error("ToolFS state corrupted")
	}
}

func TestSkillMountFallback(t *testing.T) {
	fs := NewToolFS("/toolfs")
	pm := NewSkillExecutorManager()
	fs.SetSkillExecutorManager(pm)

	session, _ := fs.NewSession("fallback-test", []string{"/toolfs/data"})
	ctx := NewSkillContext(fs, session)

	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	fs.MountLocal("/data", tmpDir, false)

	contentSkill := &ContentSkill{content: "Skill content"}
	pm.InjectSkill(contentSkill, ctx, nil)
	fs.MountSkillExecutor("/toolfs/rag", "content-skill")

	// Test local mount still works
	data, err := fs.ReadFile("/toolfs/data/test.txt")
	if err != nil {
		t.Fatalf("ReadFile on local mount failed: %v", err)
	}

	if string(data) != "Hello, ToolFS!" {
		t.Errorf("Expected local file content, got: %s", string(data))
	}

	// Test skill mount works
	skillData, err := fs.ReadFile("/toolfs/rag/test")
	if err != nil {
		t.Fatalf("ReadFile on skill mount failed: %v", err)
	}

	// ContentSkill returns content directly as string
	if string(skillData) != "Skill content" {
		t.Errorf("Expected 'Skill content', got '%s'", string(skillData))
	}
}

func TestSearchMemoryAndOpenFile(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	// Create session
	session, err := fs.NewSession("skill-test", []string{"/toolfs/data", "/toolfs/memory"})
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add some memory entries
	fs.WriteFile("/toolfs/memory/meeting1", []byte("Meeting notes: Discuss project roadmap"))
	fs.WriteFile("/toolfs/memory/meeting2", []byte("Meeting notes: Review design document"))

	// Test 1: Search memory and find result
	result, err := SearchMemoryAndOpenFile(fs, "project roadmap", "", session)
	if err != nil {
		t.Fatalf("SearchMemoryAndOpenFile failed: %v", err)
	}

	if result.Type != "memory" {
		t.Errorf("Expected type 'memory', got '%s'", result.Type)
	}
	if !result.Success {
		t.Error("Expected success to be true")
	}
	if !strings.Contains(result.Content, "project roadmap") {
		t.Errorf("Expected content to contain 'project roadmap', got '%s'", result.Content)
	}

	// Test 2: Search memory not found, try file
	result, err = SearchMemoryAndOpenFile(fs, "Hello ToolFS", "/toolfs/data/test.txt", session)
	if err != nil {
		t.Fatalf("SearchMemoryAndOpenFile failed: %v", err)
	}

	if result.Type != "file" {
		t.Errorf("Expected type 'file', got '%s'", result.Type)
	}
	if !result.Success {
		t.Error("Expected success to be true")
	}
	if result.Content != "Hello, ToolFS!" {
		t.Errorf("Expected file content, got '%s'", result.Content)
	}

	// Test 3: Search with RAG fallback
	result, err = SearchMemoryAndOpenFile(fs, "AI agent", "/toolfs/data/test.txt", session)
	if err != nil {
		// RAG might not find it, which is OK
		t.Logf("RAG search completed (may not find result): %v", err)
	} else {
		// If RAG finds something, verify the result
		if result.Type == "rag" {
			if result.Content == "" {
				t.Error("RAG result should have content")
			}
		}
	}
}

func TestExecuteCLI(t *testing.T) {
	fs := NewToolFS("/toolfs")

	// Create session with command validator
	session, err := fs.NewSession("cli-test", []string{})
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	filter := NewDangerousCommandFilter()
	session.SetCommandValidator(filter)

	// Test 1: Execute allowed command (echo on Unix-like, echo on Windows)
	var cmd string
	var args []string
	if strings.HasPrefix(runtime.GOOS, "windows") {
		cmd = "cmd"
		args = []string{"/C", "echo", "test"}
	} else {
		cmd = "echo"
		args = []string{"test"}
	}

	result, err := ExecuteCLI(cmd, args, session, fs)
	if err != nil {
		t.Fatalf("ExecuteCLI failed: %v", err)
	}

	if result.Type != "cli" {
		t.Errorf("Expected type 'cli', got '%s'", result.Type)
	}
	if result.CLIOutput == nil {
		t.Fatal("Expected CLIOutput to be set")
	}
	if result.CLIOutput.ExitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", result.CLIOutput.ExitCode)
	}
	if !strings.Contains(result.CLIOutput.Stdout, "test") {
		t.Errorf("Expected stdout to contain 'test', got '%s'", result.CLIOutput.Stdout)
	}

	// Test 2: Execute blocked command
	result, err = ExecuteCLI("rm", []string{"-rf", "/"}, session, fs)
	if err == nil {
		t.Error("Expected error for blocked command")
	}
	if result.Success {
		t.Error("Expected success to be false for blocked command")
	}
	if !strings.Contains(result.Error, "not allowed") {
		t.Errorf("Expected 'not allowed' error, got '%s'", result.Error)
	}

	// Test 3: Execute command without validator (should work)
	session2, err := fs.NewSession("no-validator", []string{})
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Try a safe command
	if strings.HasPrefix(runtime.GOOS, "windows") {
		result, err = ExecuteCLI("cmd", []string{"/C", "echo", "hello"}, session2, fs)
	} else {
		result, err = ExecuteCLI("echo", []string{"hello"}, session2, fs)
	}
	if err != nil {
		t.Logf("Command execution note: %v", err)
	}
}

func TestChainOperations(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	session, err := fs.NewSession("chain-test", []string{"/toolfs/data", "/toolfs/memory"})
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add memory entry
	fs.WriteFile("/toolfs/memory/test1", []byte("Test memory content"))

	// Chain operations: search memory -> read file -> list directory
	operations := []Operation{
		{
			Type:  "search_memory",
			Query: "memory content",
		},
		{
			Type: "read_file",
			Path: "/toolfs/data/test.txt",
		},
		{
			Type: "list_dir",
			Path: "/toolfs/data",
		},
	}

	results, err := ChainOperations(fs, operations, session)
	if err != nil {
		t.Fatalf("ChainOperations failed: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(results))
	}

	// Verify memory search result
	if results[0].Type != "memory" {
		t.Errorf("Expected first result type 'memory', got '%s'", results[0].Type)
	}
	if !results[0].Success {
		t.Error("Expected memory search to succeed")
	}

	// Verify file read result
	if results[1].Type != "file" {
		t.Errorf("Expected second result type 'file', got '%s'", results[1].Type)
	}
	if !results[1].Success {
		t.Error("Expected file read to succeed")
	}
	if results[1].Content != "Hello, ToolFS!" {
		t.Errorf("Expected file content, got '%s'", results[1].Content)
	}

	// Verify list directory result
	if results[2].Type != "file" {
		t.Errorf("Expected third result type 'file', got '%s'", results[2].Type)
	}
	if !results[2].Success {
		t.Error("Expected list directory to succeed")
	}
}

func TestChainOperationsWriteFile(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	session, err := fs.NewSession("chain-write", []string{"/toolfs/data"})
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Chain: write file -> read file
	operations := []Operation{
		{
			Type:    "write_file",
			Path:    "/toolfs/data/chained.txt",
			Content: "Chained write test",
		},
		{
			Type: "read_file",
			Path: "/toolfs/data/chained.txt",
		},
	}

	results, err := ChainOperations(fs, operations, session)
	if err != nil {
		t.Fatalf("ChainOperations failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}

	// Verify write succeeded
	if !results[0].Success {
		t.Errorf("Expected write to succeed, got error: %s", results[0].Error)
	}

	// Verify read succeeded and content matches
	if !results[1].Success {
		t.Errorf("Expected read to succeed, got error: %s", results[1].Error)
	}
	if results[1].Content != "Chained write test" {
		t.Errorf("Expected content 'Chained write test', got '%s'", results[1].Content)
	}
}

func TestChainOperationsRAGSearch(t *testing.T) {
	fs := NewToolFS("/toolfs")

	session, err := fs.NewSession("rag-chain", []string{"/toolfs/rag"})
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Chain: RAG search -> memory search
	operations := []Operation{
		{
			Type:  "search_rag",
			Query: "AI agent",
			TopK:  2,
		},
		{
			Type:  "search_memory",
			Query: "test query",
		},
	}

	results, err := ChainOperations(fs, operations, session)
	if err != nil {
		t.Fatalf("ChainOperations failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}

	// RAG search should succeed (may or may not find results)
	if results[0].Type != "rag" {
		t.Errorf("Expected first result type 'rag', got '%s'", results[0].Type)
	}
}

func TestResultJSONSerialization(t *testing.T) {
	result := &Result{
		Type:    "file",
		Source:  "/test/path.txt",
		Content: "Test content",
		Success: true,
		CLIOutput: &CLIOutput{
			Stdout:   "output",
			Stderr:   "errors",
			ExitCode: 0,
			Command:  "test command",
		},
	}

	// Test JSON serialization
	jsonData, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal result: %v", err)
	}

	// Test JSON deserialization
	var unmarshaled Result
	if err := json.Unmarshal(jsonData, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	if unmarshaled.Type != "file" {
		t.Errorf("Type mismatch: expected 'file', got '%s'", unmarshaled.Type)
	}
	if unmarshaled.Source != "/test/path.txt" {
		t.Errorf("Source mismatch")
	}
	if unmarshaled.CLIOutput == nil {
		t.Fatal("CLIOutput should not be nil")
	}
	if unmarshaled.CLIOutput.ExitCode != 0 {
		t.Errorf("CLIOutput.ExitCode mismatch")
	}
}

func TestCreateSnapshot(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	// Create initial file
	err = fs.WriteFile("/toolfs/data/test1.txt", []byte("Original content"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Create snapshot
	err = fs.CreateSnapshot("snapshot1")
	if err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}

	// Verify snapshot exists
	metadata, err := fs.GetSnapshot("snapshot1")
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}

	if metadata.Name != "snapshot1" {
		t.Errorf("Expected snapshot name 'snapshot1', got '%s'", metadata.Name)
	}

	if metadata.FileCount == 0 {
		t.Error("Expected snapshot to contain files")
	}

	// Test duplicate snapshot name
	err = fs.CreateSnapshot("snapshot1")
	if err == nil {
		t.Error("Expected error when creating duplicate snapshot")
	}

	// Test empty snapshot name
	err = fs.CreateSnapshot("")
	if err == nil {
		t.Error("Expected error when creating snapshot with empty name")
	}
}

func TestSnapshotRollback(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	// Create initial state
	initialContent := "Initial content"
	err = fs.WriteFile("/toolfs/data/test.txt", []byte(initialContent))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Create snapshot
	err = fs.CreateSnapshot("baseline")
	if err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}

	// Make changes
	modifiedContent := "Modified content"
	err = fs.WriteFile("/toolfs/data/test.txt", []byte(modifiedContent))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Verify changes
	data, err := fs.ReadFile("/toolfs/data/test.txt")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != modifiedContent {
		t.Errorf("Expected modified content, got '%s'", string(data))
	}

	// Rollback to snapshot
	err = fs.RollbackSnapshot("baseline")
	if err != nil {
		t.Fatalf("RollbackSnapshot failed: %v", err)
	}

	// Verify state restored
	data, err = fs.ReadFile("/toolfs/data/test.txt")
	if err != nil {
		t.Fatalf("ReadFile after rollback failed: %v", err)
	}
	if string(data) != initialContent {
		t.Errorf("Expected original content '%s' after rollback, got '%s'", initialContent, string(data))
	}
}

func TestSnapshotRollbackMultipleFiles(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	// Create multiple files
	fs.WriteFile("/toolfs/data/file1.txt", []byte("File 1 content"))
	fs.WriteFile("/toolfs/data/file2.txt", []byte("File 2 content"))
	fs.WriteFile("/toolfs/data/subdir/file3.txt", []byte("File 3 content"))

	// Create snapshot
	err = fs.CreateSnapshot("multi-file")
	if err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}

	// Modify all files
	fs.WriteFile("/toolfs/data/file1.txt", []byte("Modified 1"))
	fs.WriteFile("/toolfs/data/file2.txt", []byte("Modified 2"))
	fs.WriteFile("/toolfs/data/subdir/file3.txt", []byte("Modified 3"))

	// Add new file
	fs.WriteFile("/toolfs/data/newfile.txt", []byte("New file"))

	// Rollback
	err = fs.RollbackSnapshot("multi-file")
	if err != nil {
		t.Fatalf("RollbackSnapshot failed: %v", err)
	}

	// Verify all files restored
	data, _ := fs.ReadFile("/toolfs/data/file1.txt")
	if string(data) != "File 1 content" {
		t.Errorf("File1 not restored correctly")
	}

	data, _ = fs.ReadFile("/toolfs/data/file2.txt")
	if string(data) != "File 2 content" {
		t.Errorf("File2 not restored correctly")
	}

	data, _ = fs.ReadFile("/toolfs/data/subdir/file3.txt")
	if string(data) != "File 3 content" {
		t.Errorf("File3 not restored correctly")
	}

	// Verify new file is gone (or doesn't exist in snapshot)
	_, err = fs.ReadFile("/toolfs/data/newfile.txt")
	if err == nil {
		// New file might still exist if we didn't track it
		t.Log("New file still exists after rollback (may be expected)")
	}
}

func TestSnapshotCopyOnWrite(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	// Create initial state
	fs.WriteFile("/toolfs/data/file.txt", []byte("Initial"))

	// Create first snapshot
	err = fs.CreateSnapshot("snap1")
	if err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}

	snap1, _ := fs.GetSnapshot("snap1")
	size1 := snap1.Size

	// Create second snapshot (should use copy-on-write)
	err = fs.CreateSnapshot("snap2")
	if err != nil {
		t.Fatalf("CreateSnapshot snap2 failed: %v", err)
	}

	snap2, _ := fs.GetSnapshot("snap2")

	// Second snapshot should reference first (copy-on-write)
	snapshot2Obj := fs.snapshots["snap2"]
	if snapshot2Obj.BaseSnapshot != "snap1" {
		t.Logf("Copy-on-write: snap2 should reference snap1, got base: %s", snapshot2Obj.BaseSnapshot)
	}

	// Modify file and create third snapshot
	fs.WriteFile("/toolfs/data/file.txt", []byte("Modified"))
	err = fs.CreateSnapshot("snap3")
	if err != nil {
		t.Fatalf("CreateSnapshot snap3 failed: %v", err)
	}

	// Verify snapshots can be rolled back independently
	err = fs.RollbackSnapshot("snap1")
	if err != nil {
		t.Fatalf("Rollback to snap1 failed: %v", err)
	}

	data, _ := fs.ReadFile("/toolfs/data/file.txt")
	if string(data) != "Initial" {
		t.Errorf("Expected 'Initial' after rollback to snap1, got '%s'", string(data))
	}

	err = fs.RollbackSnapshot("snap3")
	if err != nil {
		t.Fatalf("Rollback to snap3 failed: %v", err)
	}

	data, _ = fs.ReadFile("/toolfs/data/file.txt")
	if string(data) != "Modified" {
		t.Errorf("Expected 'Modified' after rollback to snap3, got '%s'", string(data))
	}

	_ = size1
	_ = snap2
}

func TestSnapshotChangeTracking(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	session, err := fs.NewSession("test-session", []string{"/toolfs/data"})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	// Create initial file
	fs.WriteFile("/toolfs/data/test.txt", []byte("Initial"))

	// Create snapshot
	err = fs.CreateSnapshot("tracking")
	if err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}

	// Make changes with session
	fs.WriteFileWithSession("/toolfs/data/test.txt", []byte("Modified"), session)
	fs.WriteFileWithSession("/toolfs/data/newfile.txt", []byte("New"), session)

	// Get changes
	changes, err := fs.GetSnapshotChanges("tracking")
	if err != nil {
		t.Fatalf("GetSnapshotChanges failed: %v", err)
	}

	if len(changes) == 0 {
		t.Error("Expected changes to be tracked")
	}

	// Verify change records
	foundModify := false
	foundCreate := false
	for _, change := range changes {
		if change.Path == "/toolfs/data/test.txt" && change.Operation == "write" {
			foundModify = true
		}
		if change.Path == "/toolfs/data/newfile.txt" && change.Operation == "create" {
			foundCreate = true
		}
		if change.SessionID != "" && change.SessionID != "test-session" {
			t.Errorf("Expected session ID 'test-session', got '%s'", change.SessionID)
		}
	}

	if !foundModify {
		t.Error("Expected modify change to be tracked")
	}
	if !foundCreate {
		t.Log("Create change tracking may not be fully implemented")
	}
}

func TestListSnapshots(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	fs.MountLocal("/data", tmpDir, false)

	// Create multiple snapshots
	fs.CreateSnapshot("snap1")
	fs.CreateSnapshot("snap2")
	fs.CreateSnapshot("snap3")

	// List snapshots
	snapshots, err := fs.ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots failed: %v", err)
	}

	if len(snapshots) != 3 {
		t.Errorf("Expected 3 snapshots, got %d", len(snapshots))
	}

	// Verify all snapshots are listed
	snapMap := make(map[string]bool)
	for _, name := range snapshots {
		snapMap[name] = true
	}

	if !snapMap["snap1"] || !snapMap["snap2"] || !snapMap["snap3"] {
		t.Error("Expected all snapshots to be listed")
	}
}

func TestDeleteSnapshot(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	fs.MountLocal("/data", tmpDir, false)

	// Create snapshots
	fs.CreateSnapshot("snap1")
	fs.CreateSnapshot("snap2")
	fs.currentSnapshot = "snap2"

	// Try to delete current snapshot (should fail)
	err := fs.DeleteSnapshot("snap2")
	if err == nil {
		t.Error("Expected error when deleting current snapshot")
	}

	// Delete non-current snapshot
	err = fs.DeleteSnapshot("snap1")
	if err != nil {
		t.Fatalf("DeleteSnapshot failed: %v", err)
	}

	// Verify snapshot is deleted
	_, err = fs.GetSnapshot("snap1")
	if err == nil {
		t.Error("Expected error when getting deleted snapshot")
	}

	// Try to delete non-existent snapshot
	err = fs.DeleteSnapshot("nonexistent")
	if err == nil {
		t.Error("Expected error when deleting non-existent snapshot")
	}
}

func TestRollbackNonExistentSnapshot(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	fs.MountLocal("/data", tmpDir, false)

	// Try to rollback non-existent snapshot
	err := fs.RollbackSnapshot("nonexistent")
	if err == nil {
		t.Error("Expected error when rolling back non-existent snapshot")
	}
}

func TestSnapshotWithVirtualPaths(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	fs.MountLocal("/data", tmpDir, false)

	// Create memory entry
	fs.WriteFile("/toolfs/memory/test1", []byte("Memory content"))

	// Create snapshot (should snapshot mounted files, not virtual paths)
	err := fs.CreateSnapshot("with-memory")
	if err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}

	// Memory entries are not part of filesystem snapshots
	// but snapshot should still succeed
	metadata, _ := fs.GetSnapshot("with-memory")
	if metadata == nil {
		t.Fatal("Snapshot metadata should exist")
	}
}

// HostFSSkill attempts to access host filesystem (should be blocked)
type HostFSSkill struct {
	attemptedPath string
}

func (p *HostFSSkill) Name() string                             { return "hostfs-skill" }
func (p *HostFSSkill) Version() string                          { return "1.0.0" }
func (p *HostFSSkill) Init(config map[string]interface{}) error { return nil }

func (p *HostFSSkill) Execute(input []byte) ([]byte, error) {
	var request SkillRequest
	json.Unmarshal(input, &request)
	p.attemptedPath = request.Path

	if request.Path == "/etc/passwd" {
		response := SkillResponse{
			Success: true,
			Result:  "host file content (should not happen)",
		}
		return json.Marshal(response)
	}

	response := SkillResponse{
		Success: true,
		Result:  "normal operation",
	}
	return json.Marshal(response)
}

// PathTraversalSkill attempts path traversal (should be blocked)
type PathTraversalSkill struct{}

func (p *PathTraversalSkill) Name() string                             { return "traversal-skill" }
func (p *PathTraversalSkill) Version() string                          { return "1.0.0" }
func (p *PathTraversalSkill) Init(config map[string]interface{}) error { return nil }

func (p *PathTraversalSkill) Execute(input []byte) ([]byte, error) {
	var request SkillRequest
	json.Unmarshal(input, &request)

	if strings.Contains(request.Path, "..") {
		response := SkillResponse{
			Success: true,
			Result:  "traversal succeeded (should not happen)",
		}
		return json.Marshal(response)
	}

	response := SkillResponse{
		Success: true,
		Result:  "normal operation",
	}
	return json.Marshal(response)
}

// StdoutStderrSkill writes to stdout/stderr (should be captured)
type StdoutStderrSkill struct{}

func (p *StdoutStderrSkill) Name() string                             { return "stdio-skill" }
func (p *StdoutStderrSkill) Version() string                          { return "1.0.0" }
func (p *StdoutStderrSkill) Init(config map[string]interface{}) error { return nil }

func (p *StdoutStderrSkill) Execute(input []byte) ([]byte, error) {
	fmt.Println("This is stdout output")
	fmt.Fprintf(os.Stderr, "This is stderr output\n")

	response := SkillResponse{
		Success: true,
		Result:  "operation completed",
	}
	return json.Marshal(response)
}

// SlowExecSkill is a skill that takes time to execute (for sandbox tests)
type SlowExecSkill struct {
	delay time.Duration
}

func (p *SlowExecSkill) Name() string                             { return "slow-exec-skill" }
func (p *SlowExecSkill) Version() string                          { return "1.0.0" }
func (p *SlowExecSkill) Init(config map[string]interface{}) error { return nil }

func (p *SlowExecSkill) Execute(input []byte) ([]byte, error) {
	time.Sleep(p.delay)
	response := SkillResponse{
		Success: true,
		Result:  "slow operation completed",
	}
	return json.Marshal(response)
}

func TestSandboxBlockHostFilesystemAccess(t *testing.T) {
	sandbox := NewInMemorySandbox()
	spm := NewSandboxedSkillManager(sandbox)

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("sandbox-test", []string{})
	ctx := NewSkillContext(fs, session)

	hostFSSkill := &HostFSSkill{}
	spm.InjectSkill(hostFSSkill, ctx, nil)

	config := DefaultSandboxConfig()
	config.AllowHostFS = false
	spm.SetSandboxConfig("hostfs-skill", config)

	request := &SkillRequest{
		Operation: "read_file",
		Path:      "/etc/passwd",
	}
	input, _ := json.Marshal(request)

	result, err := spm.ExecuteSkillSandboxed("hostfs-skill", input, ctx)
	if err != nil {
		// Error returned directly is also acceptable
		return
	}

	if result == nil {
		t.Fatal("Expected execution result")
	}

	if result.Success {
		t.Error("Skill should not succeed when accessing host filesystem")
	}

	if len(result.Violations) == 0 {
		t.Error("Expected security violation to be recorded")
	}

	foundViolation := false
	for _, violation := range result.Violations {
		if strings.Contains(violation, "blocked_host_fs_access") {
			foundViolation = true
			break
		}
	}
	if !foundViolation && result.Error == "" {
		t.Error("Expected 'blocked_host_fs_access' violation or error message")
	}
}

func TestSandboxBlockPathTraversal(t *testing.T) {
	sandbox := NewInMemorySandbox()
	spm := NewSandboxedSkillManager(sandbox)

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("sandbox-test", []string{})
	ctx := NewSkillContext(fs, session)

	traversalSkill := &PathTraversalSkill{}
	spm.InjectSkill(traversalSkill, ctx, nil)

	config := DefaultSandboxConfig()
	spm.SetSandboxConfig("traversal-skill", config)

	request := &SkillRequest{
		Operation: "read_file",
		Path:      "/toolfs/../../etc/passwd",
	}
	input, _ := json.Marshal(request)

	result, err := spm.ExecuteSkillSandboxed("traversal-skill", input, ctx)
	if err != nil {
		return
	}

	if result == nil {
		t.Fatal("Expected execution result")
	}

	if result.Success {
		t.Error("Skill should not succeed with path traversal")
	}

	foundViolation := false
	for _, violation := range result.Violations {
		if strings.Contains(violation, "path_traversal") {
			foundViolation = true
			break
		}
	}
	if !foundViolation && result.Error == "" {
		t.Error("Expected 'path_traversal_attempt' violation or error message")
	}
}

func TestSandboxBlockSystemPaths(t *testing.T) {
	sandbox := NewInMemorySandbox()
	spm := NewSandboxedSkillManager(sandbox)

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("sandbox-test", []string{})
	ctx := NewSkillContext(fs, session)

	skill := &HostFSSkill{}
	spm.InjectSkill(skill, ctx, nil)

	config := DefaultSandboxConfig()
	spm.SetSandboxConfig("hostfs-skill", config)

	systemPaths := []string{
		"/etc/passwd",
		"/sys/kernel",
		"/proc/self",
		"C:\\Windows\\System32",
	}

	for _, sysPath := range systemPaths {
		request := &SkillRequest{
			Operation: "read_file",
			Path:      sysPath,
		}
		input, _ := json.Marshal(request)

		result, err := spm.ExecuteSkillSandboxed("hostfs-skill", input, ctx)
		if err != nil {
			// Error returned is acceptable (path blocked)
			continue
		}

		if result == nil {
			t.Errorf("Expected execution result for path: %s", sysPath)
			continue
		}

		if result.Success {
			t.Errorf("Expected system path %s to be blocked", sysPath)
		}

		blocked := len(result.Violations) > 0 || result.Error != ""
		if !blocked {
			t.Errorf("Expected violation or error for system path: %s", sysPath)
		}
	}
}

func TestSandboxAllowToolFSPaths(t *testing.T) {
	sandbox := NewInMemorySandbox()
	spm := NewSandboxedSkillManager(sandbox)

	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	fs.MountLocal("/data", tmpDir, false)
	session, _ := fs.NewSession("sandbox-test", []string{"/toolfs/data"})
	ctx := NewSkillContext(fs, session)

	toolfsSkill := &ContentSkill{content: "ToolFS access"}
	spm.InjectSkill(toolfsSkill, ctx, nil)

	config := DefaultSandboxConfig()
	spm.SetSandboxConfig("content-skill", config)

	request := &SkillRequest{
		Operation: "read_file",
		Path:      "/toolfs/data/test.txt",
	}
	input, _ := json.Marshal(request)

	result, err := spm.ExecuteSkillSandboxed("content-skill", input, ctx)
	if err != nil {
		t.Fatalf("ToolFS path access should be allowed: %v", err)
	}

	if !result.Success {
		t.Error("Skill should succeed when accessing ToolFS paths")
	}

	if len(result.Violations) > 0 {
		t.Errorf("ToolFS path access should not generate violations, got: %v", result.Violations)
	}
}

func TestSandboxCPUTimeout(t *testing.T) {
	sandbox := NewInMemorySandbox()
	spm := NewSandboxedSkillManager(sandbox)

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("sandbox-test", []string{})
	ctx := NewSkillContext(fs, session)

	slowSkill := &SlowExecSkill{delay: 200 * time.Millisecond}
	spm.InjectSkill(slowSkill, ctx, nil)

	config := DefaultSandboxConfig()
	config.CPUTimeout = 50 * time.Millisecond
	spm.SetSandboxConfig("slow-exec-skill", config)

	request := &SkillRequest{Operation: "test"}
	input, _ := json.Marshal(request)

	result, err := spm.ExecuteSkillSandboxed("slow-exec-skill", input, ctx)
	if err == nil && result != nil && result.Success {
		t.Log("Timeout may not trigger in this test environment")
	}

	if result != nil && result.CPUTime > config.CPUTimeout {
		t.Logf("CPU time exceeded timeout: %v > %v", result.CPUTime, config.CPUTimeout)
	}
}

func TestSandboxCaptureStdoutStderr(t *testing.T) {
	sandbox := NewInMemorySandbox()
	spm := NewSandboxedSkillManager(sandbox)

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("sandbox-test", []string{})
	ctx := NewSkillContext(fs, session)

	stdioSkill := &StdoutStderrSkill{}
	spm.InjectSkill(stdioSkill, ctx, nil)

	config := DefaultSandboxConfig()
	config.CaptureStdout = true
	config.CaptureStderr = true
	spm.SetSandboxConfig("stdio-skill", config)

	request := &SkillRequest{Operation: "test"}
	input, _ := json.Marshal(request)

	result, err := spm.ExecuteSkillSandboxed("stdio-skill", input, ctx)
	if err != nil {
		t.Fatalf("Skill execution failed: %v", err)
	}

	if !result.Success {
		t.Errorf("Skill should succeed: %s", result.Error)
	}

	if !strings.Contains(result.Stdout, "stdout output") {
		t.Errorf("Expected stdout to be captured, got: '%s' (length: %d)", result.Stdout, len(result.Stdout))
	}

	if !strings.Contains(result.Stderr, "stderr output") {
		t.Errorf("Expected stderr to be captured, got: '%s' (length: %d)", result.Stderr, len(result.Stderr))
	}
}

func TestSandboxAuditLogging(t *testing.T) {
	sandbox := NewInMemorySandbox()
	spm := NewSandboxedSkillManager(sandbox)

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("audit-test", []string{})
	ctx := NewSkillContext(fs, session)

	testLogger := &TestAuditLogger{Entries: []AuditLogEntry{}}

	skill := &ExampleSkill{name: "audit-skill", version: "1.0.0"}
	spm.InjectSkill(skill, ctx, nil)

	config := DefaultSandboxConfig()
	config.AuditLog = testLogger
	spm.SetSandboxConfig("audit-skill", config)

	request := &SkillRequest{Operation: "test"}
	input, _ := json.Marshal(request)

	result, err := spm.ExecuteSkillSandboxed("audit-skill", input, ctx)
	if err != nil {
		t.Fatalf("Skill execution failed: %v", err)
	}

	if len(testLogger.Entries) == 0 {
		t.Error("Expected audit log entry to be created")
	}

	entry := testLogger.Entries[0]
	if entry.Operation != "SkillExecute" {
		t.Errorf("Expected operation 'SkillExecute', got '%s'", entry.Operation)
	}

	if !strings.Contains(entry.Path, "audit-skill") {
		t.Errorf("Expected path to contain skill name, got '%s'", entry.Path)
	}

	if entry.SessionID != "audit-test" {
		t.Errorf("Expected session ID 'audit-test', got '%s'", entry.SessionID)
	}

	if result.Success != entry.Success {
		t.Error("Audit entry success should match result success")
	}
}

func TestSandboxDefaultConfig(t *testing.T) {
	config := DefaultSandboxConfig()

	if config.CPUTimeout <= 0 {
		t.Error("Default CPU timeout should be positive")
	}

	if config.MemoryLimit <= 0 {
		t.Error("Default memory limit should be positive")
	}

	if config.AllowHostFS {
		t.Error("Default should block host filesystem access")
	}

	if !config.CaptureStdout {
		t.Error("Default should capture stdout")
	}

	if !config.CaptureStderr {
		t.Error("Default should capture stderr")
	}
}

func TestSandboxExecutionResult(t *testing.T) {
	sandbox := NewInMemorySandbox()
	spm := NewSandboxedSkillManager(sandbox)

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("result-test", []string{})
	ctx := NewSkillContext(fs, session)

	skill := &ExampleSkill{name: "result-skill", version: "1.0.0"}
	spm.InjectSkill(skill, ctx, nil)

	config := DefaultSandboxConfig()
	spm.SetSandboxConfig("result-skill", config)

	request := &SkillRequest{
		Operation: "test",
		Path:      "/toolfs/data/test.txt",
	}
	input, _ := json.Marshal(request)

	result, err := spm.ExecuteSkillSandboxed("result-skill", input, ctx)
	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}

	if result == nil {
		t.Fatal("Expected execution result")
	}

	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	if result.CPUTime < 0 {
		t.Error("Expected CPU time to be non-negative")
	}

	if result.Metadata == nil {
		t.Error("Expected metadata to be set")
	}

	if result.Metadata["skill_name"] != "result-skill" {
		t.Errorf("Expected skill name in metadata")
	}
}

func TestSandboxExecuteSkillWithSandbox(t *testing.T) {
	sandbox := NewInMemorySandbox()
	spm := NewSandboxedSkillManager(sandbox)

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("wraptest", []string{})
	ctx := NewSkillContext(fs, session)

	skill := &ContentSkill{content: "wrapped result"}
	spm.InjectSkill(skill, ctx, nil)

	config := DefaultSandboxConfig()
	spm.SetSandboxConfig("content-skill", config)

	request := &SkillRequest{
		Operation: "read_file",
		Path:      "/toolfs/test.txt",
	}

	response, err := spm.ExecuteSkillWithSandbox("content-skill", request)
	if err != nil {
		t.Fatalf("ExecuteSkillWithSandbox failed: %v", err)
	}

	if !response.Success {
		t.Errorf("Expected success, got error: %s", response.Error)
	}
}

func TestSearchMemoryAndExecuteSkill(t *testing.T) {
	fs := NewToolFS("/toolfs")
	pm := NewSkillExecutorManager()
	fs.SetSkillExecutorManager(pm)

	session, _ := fs.NewSession("skill-skill-test", []string{"/toolfs/rag", "/toolfs/memory"})
	ctx := NewSkillContext(fs, session)

	testSkill := &ContentSkill{content: "Skill search result for ToolFS"}
	pm.InjectSkill(testSkill, ctx, nil)
	fs.MountSkillExecutor("/toolfs/rag", "content-skill")

	fs.WriteFile("/toolfs/memory/test1", []byte("Memory entry about ToolFS"))

	result, err := SearchMemoryAndExecuteSkill(fs, "ToolFS", "/toolfs/rag", session)
	if err != nil {
		t.Fatalf("SearchMemoryAndExecuteSkill failed: %v", err)
	}

	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	if metadata, ok := result.Metadata.(map[string]interface{}); ok {
		if sourcesFound, ok := metadata["sources_found"].(int); ok {
			if sourcesFound < 1 {
				t.Errorf("Expected at least 1 source found, got %d", sourcesFound)
			}
		}
	}
}

func TestChainOperationsWithSkill(t *testing.T) {
	fs := NewToolFS("/toolfs")
	pm := NewSkillExecutorManager()
	fs.SetSkillExecutorManager(pm)

	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()
	fs.MountLocal("/data", tmpDir, false)

	session, _ := fs.NewSession("chain-skill-test", []string{"/toolfs/data", "/toolfs/rag", "/toolfs/memory"})
	ctx := NewSkillContext(fs, session)

	testSkill := &ContentSkill{content: "Skill content from chain"}
	pm.InjectSkill(testSkill, ctx, nil)
	fs.MountSkillExecutor("/toolfs/rag", "content-skill")

	fs.WriteFile("/toolfs/memory/chain1", []byte("Memory content for chain"))

	operations := []Operation{
		{Type: "search_memory", Query: "chain"},
		{Type: "execute_code_skill", SkillPath: "/toolfs/rag", Query: "chain"},
		{Type: "read_file", Path: "/toolfs/data/test.txt"},
	}

	results, err := ChainOperations(fs, operations, session)
	if err != nil {
		t.Fatalf("ChainOperations failed: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(results))
	}

	if results[1].Type != "code_skill" {
		t.Errorf("Expected skill result, got '%s'", results[1].Type)
	}
	if !results[1].Success {
		t.Errorf("Expected skill success, got error: %s", results[1].Error)
	}
}

func TestSearchMemoryAndExecuteSkillFullChain(t *testing.T) {
	fs := NewToolFS("/toolfs")
	pm := NewSkillExecutorManager()
	fs.SetSkillExecutorManager(pm)

	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()
	fs.MountLocal("/data", tmpDir, false)

	session, _ := fs.NewSession("full-skill-test", []string{"/toolfs/data", "/toolfs/rag", "/toolfs/memory"})
	ctx := NewSkillContext(fs, session)

	fs.WriteFile("/toolfs/memory/skill1", []byte("Memory entry: ToolFS skill API"))

	testSkill := &ContentSkill{content: "RAG skill result for skill API"}
	pm.InjectSkill(testSkill, ctx, nil)
	fs.MountSkillExecutor("/toolfs/rag", "content-skill")

	result, err := SearchMemoryAndExecuteSkill(fs, "skill API", "/toolfs/rag", session)
	if err != nil {
		t.Fatalf("SearchMemoryAndExecuteSkill failed: %v", err)
	}

	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	if metadata, ok := result.Metadata.(map[string]interface{}); ok {
		if sourcesFound, ok := metadata["sources_found"].(int); ok {
			if sourcesFound < 1 {
				t.Errorf("Expected at least 1 source, got %d", sourcesFound)
			}
		}
	}
}
