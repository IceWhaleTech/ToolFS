<div align="center">
  <h1>🗃ToolFS</h1>
  <p><strong>大语言模型智能体的标准虚拟文件系统。</strong></p>
  
  <p>
    <a href="README.md">English</a> | 
    <a href="README_zh.md">中文</a>
  </p>
</div>

---

**ToolFS** 是一个专为大语言模型 (LLM) 智能体设计的虚拟文件系统框架。它将文件、持久化记忆、语义检索 (RAG) 和代码执行 (WASM skills) 等分散的接口，统一映射到符合 POSIX 规范的 `/toolfs` 命名空间中。

通过将复杂的状态管理和工具调用转换为标准的文件系统操作，ToolFS 利用了 LLM 对路径结构和文件操作的天然理解能力，极大地降低了智能体工具集成的门槛。

## 💡 为什么选择 ToolFS？

目前的 AI 智能体架构常常受到“工具膨胀”的困扰，管理大量零散的 API 成为系统瓶颈。ToolFS 通过以下方式解决这一问题：
- **天然的抽象层**：LLM 天生擅长理解文件和目录结构。将工具映射为路径，极大地简化了智能体的意图识别。
- **统一的状态管理**：会话历史、知识库和本地文件共享同一个生命周期。
- **智能体自主性**：上下文感知的技能文档允许智能体在没有硬编码逻辑的情况下，自主发现并链式调用工具。

## 🎯 核心能力

- **统一命名空间**：文件、会话记忆、向量 RAG 查询和自主技能的统一入口 (`/toolfs`)。
- **统一技能 API**：注册并执行 WASM 或原生技能，配备**上下文感知文档**，帮助智能体理解“何时”以及“如何”使用这些工具。
- **会话限制安全**：基于路径的细粒度权限控制，为多租户智能体部署提供隔离环境。
- **原子快照**：支持环境状态的即时快照（写时复制），用于回滚、调试和状态重现。
- **审计就绪**：基于 JSON 的全量操作日志，确保智能体行为的可追溯性与合规性。

## 🛠 架构设计

ToolFS 在智能体与其环境之间充当抽象层：

![ToolFS Architecture](assets/toolfs%20internal%20arch.png)

*ToolFS 内部架构*

```text
[ 智能体 ] <──> [ /toolfs 虚拟路径 ] <──> [ ToolFS 核心 ]
                                                     │
               ┌──────────────┬──────────────┬───────┴──────┬──────────────┐
               ▼              ▼              ▼              ▼              ▼
         [ 本地文件 ]   [ 记忆库 (KV) ] [ RAG 存储 ]   [ WASM 技能 ]  [ 状态快照 ]
```

## 🚀 快速开始

### 1. 安装

```bash
go get github.com/IceWhaleTech/toolfs
```

### 2. 集成示例

只需几行代码即可整合记忆、RAG 和文件访问：

```go
package main

import (
    "github.com/IceWhaleTech/toolfs"
)

func main() {
    // 初始化根挂载点
    fs := toolfs.NewToolFS("/toolfs")

    // 带权限限制的隔离会话
    session, _ := fs.NewSession("agent-007", []string{"/toolfs/data", "/toolfs/memory", "/toolfs/rag"})

    // 持久化记忆 (Memory)
    fs.WriteFileWithSession("/toolfs/memory/last_query", []byte("如何构建智能体？"), session)

    // 语义检索 (RAG)
    // 直接读取虚拟路径！
    results, _ := fs.ReadFileWithSession("/toolfs/rag/query?text=智能体设计&top_k=3", session)
    
    // 技能执行 (Skills)
    // 链式操作：搜索记忆 -> 执行代码技能 -> 保存结果
    ops := []toolfs.Operation{
        {Type: "search_memory", Query: "preferences"},
        {Type: "execute_code_skill", SkillPath: "/toolfs/skills/processor"},
    }
    fs.ChainOperations(ops, session)
}
```

## ⚡ 性能指标

专为高频智能体循环优化。*测试环境：Intel Xeon E5-2690 v2。*

| 操作 | 吞吐量 | 平均延迟 | 开销 |
|-----------|-----------|---------|----------|
| **记忆访问** | **1,000,000+ ops/s** | ~2 μs | 零内存分配 |
| **路径解析**| **14,000,000+ ops/s**| <100 ns | 缓存驱动 |
| **RAG 搜索** | **460,000+ ops/s** | ~7 μs | 高效向量匹配 |
| **本地文件 I/O** | **60,000+ ops/s** | ~50 μs | 本地优先 |

## 🧩 ToolFS vs. AgentFS

虽然两者都关注智能体状态，但侧重点不同：

| 特性 | ToolFS | AgentFS |
|---------|--------|---------|
| **主要目标** | 统一工具/存储抽象 | 结构化状态与审计追踪 |
| **存储引擎** | 虚拟层 (文件, 记忆, RAG) | 基于 SQLite |
| **工具执行** | 原生与 WASM 技能 (统一 API) | 侧重状态的 CRUD |
| **审计模型** | 基于路径/会话的日志 | 事务性 SQL 日志 |

**交叉点：** 两者可以结合使用——AgentFS 用于深度结构化存储，而 ToolFS 为该存储提供标准的文件系统 API，并与其他工具统一。

## 🙏 灵感来源

ToolFS 的灵感源于将文件系统作为自主智能体主要接口的设计模式：
- **[如何使用文件系统和 Bash 构建智能体](https://vercel.com/blog/how-to-build-agents-with-filesystems-and-bash)**：通过标准文件系统模式替代自定义工具，成本降低 75%。
- **[FUSE is All You Need](https://jakobemmerling.de/posts/fuse-is-all-you-need/)**：探索通过虚拟文件系统访问智能体工具。
- **[我们移除了 80% 的智能体工具](https://vercel.com/blog/we-removed-80-percent-of-our-agents-tools)**：通过文件系统化工具设计实现架构极简化的案例研究。

## 📚 文档资源

- **[统一技能系统](skills/SKILL.md)**：深入了解 "Skill is Skill" 架构。
- **[RAG 实现指南](examples/rag_skill/README_zh.md)**：构建自定义语义搜索技能。
- **[Go API 参考](https://pkg.go.dev/github.com/IceWhaleTech/toolfs)**：详细的包接口文档。

---

<div align="center">
  <p>用心制作 ❤️ 为自主智能体社区</p>
</div>
