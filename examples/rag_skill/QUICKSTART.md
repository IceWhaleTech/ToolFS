# RAG 插件快速开始指南

## 5 分钟快速开始

### 1. 编译插件

**Linux/Mac:**
```bash
cd examples/rag_skill
chmod +x build.sh
./build.sh
```

**Windows:**
```cmd
cd examples\rag_skill
build.bat
```

### 2. 运行测试

```bash
go test -v
```

应该看到所有测试通过：
```
=== RUN   TestRAGSkill_Execute_Search
--- PASS: TestRAGSkill_Execute_Search (0.00s)
...
PASS
```

### 3. 在代码中使用

```go
package main

import (
    "fmt"
    "github.com/IceWhaleTech/toolfs"
    rag_skill "github.com/IceWhaleTech/toolfs/examples/rag_skill"
)

func main() {
    // 创建 ToolFS
    fs := toolfs.NewToolFS("/toolfs")
    pm := toolfs.NewSkillExecutorManager()
    fs.SetSkillExecutorManager(pm)
    
    // 创建并注入插件
    skill := rag_skill.NewRAGSkill()
    skill.Init(nil) // 使用默认文档
    
    ctx := toolfs.NewSkillContext(fs, nil)
    pm.InjectSkill(skill, ctx, nil)
    
    // 挂载插件
    fs.MountSkill("/toolfs/rag", "rag-skill")
    
    // 搜索
    data, _ := fs.ReadFile("/toolfs/rag/query?text=ToolFS")
    fmt.Println(string(data))
}
```

## 测试插件

### 单元测试

```bash
go test -v -run TestRAGSkill_Execute_Search
```

### 集成测试

```bash
# 注意：integration_example.go 需要 toolfs 包可用
# 如果不在 toolfs 项目内，需要调整导入路径
go run integration_example.go
```

## 编译为 WASM

```bash
# Linux/Mac
GOOS=js GOARCH=wasm go build -o rag.wasm .

# Windows
set GOOS=js
set GOARCH=wasm
go build -o rag.wasm .
```

## 添加自定义文档

```go
skill := rag_skill.NewRAGSkill()
skill.Init(map[string]interface{}{
    "documents": []interface{}{
        map[string]interface{}{
            "id":      "my-doc-1",
            "content": "Your document content here",
            "metadata": map[string]interface{}{
                "source": "my-source",
                "author": "me",
            },
        },
    },
})
```

## 常见问题

### Q: 如何添加更多文档？
A: 在 `Init()` 时通过 `documents` 配置项传入，或修改 `loadDefaultDocuments()` 方法。

### Q: 如何使用真实的嵌入模型？
A: 修改 `generateVector()` 方法，调用嵌入 API（如 OpenAI, Sentence-BERT）。

### Q: 如何集成真实的向量数据库？
A: 修改 `vector_db.go`，实现真实的数据库客户端接口。

### Q: WASM 编译失败？
A: 确保使用 Go 1.21+，并且设置了正确的 GOOS 和 GOARCH。

## 下一步

- 阅读完整 [README.md](README.md)
- 查看 [WASM_EXPORT.md](WASM_EXPORT.md) 了解 WASM 集成
- 查看 `integration_example.go` 了解完整使用示例

