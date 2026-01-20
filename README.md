<div align="center">
  <h1>🗃ToolFS</h1>
  <p><strong>The standard virtual filesystem for AI agents.</strong></p>
  
  <p>
    <a href="README.md">English</a> | 
    <a href="README_zh.md">中文</a>
  </p>
</div>

---

**ToolFS** is a specialized virtual filesystem framework designed for Large Language Model (LLM) agents. It unifies disparate interfaces—files, persistent memory, semantic search (RAG), and code execution (WASM skills)—into a single, POSIX-compliant `/toolfs` namespace.

By mapping complex state and capabilities to filesystem operations, ToolFS leverages the LLM's inherent understanding of path structures and file manipulation, significantly reducing the complexity of tool integration.

## 💡 Why ToolFS?

Current AI agent architectures often suffer from "tool bloat," where managing dozens of disparate APIs becomes a bottleneck. ToolFS solves this by providing:
- **Natural Abstraction**: LLMs inherently understand files and directories. Mapping tools to paths simplifies intent recognition.
- **Unified State**: Session history, knowledge bases, and local files share a single lifecycle.
- **Agentic Autonomy**: Context-aware skill documentation allows agents to discover and chain tools without hardcoded logic.

## 🎯 Key Capabilities

- **Unified Namespace**: A single entry point (`/toolfs`) for files, session-bounded memory, vector-based RAG queries, and autonomous skills.
- **Unified Skill API**: Register and execute WASM-based or native skills with **context-aware documentation** that helps agents understand *when* and *how* to use them.
- **Session-Bounded Security**: Fine-grained path-based permissions and isolated environments for multi-tenant agent deployments.
- **Atomic Snapshots**: Create instant, copy-on-write snapshots of the entire agent environment for rollback, debugging, and perfect reproducibility.
- **Audit-Ready**: Transparent JSON-based audit logging for every operation, ensuring compliance and observability.

## 🛠 Architecture

ToolFS acts as an abstraction layer between the Agent and its environment:

![ToolFS Architecture](assets/toolfs%20internal%20arch.png)

*ToolFS Internal Architecture*

```text
[ Agent ] <──> [ /toolfs Virtual Path ] <──> [ ToolFS Core ]
                                                     │
               ┌──────────────┬──────────────┬───────┴──────┬──────────────┐
               ▼              ▼              ▼              ▼              ▼
         [ Local FS ]   [ Memory KV ]   [ RAG Store ]   [ WASM Skills ] [ Snapshots ]
```

## 🚀 Quick Start

### 1. Installation

```bash
go get github.com/IceWhaleTech/toolfs
```

### 2. Integration Example

Combine memory, RAG, and file access in a few lines:

```go
package main

import (
    "github.com/IceWhaleTech/toolfs"
)

func main() {
    // Initialize with a root mount point
    fs := toolfs.NewToolFS("/toolfs")

    // Isolated session with path-level permissions
    session, _ := fs.NewSession("agent-007", []string{"/toolfs/data", "/toolfs/memory", "/toolfs/rag"})

    // Persistent Context (Memory)
    fs.WriteFileWithSession("/toolfs/memory/last_query", []byte("How to build an agent?"), session)

    // Semantic Retrieval (RAG)
    // Simply read a virtual path!
    results, _ := fs.ReadFileWithSession("/toolfs/rag/query?text=agent+design&top_k=3", session)
    
    // Skill Execution
    // Chains multiple operations: search memory -> execute skill -> save result
    ops := []toolfs.Operation{
        {Type: "search_memory", Query: "preferences"},
        {Type: "execute_code_skill", SkillPath: "/toolfs/skills/processor"},
    }
    fs.ChainOperations(ops, session)
}
```

## ⚡ Performance

Optimized for high-frequency agent loops. *Tested on Intel Xeon E5-2690 v2.*

| Operation | Throughput | Latency | Overhead |
|-----------|-----------|---------|----------|
| **Memory Access** | **1,000,000+ ops/s** | ~2 μs | 0 allocations |
| **Path Resolution**| **14,000,000+ ops/s**| <100 ns | Cache-driven |
| **RAG Search** | **460,000+ ops/s** | ~7 μs | Highly efficient |
| **File I/O (1KB)** | **60,000+ ops/s** | ~50 μs | Local-first |

## 🧩 ToolFS vs. AgentFS

While both focus on agent state, they serve different primary roles:

| Feature | ToolFS | AgentFS |
|---------|--------|---------|
| **Primary Goal** | Unified Tool/Storage Abstraction | Structured State & Audit Trails |
| **Storage Engine** | Virtual Layer (File, Memory, RAG) | SQLite-backed |
| **Tool Execution** | Native & WASM Skills (Unified API) | Focus on CRUD of state |
| **Audit Model** | Per-path/Per-session logs | Transactional SQL logs |

**Intersection:** Both can be used together—AgentFS for deep structured memory, and ToolFS for providing a standard filesystem-like API to that memory alongside other tools.

## 🙏 Inspirations

ToolFS is inspired by the pattern of using filesystems as the primary interface for autonomous agents:
- **[How to Build Agents with Filesystems and Bash](https://vercel.com/blog/how-to-build-agents-with-filesystems-and-bash)**: Demonstrates 75% cost reduction by replacing custom tools with standard FS patterns.
- **[FUSE is All You Need](https://jakobemmerling.de/posts/fuse-is-all-you-need/)**: Exploring virtual filesystems for agent tool access.
- **[We Removed 80% of our Agents' Tools](https://vercel.com/blog/we-removed-80-percent-of-our-agents-tools)**: Case study on radical simplification via filesystem-based tool design.

## 📚 Documentation

- **[Unified Skill System](skills/SKILL.md)**: Deep dive into the "Skill is Skill" architecture.
- **[RAG Implementation Guide](examples/rag_skill/README.md)**: Building custom semantic search skills.
- **[Go API Reference](https://pkg.go.dev/github.com/IceWhaleTech/toolfs)**: Detailed package documentation.

---

<div align="center">
  <p>Built for the future of Autonomous Agents.</p>
</div>
