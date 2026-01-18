<div align="center">
  <h1>🗃ToolFS</h1>
  <p><strong>AI 智能体的虚拟文件系统</strong></p>
  
  <p>
    <a href="README.md">English</a> | 
    <a href="README_zh.md">中文</a>
  </p>
</div>

---

**ToolFS** 是一个专为大语言模型（LLM）智能体设计的开源虚拟文件系统框架（Go 语言）。就像传统文件系统为应用程序提供文件和目录抽象一样，ToolFS 为 AI 智能体提供所需的存储抽象：文件、记忆、RAG、插件和快照——所有这些都通过统一的 `/toolfs` 命名空间访问。

## 🎯 什么是 ToolFS？

ToolFS 是一个虚拟文件系统框架，为 LLM 智能体提供统一的接口来访问：
- **本地文件系统**：访问挂载的本地目录，支持权限控制
- **记忆存储**：用于会话数据和上下文的持久化键值存储
- **RAG 系统**：对向量数据库进行语义搜索，实现文档检索
- **插件系统**：执行挂载到虚拟路径的 WASM 插件
- **快照管理**：创建时间点快照并恢复之前的状态

![ToolFS 架构](assets/toolfs%20internal%20arch.png)

*ToolFS 内部架构*

## 💡 为什么选择 ToolFS？

ToolFS 为 AI 智能体开发提供以下优势：

- **统一接口**：所有存储操作（文件、记忆、RAG 和插件）使用单一的 `/toolfs` 命名空间
- **会话隔离**：每个智能体会话都有独立的访问权限，可配置权限控制
- **可审计性**：所有操作都通过会话跟踪记录，便于安全和调试
- **可重现性**：可在任意时间点快照文件系统状态，稍后恢复以重现精确的执行状态
- **可扩展性**：插件系统允许使用 WASM 模块扩展功能
- **离线可用**：完全离线工作，无需外部依赖
- **沙盒就绪**：专为在沙盒环境中安全执行而设计

## 🚀 快速开始

### 安装

```bash
# 克隆仓库
git clone https://github.com/IceWhaleTech/toolfs.git
cd toolfs

# 下载 Go 模块依赖
go mod tidy
```

### 基本用法

```go
package main

import (
    "fmt"
    "github.com/IceWhaleTech/toolfs"
)

func main() {
    // 初始化 ToolFS
    fs := toolfs.NewToolFS("/toolfs")

    // 挂载本地目录（只读）
    err := fs.MountLocal("/project_data", "./data", true)
    if err != nil {
        panic(err)
    }

    // 读取文件
    content, err := fs.ReadFile("/project_data/example.txt")
    if err != nil {
        panic(err)
    }
    fmt.Println(string(content))

    // 写入记忆
    err = fs.WriteFile("/toolfs/memory/meeting_notes", []byte("讨论 AI 智能体路线图"))
    if err != nil {
        panic(err)
    }

    // 执行 RAG 搜索
    results, err := fs.ReadFile("/toolfs/rag/query?text=AI+agent&top_k=3")
    if err != nil {
        panic(err)
    }
    fmt.Println(results)
}
```

## 🔧 ToolFS 工作原理

ToolFS 为智能体状态管理提供三个核心接口：

1. **文件系统接口**：用于从挂载的本地路径访问文件和目录的类 POSIX 文件系统
2. **记忆接口**：用于持久化会话数据和上下文的键值存储
3. **RAG 接口**：对向量数据库进行语义搜索，实现文档检索

所有操作都通过统一的 `/toolfs` 命名空间进行，无论底层存储类型如何，都提供一致的 API。

![智能体架构](assets/agent%20arch.png)

*使用 ToolFS 的智能体架构*

## ⚡ 性能

ToolFS 针对高性能智能体工作负载进行了优化，具有以下性能特征：

### 核心操作

| 操作 | 吞吐量 | 延迟 | 内存 | 分配次数/操作 |
|------|--------|------|------|--------------|
| **记忆写入** | 1,000,000 ops/s | 2.07 μs | 680 B | 13 |
| **记忆读取** | 883,665 ops/s | 2.83 μs | 304 B | 6 |
| **记忆列表** | 401,068 ops/s | 6.20 μs | 16,408 B | 2 |
| **路径解析** | 14,034,529 ops/s | 75.96 ns | 0 B | 0 |
| **文件读取** (1KB) | 61,449 ops/s | 56.11 μs | 2,072 B | 5 |
| **RAG 搜索** | 465,460 ops/s | 7.28 μs | 1,498 B | 28 |

### 性能亮点

- **内存操作**：亚微秒级延迟，吞吐量 >1M ops/s
- **路径解析缓存**：缓存命中时接近零开销（0 次内存分配）
- **优化的虚拟路径**：预计算并缓存，实现最高效率
- **写时复制快照**：高效的状态管理，开销最小

## ⚡ 性能测试（详细）

ToolFS 提供了完整的性能基准测试套件，可用于评估各功能的性能表现。

### 运行基准测试

```bash
# 运行所有基准测试
go test -bench=. -benchmem ./...

# 运行特定基准测试
go test -bench=BenchmarkReadFile -benchmem ./...

# 运行基准测试并生成性能报告 (Linux/macOS)
bash scripts/benchmark_report.sh

# 运行基准测试并生成性能报告 (Windows)
scripts\benchmark_report.bat
```

### 性能测试覆盖

基准测试包括以下操作：

- **文件操作**: 文件读写、目录列表、大文件处理
- **记忆存储**: 记忆写入、读取、列表操作
- **RAG 搜索**: 语义搜索性能、TopK 参数影响
- **快照操作**: 快照创建、回滚性能
- **插件执行**: 插件调用开销
- **并发性能**: 多协程并发访问性能
- **会话控制**: 访问控制开销

### 性能数据概览

基于实际测试的性能数据（测试环境：Windows amd64，Intel Xeon E5-2690 v2）：

#### 文件操作性能

| 操作 | 吞吐量 | 平均延迟 | 说明 |
|------|--------|----------|------|
| 文件读取 (1KB) | 60K ops/s | 58 μs | 小文件读取性能优秀 |
| 文件读取 (100KB) | 31K ops/s | 119 μs | 大文件读取性能良好 |
| 文件写入 (1KB) | 3.2K ops/s | 1.06 ms | 写入性能受文件系统 I/O 限制 |
| 目录列表 | 22K ops/s | 160 μs | 适合频繁目录遍历 |

#### 记忆存储性能

| 操作 | 吞吐量 | 平均延迟 | 说明 |
|------|--------|----------|------|
| 记忆写入 | **1.0M ops/s** | 2.07 μs | 内存操作极快（优化后） |
| 记忆读取 | **883K ops/s** | 2.83 μs | 检索性能优秀 |
| 记忆列表 (1000项) | **401K ops/s** | 6.20 μs | 性能提升 75%（优化后） |

#### RAG 搜索性能

| 操作 | 吞吐量 | 平均延迟 | 说明 |
|------|--------|----------|------|
| RAG 搜索 | 465K ops/s | 7.28 μs | 搜索性能优秀 |
| RAG 搜索 (TopK) | 421K ops/s | 8.71 μs | TopK 参数影响较小 |

#### 快照操作性能

| 操作 | 吞吐量 | 平均延迟 | 说明 |
|------|--------|----------|------|
| 快照创建 | 570 ops/s | 4.15 ms | 需要扫描和复制文件 |
| 快照回滚 | 672 ops/s | 3.70 ms | 回滚略快于创建 |

#### 并发性能

| 操作 | 吞吐量 | 平均延迟 | 说明 |
|------|--------|----------|------|
| 并发读取 | 91K ops/s | 37.9 μs | 多协程环境下性能良好 |

### 性能亮点

1. **内存操作**: 记忆存储达到 **1.3M ops/s**，比文件 I/O 快约 400 倍
2. **文件读取**: 小文件读取性能达到 **60K ops/s**，延迟仅 58μs
3. **RAG 搜索**: 搜索性能达到 **465K ops/s**，适合实时查询
4. **并发支持**: 多协程并发读取性能优秀

## 📚 文档与示例

如需详细了解使用示例和高级功能，请查看：

- **[技能文档](skills/SKILL.md)** - ToolFS 技能和模块完整指南
  - [记忆技能](skills/memory/SKILL.md) - 持久化存储操作
  - [RAG 技能](skills/rag/SKILL.md) - 语义搜索操作
  - [文件系统技能](skills/filesystem/SKILL.md) - 文件和目录操作
  - [插件技能](skills/plugin/SKILL.md) - 插件执行和管理
  - [快照技能](skills/snapshot/SKILL.md) - 状态管理操作
- **[示例](examples/)** - 工作代码示例和集成案例
  - [RAG 插件示例](examples/rag_plugin/README.md) - 完整的 RAG 插件实现

## 🤔 常见问题

### ToolFS 与传统文件系统有何不同？

传统文件系统提供文件和目录抽象。ToolFS 在此基础上扩展了智能体特定的抽象：记忆存储、RAG 搜索、插件执行和快照管理——所有这些都通过专为 LLM 智能体工作流优化的统一接口提供。

### 可以在容器或虚拟机中使用 ToolFS 吗？

可以！ToolFS 设计为可在任何环境中工作，包括容器、虚拟机和沙盒。它提供适合多租户部署的会话隔离和权限控制。

### ToolFS 与 AgentFS 相比如何？

ToolFS 和 AgentFS 解决类似问题，但采用不同方法：
- **AgentFS**：基于 SQLite 的文件系统，具有审计追踪功能，专为智能体状态管理而设计
- **ToolFS**：虚拟文件系统，为文件、记忆、RAG 和插件提供统一接口，专为可扩展性和插件架构而设计

两者可以结合使用：AgentFS 用于结构化状态管理，ToolFS 用于统一存储抽象和插件执行。

### ToolFS 是否可用于生产环境？

ToolFS 正在积极开发中。请查看下面的路线图了解当前开发状态。在生产环境中使用应进行适当的测试和验证。

## 🗺️ 路线图

- ✅ 阶段 1：基本 FS API（读写/列表）
- ✅ 阶段 2：记忆 / RAG 集成
- ✅ 阶段 3：安全性和权限控制
- ✅ 阶段 4：技能 API / 工具使用集成
- ✅ 阶段 5：快照 / 回滚支持
- 🔄 阶段 6：生产环境加固和性能优化
- 📋 阶段 7：扩展插件生态系统和集成

## 🙏 灵感来源

ToolFS 的灵感来自于基于文件系统的智能体架构模式，该模式充分利用了 LLM 对文件系统和 Unix 工具的原生理解能力。这种方法已在生产系统中得到验证：

- **[Vercel: 如何使用文件系统和 bash 构建智能体](https://vercel.com/blog/how-to-build-agents-with-filesystems-and-bash)** - 展示了如何用文件系统和 bash 工具替换自定义工具，使成本降低 75%，同时提高输出质量
- **[FUSE is All You Need](https://jakobemmerling.de/posts/fuse-is-all-you-need/)** - 探索使用 FUSE 通过虚拟文件系统为智能体提供访问能力
- **[Vercel: 我们移除了智能体 80% 的工具](https://vercel.com/blog/we-removed-80-percent-of-our-agents-tools)** - 关于使用基于文件系统的模式简化智能体架构的案例研究

核心洞察是：由于 LLM 在大量代码上进行训练，它们在文件系统操作方面表现出色。通过将智能体上下文结构化为文件并提供文件系统访问，智能体可以充分利用其原生能力，而无需设计自定义工具。

---

<div align="center">
  <p>用心制作 ❤️ 为 AI 智能体社区</p>
  <p>
    <a href="README.md">English</a> | 
    <a href="README_zh.md">中文</a>
  </p>
</div>

