# RAG Plugin for ToolFS

这是一个示例 RAG (Retrieval-Augmented Generation) 插件，演示如何为 ToolFS 开发、编译和测试 WASM 插件。

## 功能特性

- ✅ 实现 `ToolFSPlugin` 接口
- ✅ 内存向量数据库（支持真实向量数据库集成）
- ✅ 语义搜索和关键词搜索
- ✅ 返回 JSON 格式的搜索结果
- ✅ 可编译为 WASM 模块
- ✅ 完整的单元测试

## 项目结构

```
rag_plugin/
├── rag_plugin.go      # 主插件实现
├── vector_db.go       # 向量数据库实现
├── rag_plugin_test.go # 单元测试
├── go.mod            # Go 模块定义
├── build.sh          # Linux/Mac 构建脚本
├── build.bat         # Windows 构建脚本
└── README.md         # 本文档
```

## 开发插件

### 1. 实现 ToolFSPlugin 接口

插件必须实现以下接口：

```go
type ToolFSPlugin interface {
    Name() string
    Version() string
    Init(config map[string]interface{}) error
    Execute(input []byte) ([]byte, error)
}
```

### 2. 插件初始化

`Init()` 方法接收配置参数，可以：
- 加载文档数据
- 初始化向量数据库
- 设置插件参数

示例：

```go
config := map[string]interface{}{
    "documents": []interface{}{
        map[string]interface{}{
            "id":      "doc1",
            "content": "Document content here",
            "metadata": map[string]interface{}{
                "source": "example",
            },
        },
    },
}
plugin.Init(config)
```

### 3. 处理插件请求

`Execute()` 方法接收 JSON 格式的 `PluginRequest`：

```json
{
    "operation": "search",
    "path": "/toolfs/rag/query?text=AI",
    "data": {
        "query": "AI agents",
        "top_k": 5
    }
}
```

必须返回 JSON 格式的 `PluginResponse`：

```json
{
    "success": true,
    "result": {
        "query": "AI agents",
        "top_k": 5,
        "results": [
            {
                "document": {
                    "id": "doc1",
                    "content": "...",
                    "metadata": {}
                },
                "score": 0.95
            }
        ],
        "count": 1
    }
}
```

## 编译为 WASM

### Linux/Mac

```bash
chmod +x build.sh
./build.sh
```

或手动编译：

```bash
GOOS=js GOARCH=wasm go build -o rag.wasm .
```

### Windows

```cmd
build.bat
```

或手动编译：

```cmd
set GOOS=js
set GOARCH=wasm
go build -o rag.wasm .
```

### 导出插件实例

在插件代码中，必须导出插件实例：

```go
var PluginInstance ToolFSPlugin = NewRAGPlugin()
```

WASM 运行时需要通过此导出访问插件。

## 注入插件到 ToolFS

### 方法 1: 使用 PluginManager（推荐）

```go
package main

import (
    "github.com/IceWhaleTech/toolfs"
    // 导入 WASM 加载器
)

func main() {
    fs := toolfs.NewToolFS("/toolfs")
    pm := toolfs.NewPluginManager()
    
    // 配置 WASM 加载器（需要实现 WASMPluginLoader 接口）
    // loader := NewWASMLoader() // 你的 WASM 加载器实现
    // pm.SetWASMLoader(loader)
    
    // 加载 WASM 插件
    ctx := toolfs.NewPluginContext(fs, nil)
    err := pm.LoadPlugin("rag.wasm", ctx, map[string]interface{}{
        "documents": []interface{}{
            // 你的文档数据
        },
    })
    
    if err != nil {
        panic(err)
    }
    
    // 使用插件
    request := &toolfs.PluginRequest{
        Operation: "search",
        Data: map[string]interface{}{
            "query": "AI agents",
            "top_k": 5,
        },
    }
    
    response, err := pm.ExecutePlugin("rag-plugin", request)
    // 处理响应...
}
```

### 方法 2: 直接注入 Go 插件（开发/测试）

```go
package main

import (
    "github.com/IceWhaleTech/toolfs/examples/rag_plugin"
    "github.com/IceWhaleTech/toolfs"
)

func main() {
    fs := toolfs.NewToolFS("/toolfs")
    pm := toolfs.NewPluginManager()
    
    ctx := toolfs.NewPluginContext(fs, nil)
    
    // 创建插件实例
    plugin := rag_plugin.NewRAGPlugin()
    plugin.Init(map[string]interface{}{
        "documents": []interface{}{
            // 文档数据
        },
    })
    
    // 注入插件
    pm.InjectPlugin(plugin, ctx, nil)
    
    // 使用插件
    request := &toolfs.PluginRequest{
        Operation: "search",
        Data: map[string]interface{}{
            "query": "ToolFS",
            "top_k": 3,
        },
    }
    
    response, err := pm.ExecutePlugin("rag-plugin", request)
    // 处理响应...
}
```

### 方法 3: 挂载为插件路径

```go
fs := toolfs.NewToolFS("/toolfs")
pm := toolfs.NewPluginManager()
fs.SetPluginManager(pm)

// 加载插件
plugin := rag_plugin.NewRAGPlugin()
plugin.Init(nil)
ctx := toolfs.NewPluginContext(fs, nil)
pm.InjectPlugin(plugin, ctx, nil)

// 挂载插件到路径
fs.MountPlugin("/toolfs/rag", "rag-plugin")

// 通过文件系统 API 访问
data, err := fs.ReadFile("/toolfs/rag/query?text=AI")
// data 包含 JSON 格式的搜索结果
```

## 运行测试

```bash
cd examples/rag_plugin
go test -v
```

测试覆盖：
- ✅ 搜索功能
- ✅ 路径参数解析
- ✅ 目录列表
- ✅ 无效操作处理
- ✅ 初始化验证
- ✅ 向量数据库搜索
- ✅ 相似度计算

## 集成真实向量数据库

要集成真实的向量数据库（如 Milvus, Pinecone, Weaviate），只需修改 `vector_db.go`：

```go
type VectorDatabase struct {
    client *milvus.Client // 或你选择的数据库客户端
    // ...
}

func (db *VectorDatabase) Search(query string, topK int) []SearchResult {
    // 使用真实数据库 API
    queryVector := generateQueryVector(query)
    results := db.client.Search(queryVector, topK)
    // 转换为 SearchResult
    return results
}
```

## 使用嵌入模型

当前实现使用简单的向量生成。要使用真实的嵌入模型：

1. **在 WASM 中运行模型**（不推荐，WASM 性能有限）
2. **使用 HTTP API 调用嵌入服务**（推荐）：

```go
func (p *RAGPlugin) generateVector(text string) []float32 {
    // 调用嵌入 API
    resp, err := http.Post("https://api.openai.com/v1/embeddings", ...)
    // 解析响应获取向量
    return vector
}
```

3. **在初始化时预计算所有文档向量**

## 示例：完整使用流程

```go
package main

import (
    "encoding/json"
    "fmt"
    "github.com/IceWhaleTech/toolfs"
    "github.com/IceWhaleTech/toolfs/examples/rag_plugin"
)

func main() {
    // 1. 创建 ToolFS 实例
    fs := toolfs.NewToolFS("/toolfs")
    
    // 2. 创建插件管理器
    pm := toolfs.NewPluginManager()
    fs.SetPluginManager(pm)
    
    // 3. 创建并初始化 RAG 插件
    ragPlugin := rag_plugin.NewRAGPlugin()
    err := ragPlugin.Init(map[string]interface{}{
        "documents": []interface{}{
            map[string]interface{}{
                "id":      "1",
                "content": "ToolFS provides secure file access for AI agents",
            },
        },
    })
    if err != nil {
        panic(err)
    }
    
    // 4. 注入插件
    ctx := toolfs.NewPluginContext(fs, nil)
    pm.InjectPlugin(ragPlugin, ctx, nil)
    
    // 5. 挂载插件到路径
    fs.MountPlugin("/toolfs/rag", "rag-plugin")
    
    // 6. 通过文件系统 API 搜索
    data, err := fs.ReadFile("/toolfs/rag/query?text=ToolFS")
    if err != nil {
        panic(err)
    }
    
    // 7. 解析结果
    var response toolfs.PluginResponse
    json.Unmarshal(data, &response)
    
    if response.Success {
        fmt.Printf("Search results: %+v\n", response.Result)
    }
}
```

## 性能优化建议

1. **预计算向量**：在插件初始化时计算所有文档向量
2. **使用索引**：对于大型数据库，使用向量索引（如 HNSW）
3. **批量处理**：支持批量查询以提高吞吐量
4. **缓存**：缓存常用查询结果

## 故障排除

### 插件无法加载
- 检查 WASM 文件是否正确编译
- 验证 `PluginInstance` 是否正确导出
- 确认 WASM 运行时配置正确

### 搜索结果为空
- 验证文档是否正确加载
- 检查查询参数格式
- 确认向量生成逻辑正确

### 性能问题
- 减少向量维度
- 使用更高效的相似度算法
- 限制 top_k 大小

## 许可证

本示例插件遵循 ToolFS 项目许可证。

