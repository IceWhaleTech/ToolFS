# WASM 导出说明

要将插件编译为 WASM 并在 ToolFS 中使用，需要确保正确导出插件实例。

## 当前实现

插件已经通过以下方式导出：

```go
var PluginInstance ToolFSPlugin = NewRAGPlugin()
```

## WASM 运行时集成

### 使用 wazero（推荐）

```go
package main

import (
    "context"
    "embed"
    "github.com/tetratelabs/wazero"
    "github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed rag.wasm
var wasmFile embed.FS

func LoadRAGPlugin() (ToolFSPlugin, error) {
    ctx := context.Background()
    
    r := wazero.NewRuntime(ctx)
    defer r.Close(ctx)
    
    // 添加 WASI 支持（如果需要）
    _, err := wasi_snapshot_preview1.Instantiate(ctx, r)
    if err != nil {
        return nil, err
    }
    
    // 加载 WASM 模块
    wasm, err := wasmFile.ReadFile("rag.wasm")
    if err != nil {
        return nil, err
    }
    
    module, err := r.InstantiateModuleFromBinary(ctx, wasm)
    if err != nil {
        return nil, err
    }
    
    // 获取导出的插件实例
    pluginInstanceFunc := module.ExportedFunction("PluginInstance")
    // 这里需要根据实际的 WASM 导出方式调整
    
    return plugin, nil
}
```

### 使用 go:wasmimport（Go 1.21+）

如果使用 Go 1.21+ 的 WASM 支持，可以使用 `go:wasmimport`：

```go
//go:wasmimport env plugin_instance
func wasmPluginInstance() uint32
```

### 运行时要求

WASM 插件需要以下运行时支持：

1. **内存管理**：WASM 线性内存
2. **函数导出**：`PluginInstance` 必须可导出
3. **JSON 序列化**：`encoding/json` 包需要在 WASM 中可用

## 编译注意事项

1. **减小二进制大小**：
   ```bash
   GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o rag.wasm
   ```

2. **禁用调试信息**（生产环境）：
   ```bash
   GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o rag.wasm
   ```

3. **检查导出符号**：
   使用 `wasm-objdump` 或 `wasm-tools` 检查导出的函数

## 测试 WASM 模块

```bash
# 使用 Node.js 测试
node -e "
const fs = require('fs');
const wasm = fs.readFileSync('rag.wasm');
WebAssembly.instantiate(wasm).then(result => {
    console.log('Exports:', Object.keys(result.instance.exports));
});
"
```

## 限制和注意事项

1. **网络访问**：WASM 中的 HTTP 请求需要运行时支持
2. **文件系统**：只能通过 ToolFS API 访问文件系统
3. **性能**：WASM 性能低于原生 Go 代码
4. **内存**：WASM 内存限制可能影响大型向量数据库

## 替代方案

如果 WASM 限制太大，可以考虑：

1. **原生 Go 插件**：使用 `plugin` 包（仅 Linux/macOS）
2. **gRPC 服务**：将插件作为独立服务运行
3. **HTTP API**：通过 HTTP 调用插件服务

