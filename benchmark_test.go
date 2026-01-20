package toolfs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setupBenchmarkDir creates a test directory for benchmarking
func setupBenchmarkDir(b *testing.B) string {
	tmpDir, err := os.MkdirTemp("", "toolfs_bench_*")
	if err != nil {
		b.Fatalf("Failed to create temp dir: %v", err)
	}

	// Create test files of various sizes
	sizes := []int{1024, 10240, 102400} // 1KB, 10KB, 100KB
	for i, size := range sizes {
		testFile := filepath.Join(tmpDir, fmt.Sprintf("test%d.txt", i))
		data := make([]byte, size)
		for j := range data {
			data[j] = byte(j % 256)
		}
		if err := os.WriteFile(testFile, data, 0644); err != nil {
			b.Fatalf("Failed to create test file: %v", err)
		}
	}

	// Create subdirectories
	for i := 0; i < 10; i++ {
		subDir := filepath.Join(tmpDir, fmt.Sprintf("subdir%d", i))
		if err := os.Mkdir(subDir, 0755); err != nil {
			b.Fatalf("Failed to create subdirectory: %v", err)
		}
	}

	return tmpDir
}

// BenchmarkReadFile benchmarks file reading performance
func BenchmarkReadFile(b *testing.B) {
	fs := NewToolFS("/toolfs")
	tmpDir := setupBenchmarkDir(b)
	defer os.RemoveAll(tmpDir)

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		b.Fatalf("MountLocal failed: %v", err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := fs.ReadFile("/toolfs/data/test0.txt")
			if err != nil {
				b.Fatalf("ReadFile failed: %v", err)
			}
		}
	})
}

// BenchmarkReadFileLarge benchmarks reading large files
func BenchmarkReadFileLarge(b *testing.B) {
	fs := NewToolFS("/toolfs")
	tmpDir := setupBenchmarkDir(b)
	defer os.RemoveAll(tmpDir)

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		b.Fatalf("MountLocal failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := fs.ReadFile("/toolfs/data/test2.txt") // 100KB file
		if err != nil {
			b.Fatalf("ReadFile failed: %v", err)
		}
	}
}

// BenchmarkWriteFile benchmarks file writing performance
func BenchmarkWriteFile(b *testing.B) {
	fs := NewToolFS("/toolfs")
	tmpDir := setupBenchmarkDir(b)
	defer os.RemoveAll(tmpDir)

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		b.Fatalf("MountLocal failed: %v", err)
	}

	testData := make([]byte, 1024)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := fmt.Sprintf("/toolfs/data/bench%d.txt", i)
		err := fs.WriteFile(path, testData)
		if err != nil {
			b.Fatalf("WriteFile failed: %v", err)
		}
	}
}

// BenchmarkListDir benchmarks directory listing performance
func BenchmarkListDir(b *testing.B) {
	fs := NewToolFS("/toolfs")
	tmpDir := setupBenchmarkDir(b)
	defer os.RemoveAll(tmpDir)

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		b.Fatalf("MountLocal failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := fs.ListDir("/toolfs/data")
		if err != nil {
			b.Fatalf("ListDir failed: %v", err)
		}
	}
}

// BenchmarkMemoryWrite benchmarks memory store write performance
func BenchmarkMemoryWrite(b *testing.B) {
	fs := NewToolFS("/toolfs")

	testData := []byte("This is a test memory entry")
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		path := fmt.Sprintf("/toolfs/memory/entry%d", i)
		err := fs.WriteFile(path, testData)
		if err != nil {
			b.Fatalf("Memory write failed: %v", err)
		}
	}
}

// BenchmarkMemoryRead benchmarks memory store read performance
func BenchmarkMemoryRead(b *testing.B) {
	fs := NewToolFS("/toolfs")

	// Pre-populate memory entries
	for i := 0; i < 100; i++ {
		path := fmt.Sprintf("/toolfs/memory/entry%d", i)
		fs.WriteFile(path, []byte(fmt.Sprintf("Memory entry %d", i)))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := fmt.Sprintf("/toolfs/memory/entry%d", i%100)
		_, err := fs.ReadFile(path)
		if err != nil {
			b.Fatalf("Memory read failed: %v", err)
		}
	}
}

// BenchmarkRAGSearch benchmarks RAG search performance
func BenchmarkRAGSearch(b *testing.B) {
	fs := NewToolFS("/toolfs")

	queries := []string{"AI agent", "memory system", "RAG search", "ToolFS", "semantic search"}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		query := queries[i%len(queries)]
		path := fmt.Sprintf("/toolfs/rag/query?text=%s&top_k=3", query)
		_, err := fs.ReadFile(path)
		if err != nil {
			b.Fatalf("RAG search failed: %v", err)
		}
	}
}

// BenchmarkRAGSearchWithTopK benchmarks RAG search with different top_k values
func BenchmarkRAGSearchWithTopK(b *testing.B) {
	fs := NewToolFS("/toolfs")

	topKs := []int{1, 3, 5, 10}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		topK := topKs[i%len(topKs)]
		path := fmt.Sprintf("/toolfs/rag/query?text=AI+agent&top_k=%d", topK)
		_, err := fs.ReadFile(path)
		if err != nil {
			b.Fatalf("RAG search failed: %v", err)
		}
	}
}

// BenchmarkSnapshotCreate benchmarks snapshot creation performance
func BenchmarkSnapshotCreate(b *testing.B) {
	fs := NewToolFS("/toolfs")
	tmpDir := setupBenchmarkDir(b)
	defer os.RemoveAll(tmpDir)

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		b.Fatalf("MountLocal failed: %v", err)
	}

	// Create some test files
	for i := 0; i < 10; i++ {
		path := fmt.Sprintf("/toolfs/data/file%d.txt", i)
		fs.WriteFile(path, []byte(fmt.Sprintf("Content of file %d", i)))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snapshotName := fmt.Sprintf("snapshot%d", i)
		err := fs.CreateSnapshot(snapshotName)
		if err != nil {
			b.Fatalf("CreateSnapshot failed: %v", err)
		}
	}
}

// BenchmarkSnapshotRollback benchmarks snapshot rollback performance
func BenchmarkSnapshotRollback(b *testing.B) {
	fs := NewToolFS("/toolfs")
	tmpDir := setupBenchmarkDir(b)
	defer os.RemoveAll(tmpDir)

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		b.Fatalf("MountLocal failed: %v", err)
	}

	// Create initial file
	fs.WriteFile("/toolfs/data/test.txt", []byte("Initial content"))

	// Create snapshot
	err = fs.CreateSnapshot("baseline")
	if err != nil {
		b.Fatalf("CreateSnapshot failed: %v", err)
	}

	// Modify file
	fs.WriteFile("/toolfs/data/test.txt", []byte("Modified content"))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := fs.RollbackSnapshot("baseline")
		if err != nil {
			b.Fatalf("RollbackSnapshot failed: %v", err)
		}
		// Modify again for next iteration
		fs.WriteFile("/toolfs/data/test.txt", []byte("Modified content"))
	}
}

// BenchmarkPathResolution benchmarks path resolution performance
func BenchmarkPathResolution(b *testing.B) {
	fs := NewToolFS("/toolfs")
	tmpDir1 := setupBenchmarkDir(b)
	defer os.RemoveAll(tmpDir1)

	tmpDir2 := setupBenchmarkDir(b)
	defer os.RemoveAll(tmpDir2)

	fs.MountLocal("/data1", tmpDir1, false)
	fs.MountLocal("/data2", tmpDir2, false)

	paths := []string{
		"/toolfs/data1/test0.txt",
		"/toolfs/data2/test0.txt",
		"/toolfs/data1/test1.txt",
		"/toolfs/data2/test1.txt",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := paths[i%len(paths)]
		_, _, err := fs.resolvePath(path)
		if err != nil {
			b.Fatalf("resolvePath failed: %v", err)
		}
	}
}

// BenchmarkSkillExecute benchmarks skill execution performance
func BenchmarkSkillExecute(b *testing.B) {
	fs := NewToolFS("/toolfs")
	pm := NewSkillExecutorManager()
	fs.SetSkillExecutorManager(pm)

	session, _ := fs.NewSession("skill-bench", []string{"/toolfs/rag"})
	ctx := NewSkillContext(fs, session)

	contentSkill := &ContentSkill{content: "Skill response content"}
	pm.InjectSkill(contentSkill, ctx, nil)
	fs.MountSkillExecutor("/toolfs/rag", "content-skill")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := fmt.Sprintf("/toolfs/rag/test%d", i)
		_, err := fs.ReadFile(path)
		if err != nil {
			b.Fatalf("Skill read failed: %v", err)
		}
	}
}

// BenchmarkSessionAccessControl benchmarks session-based access control overhead
func BenchmarkSessionAccessControl(b *testing.B) {
	fs := NewToolFS("/toolfs")
	tmpDir := setupBenchmarkDir(b)
	defer os.RemoveAll(tmpDir)

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		b.Fatalf("MountLocal failed: %v", err)
	}

	session, err := fs.NewSession("session-bench", []string{"/toolfs/data"})
	if err != nil {
		b.Fatalf("NewSession failed: %v", err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := fs.ReadFileWithSession("/toolfs/data/test0.txt", session)
			if err != nil {
				b.Fatalf("ReadFileWithSession failed: %v", err)
			}
		}
	})
}

// BenchmarkConcurrentReads benchmarks concurrent read operations
func BenchmarkConcurrentReads(b *testing.B) {
	fs := NewToolFS("/toolfs")
	tmpDir := setupBenchmarkDir(b)
	defer os.RemoveAll(tmpDir)

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		b.Fatalf("MountLocal failed: %v", err)
	}

	files := []string{
		"/toolfs/data/test0.txt",
		"/toolfs/data/test1.txt",
		"/toolfs/data/test2.txt",
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			path := files[i%len(files)]
			_, err := fs.ReadFile(path)
			if err != nil {
				b.Fatalf("ReadFile failed: %v", err)
			}
			i++
		}
	})
}

// BenchmarkMemoryJSONSerialization benchmarks JSON serialization for memory entries
func BenchmarkMemoryJSONSerialization(b *testing.B) {
	entry := &MemoryEntry{
		ID:        "test-entry",
		Content:   "This is test content for benchmarking JSON serialization",
		CreatedAt: getTimeNow(),
		UpdatedAt: getTimeNow(),
		Metadata: map[string]interface{}{
			"author": "Test Author",
			"tags":   []string{"test", "benchmark", "json"},
			"score":  0.95,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := json.Marshal(entry)
		if err != nil {
			b.Fatalf("JSON marshal failed: %v", err)
		}
	}
}

// BenchmarkMemoryList benchmarks memory entry listing performance
func BenchmarkMemoryList(b *testing.B) {
	fs := NewToolFS("/toolfs")

	// Pre-populate memory entries
	for i := 0; i < 1000; i++ {
		path := fmt.Sprintf("/toolfs/memory/entry%d", i)
		fs.WriteFile(path, []byte(fmt.Sprintf("Memory entry %d", i)))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := fs.ListDir("/toolfs/memory")
		if err != nil {
			b.Fatalf("Memory list failed: %v", err)
		}
	}
}

// Helper function to get current time (abstracted for testing)
func getTimeNow() time.Time {
	return time.Now()
}

