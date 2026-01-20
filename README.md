<div align="center">
  <h1>🗃ToolFS</h1>
  <p><strong>The virtual filesystem for AI agents.</strong></p>
  
  <p>
    <a href="README.md">English</a> | 
    <a href="README_zh.md">中文</a>
  </p>
</div>

---

**ToolFS** is an open-source virtual filesystem framework designed for large language model (LLM) agents in Go. Just as traditional filesystems provide file and directory abstractions for applications, ToolFS provides the storage abstractions that AI agents need: files, memory, RAG, skills, and snapshots—all through a unified `/toolfs` namespace.

## 🎯 What is ToolFS?

ToolFS is a virtual filesystem framework that provides a unified interface for LLM agents to interact with:
- **Local Filesystem**: Access mounted local directories with permission control
- **Memory Store**: Persistent key-value storage for session data and context
- **RAG System**: Semantic search over vector databases for document retrieval
- **Skill System**: Execute WASM-based skills mounted to virtual paths
- **Snapshot Management**: Create point-in-time snapshots and restore previous states

![ToolFS Architecture](assets/toolfs%20internal%20arch.png)

*ToolFS Internal Architecture*

## 💡 Why ToolFS?

ToolFS provides the following benefits for AI agent development:

- **Unified Interface**: Single `/toolfs` namespace for all storage operations—files, memory, RAG, and skills
- **Session Isolation**: Each agent session has isolated access with configurable permissions
- **Auditability**: All operations are logged with session tracking for security and debugging
- **Reproducibility**: Snapshot filesystem state at any point and restore later to reproduce exact execution states
- **Extensibility**: Skill system allows extending functionality with WASM modules
- **Offline Capable**: Works fully offline without external dependencies
- **Sandbox Ready**: Designed for safe execution in sandboxed environments

## 🚀 Quick Start

### Installation

```bash
# Clone the repository
git clone https://github.com/IceWhaleTech/toolfs.git
cd toolfs

# Download Go module dependencies
go mod tidy
```

### Basic Usage

```go
package main

import (
    "fmt"
    "github.com/IceWhaleTech/toolfs"
)

func main() {
    // Initialize ToolFS
    fs := toolfs.NewToolFS("/toolfs")

    // Mount a local directory (read-only)
    err := fs.MountLocal("/project_data", "./data", true)
    if err != nil {
        panic(err)
    }

    // Read a file
    content, err := fs.ReadFile("/project_data/example.txt")
    if err != nil {
        panic(err)
    }
    fmt.Println(string(content))

    // Write to Memory
    err = fs.WriteFile("/toolfs/memory/meeting_notes", []byte("Discuss AI agent roadmap"))
    if err != nil {
        panic(err)
    }

    // Perform RAG search
    results, err := fs.ReadFile("/toolfs/rag/query?text=AI+agent&top_k=3")
    if err != nil {
        panic(err)
    }
    fmt.Println(results)
}
```

## 🔧 How ToolFS Works

ToolFS provides three essential interfaces for agent state management:

1. **Filesystem Interface**: POSIX-like filesystem for files and directories from mounted local paths
2. **Memory Interface**: Key-value store for persistent session data and context
3. **RAG Interface**: Semantic search over vector databases for document retrieval

All operations go through the unified `/toolfs` namespace, providing a consistent API regardless of the underlying storage type.

![Agent Architecture](assets/agent%20arch.png)

*Agent Architecture with ToolFS*

## ⚡ Performance

ToolFS is optimized for high-performance agent workloads with the following performance characteristics:

### Core Operations

| Operation | Throughput | Latency | Memory | Allocs/Op |
|-----------|-----------|---------|--------|-----------|
| **Memory Write** | 1,000,000 ops/s | 2.07 μs | 680 B | 13 |
| **Memory Read** | 883,665 ops/s | 2.83 μs | 304 B | 6 |
| **Memory List** | 401,068 ops/s | 6.20 μs | 16,408 B | 2 |
| **Path Resolution** | 14,034,529 ops/s | 75.96 ns | 0 B | 0 |
| **File Read** (1KB) | 61,449 ops/s | 56.11 μs | 2,072 B | 5 |
| **RAG Search** | 465,460 ops/s | 7.28 μs | 1,498 B | 28 |

### Performance Highlights

- **Memory operations**: Sub-microsecond latency with >1M ops/s throughput
- **Path resolution caching**: Near-zero overhead (0 allocations) for cached paths
- **Optimized virtual paths**: Pre-computed and cached for maximum efficiency
- **Copy-on-write snapshots**: Efficient state management with minimal overhead

## ⚡ Performance Testing (Detailed)

ToolFS provides a comprehensive performance benchmark suite for evaluating the performance of various features.

### Running Benchmarks

```bash
# Run all benchmarks
go test -bench=. -benchmem ./...

# Run specific benchmarks
go test -bench=BenchmarkReadFile -benchmem ./...

# Run benchmarks and generate performance report (Linux/macOS)
bash scripts/benchmark_report.sh

# Run benchmarks and generate performance report (Windows)
scripts\benchmark_report.bat
```

### Performance Test Coverage

Benchmarks include the following operations:

- **File Operations**: File read/write, directory listing, large file handling
- **Memory Storage**: Memory write, read, list operations
- **RAG Search**: Semantic search performance, TopK parameter impact
- **Snapshot Operations**: Snapshot creation, rollback performance
- **Skill Execution**: Skill invocation overhead
- **Concurrency Performance**: Multi-goroutine concurrent access performance
- **Session Control**: Access control overhead

### Performance Data Overview

Performance data based on actual testing (Test environment: Windows amd64, Intel Xeon E5-2690 v2):

#### File Operation Performance

| Operation | Throughput | Avg Latency | Notes |
|-----------|-----------|-------------|-------|
| File Read (1KB) | 60K ops/s | 58 μs | Excellent small file read performance |
| File Read (100KB) | 31K ops/s | 119 μs | Good large file read performance |
| File Write (1KB) | 3.2K ops/s | 1.06 ms | Write performance limited by filesystem I/O |
| Directory List | 22K ops/s | 160 μs | Suitable for frequent directory traversal |

#### Memory Storage Performance

| Operation | Throughput | Avg Latency | Notes |
|-----------|-----------|-------------|-------|
| Memory Write | **1.0M ops/s** | 2.07 μs | Extremely fast memory operations (optimized) |
| Memory Read | **883K ops/s** | 2.83 μs | Excellent retrieval performance |
| Memory List (1000 items) | **401K ops/s** | 6.20 μs | 75% performance improvement (optimized) |

#### RAG Search Performance

| Operation | Throughput | Avg Latency | Notes |
|-----------|-----------|-------------|-------|
| RAG Search | 465K ops/s | 7.28 μs | Excellent search performance |
| RAG Search (TopK) | 421K ops/s | 8.71 μs | TopK parameter has minimal impact |

#### Snapshot Operation Performance

| Operation | Throughput | Avg Latency | Notes |
|-----------|-----------|-------------|-------|
| Snapshot Create | 570 ops/s | 4.15 ms | Requires scanning and copying files |
| Snapshot Rollback | 672 ops/s | 3.70 ms | Rollback slightly faster than creation |

#### Concurrency Performance

| Operation | Throughput | Avg Latency | Notes |
|-----------|-----------|-------------|-------|
| Concurrent Read | 91K ops/s | 37.9 μs | Good performance in multi-goroutine environment |

### Performance Highlights

1. **Memory Operations**: Memory storage reaches **1.3M ops/s**, approximately 400x faster than file I/O
2. **File Reading**: Small file read performance reaches **60K ops/s** with only 58μs latency
3. **RAG Search**: Search performance reaches **465K ops/s**, suitable for real-time queries
4. **Concurrency Support**: Excellent multi-goroutine concurrent read performance

## 📚 Documentation & Examples

For detailed usage examples and advanced features, check out:

- **[Skills Documentation](skills/SKILL.md)** - Complete guide to ToolFS skills and modules
  - [Memory Skill](skills/memory/SKILL.md) - Persistent storage operations
  - [RAG Skill](skills/rag/SKILL.md) - Semantic search operations
  - [Filesystem Skill](skills/filesystem/SKILL.md) - File and directory operations
  - [Skill Skill](skills/skill/SKILL.md) - Skill execution and management
  - [Snapshot Skill](skills/snapshot/SKILL.md) - State management operations
- **[Examples](examples/)** - Working code examples and integrations
  - [RAG Skill Example](examples/rag_skill/README.md) - Complete RAG skill implementation

## 🤔 FAQ

### How is ToolFS different from traditional filesystems?

Traditional filesystems provide file and directory abstractions. ToolFS extends this with agent-specific abstractions: memory storage, RAG search, skill execution, and snapshot management—all through a unified interface optimized for LLM agent workflows.

### Can I use ToolFS with containers or VMs?

Yes! ToolFS is designed to work in any environment, including containers, VMs, and sandboxes. It provides session isolation and permission control suitable for multi-tenant deployments.

### How does ToolFS compare to AgentFS?

ToolFS and AgentFS address similar problems but with different approaches:
- **AgentFS**: SQLite-based filesystem with audit trails, designed for agent state management
- **ToolFS**: Virtual filesystem with unified interface for files, memory, RAG, and skills, designed for extensibility and skill architecture

Both can be used together: AgentFS for structured state management, ToolFS for unified storage abstractions and skill execution.

### Is ToolFS production-ready?

ToolFS is actively developed. Check the roadmap below for current development status. Use in production environments should be done with appropriate testing and validation.

## 🗺️ Roadmap

- ✅ Phase 1: Basic FS API (read/write/list)
- ✅ Phase 2: Memory / RAG integration
- ✅ Phase 3: Security & permission control
- ✅ Phase 4: Skill API / Tool Using integration
- ✅ Phase 5: Snapshot / rollback support
- 🔄 Phase 6: Production hardening and performance optimization
- 📋 Phase 7: Additional skill ecosystem and integrations

## 🙏 Inspired By

ToolFS is inspired by the filesystem-based agent architecture pattern, which leverages LLMs' native understanding of filesystems and Unix tools. This approach has been proven effective in production systems:

- **[Vercel: How to build agents with filesystems and bash](https://vercel.com/blog/how-to-build-agents-with-filesystems-and-bash)** - Demonstrates how replacing custom tooling with filesystem and bash tools reduced costs by 75% and improved output quality
- **[FUSE is All You Need](https://jakobemmerling.de/posts/fuse-is-all-you-need/)** - Explores using FUSE to give agents access to anything via virtual filesystems
- **[Vercel: We removed 80% of our agents' tools](https://vercel.com/blog/we-removed-80-percent-of-our-agents-tools)** - Case study on simplifying agent architecture with filesystem-based patterns

The core insight is that LLMs excel at filesystem operations because they've been trained on massive amounts of code. By structuring agent context as files and providing filesystem access, agents can leverage their native capabilities without custom tool design.

---

<div align="center">
  <p>Made with ❤️ for the AI agent community</p>
  <p>
    <a href="README.md">English</a> | 
    <a href="README_zh.md">中文</a>
  </p>
</div>
