//go:build ignore
// +build ignore

// 这是一个集成示例，展示如何在 ToolFS 中使用 RAG 插件
// 运行: go run integration_example.go

package main

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/IceWhaleTech/toolfs"
	rag_plugin "github.com/IceWhaleTech/toolfs/examples/rag_plugin"
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
				"id":      "doc1",
				"content": "ToolFS provides secure file access for AI agents with plugin support",
				"metadata": map[string]interface{}{
					"source": "documentation",
					"topic":  "introduction",
				},
			},
			map[string]interface{}{
				"id":      "doc2",
				"content": "WASM plugins enable sandboxed execution in ToolFS",
				"metadata": map[string]interface{}{
					"source": "documentation",
					"topic":  "plugins",
				},
			},
			map[string]interface{}{
				"id":      "doc3",
				"content": "RAG combines retrieval and generation for better AI responses",
				"metadata": map[string]interface{}{
					"source": "documentation",
					"topic":  "rag",
				},
			},
		},
	})
	if err != nil {
		log.Fatalf("Failed to initialize plugin: %v", err)
	}

	// 4. 注入插件到插件管理器
	ctx := toolfs.NewPluginContext(fs, nil)
	pm.InjectPlugin(ragPlugin, ctx, nil)

	// 5. 挂载插件到路径
	err = fs.MountPlugin("/toolfs/rag", "rag-plugin")
	if err != nil {
		log.Fatalf("Failed to mount plugin: %v", err)
	}

	fmt.Println("=== RAG Plugin Integration Example ===\n")

	// 6. 通过文件系统 API 进行搜索
	fmt.Println("1. Searching via ReadFile API:")
	query := "ToolFS plugins"
	data, err := fs.ReadFile(fmt.Sprintf("/toolfs/rag/query?text=%s", query))
	if err != nil {
		log.Fatalf("ReadFile failed: %v", err)
	}

	// 解析响应
	var response toolfs.PluginResponse
	if err := json.Unmarshal(data, &response); err != nil {
		log.Fatalf("Failed to unmarshal response: %v", err)
	}

	if response.Success {
		fmt.Printf("Query: %s\n", query)
		if resultMap, ok := response.Result.(map[string]interface{}); ok {
			if results, ok := resultMap["results"].([]interface{}); ok {
				fmt.Printf("Found %d results:\n", len(results))
				for i, r := range results {
					if result, ok := r.(map[string]interface{}); ok {
						if doc, ok := result["document"].(map[string]interface{}); ok {
							fmt.Printf("  [%d] Score: %.3f - %s\n",
								i+1,
								result["score"].(float64),
								doc["content"].(string))
						}
					}
				}
			}
		}
	} else {
		fmt.Printf("Search failed: %s\n", response.Error)
	}

	fmt.Println("\n2. Listing directory:")
	entries, err := fs.ListDir("/toolfs/rag")
	if err != nil {
		log.Fatalf("ListDir failed: %v", err)
	}
	fmt.Printf("Entries: %v\n", entries)

	fmt.Println("\n3. Using PluginManager directly:")
	request := &toolfs.PluginRequest{
		Operation: "search",
		Data: map[string]interface{}{
			"query": "WASM sandbox",
			"top_k": 2,
		},
	}

	pluginResponse, err := pm.ExecutePlugin("rag-plugin", request)
	if err != nil {
		log.Fatalf("ExecutePlugin failed: %v", err)
	}

	if pluginResponse.Success {
		if resultMap, ok := pluginResponse.Result.(map[string]interface{}); ok {
			if results, ok := resultMap["results"].([]interface{}); ok {
				fmt.Printf("Found %d results for 'WASM sandbox':\n", len(results))
				for i, r := range results {
					if result, ok := r.(map[string]interface{}); ok {
						if doc, ok := result["document"].(map[string]interface{}); ok {
							fmt.Printf("  [%d] Score: %.3f - %s\n",
								i+1,
								result["score"].(float64),
								doc["content"].(string))
						}
					}
				}
			}
		}
	}

	fmt.Println("\n=== Example Complete ===")
}
