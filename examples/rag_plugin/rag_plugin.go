package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// 为了能够在 WASM 中运行，我们不能直接导入 toolfs 包
// 所以我们需要定义接口和类型

// ToolFSPlugin 是插件必须实现的接口
type ToolFSPlugin interface {
	Name() string
	Version() string
	Init(config map[string]interface{}) error
	Execute(input []byte) ([]byte, error)
}

// PluginRequest 表示插件执行请求
type PluginRequest struct {
	Operation string                 `json:"operation"`
	Path      string                 `json:"path"`
	Data      map[string]interface{} `json:"data"`
}

// PluginResponse 表示插件执行响应
type PluginResponse struct {
	Success  bool                   `json:"success"`
	Result   interface{}            `json:"result,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// Document 表示向量数据库中的文档
type Document struct {
	ID      string    `json:"id"`
	Content string    `json:"content"`
	Vector  []float32 `json:"vector"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// SearchResult 表示搜索结果
type SearchResult struct {
	Document Document `json:"document"`
	Score    float32  `json:"score"`
}

// SearchResponse 表示搜索响应
type SearchResponse struct {
	Query   string        `json:"query"`
	TopK    int           `json:"top_k"`
	Results []SearchResult `json:"results"`
	Count   int           `json:"count"`
}

// RAGPlugin 实现 RAG 搜索插件
type RAGPlugin struct {
	name        string
	version     string
	vectorDB    *VectorDatabase
	initialized bool
}

// NewRAGPlugin 创建新的 RAG 插件实例
func NewRAGPlugin() *RAGPlugin {
	return &RAGPlugin{
		name:    "rag-plugin",
		version: "1.0.0",
		vectorDB: NewVectorDatabase(),
	}
}

// Name 返回插件名称
func (p *RAGPlugin) Name() string {
	return p.name
}

// Version 返回插件版本
func (p *RAGPlugin) Version() string {
	return p.version
}

// Init 初始化插件
func (p *RAGPlugin) Init(config map[string]interface{}) error {
	// 从配置中加载文档数据（如果提供）
	if docs, ok := config["documents"].([]interface{}); ok {
		for _, doc := range docs {
			if docMap, ok := doc.(map[string]interface{}); ok {
				document := Document{
					ID:      getString(docMap, "id"),
					Content: getString(docMap, "content"),
					Metadata: make(map[string]interface{}),
				}
				if metadata, ok := docMap["metadata"].(map[string]interface{}); ok {
					document.Metadata = metadata
				}
				// 生成简单的向量（实际应该使用嵌入模型）
				document.Vector = p.generateVector(document.Content)
				p.vectorDB.AddDocument(document)
			}
		}
	}
	
	// 如果没有提供文档，加载一些默认示例文档
	if p.vectorDB.Count() == 0 {
		p.loadDefaultDocuments()
	}
	
	p.initialized = true
	return nil
}

// Execute 执行插件操作
func (p *RAGPlugin) Execute(input []byte) ([]byte, error) {
	if !p.initialized {
		return nil, errors.New("plugin not initialized, call Init() first")
	}
	
	var request PluginRequest
	if err := json.Unmarshal(input, &request); err != nil {
		return p.errorResponse("failed to parse request: " + err.Error()), nil
	}
	
	switch request.Operation {
	case "search", "read_file":
		return p.handleSearch(request)
	case "list_dir":
		return p.handleListDir()
	default:
		return p.errorResponse(fmt.Sprintf("unsupported operation: %s", request.Operation)), nil
	}
}

// handleSearch 处理搜索请求
func (p *RAGPlugin) handleSearch(request PluginRequest) ([]byte, error) {
	// 从 Data 中提取查询和 top_k
	query := ""
	topK := 5
	
	if request.Data != nil {
		if q, ok := request.Data["query"].(string); ok && q != "" {
			query = q
		} else if q, ok := request.Data["input"].(string); ok && q != "" {
			query = q
		} else if request.Path != "" {
			// 从路径中提取查询参数
			if idx := strings.Index(request.Path, "?text="); idx != -1 {
				query = request.Path[idx+6:]
			} else if idx := strings.Index(request.Path, "?q="); idx != -1 {
				query = request.Path[idx+3:]
			}
		}
		
		if k, ok := request.Data["top_k"].(float64); ok {
			topK = int(k)
		} else if k, ok := request.Data["top_k"].(int); ok {
			topK = k
		}
	}
	
	if query == "" {
		return p.errorResponse("query parameter is required"), nil
	}
	
	if topK <= 0 {
		topK = 5
	}
	if topK > 100 {
		topK = 100 // 限制最大 top_k
	}
	
	// 执行向量搜索
	results := p.vectorDB.Search(query, topK)
	
	response := SearchResponse{
		Query:   query,
		TopK:    topK,
		Results: results,
		Count:   len(results),
	}
	
	pluginResponse := PluginResponse{
		Success: true,
		Result:  response,
		Metadata: map[string]interface{}{
			"plugin_name":    p.name,
			"plugin_version": p.version,
		},
	}
	
	return json.Marshal(pluginResponse)
}

// handleListDir 处理目录列表请求
func (p *RAGPlugin) handleListDir() ([]byte, error) {
	response := PluginResponse{
		Success: true,
		Result: map[string]interface{}{
			"entries": []string{"query", "search"},
		},
	}
	return json.Marshal(response)
}

// errorResponse 创建错误响应
func (p *RAGPlugin) errorResponse(message string) []byte {
	response := PluginResponse{
		Success: false,
		Error:   message,
	}
	data, _ := json.Marshal(response)
	return data
}

// generateVector 为文本生成简单的向量表示（模拟嵌入）
// 在实际应用中，应该使用真正的嵌入模型（如 OpenAI, Sentence-BERT 等）
func (p *RAGPlugin) generateVector(text string) []float32 {
	// 简单的基于字符频率的向量生成（仅用于演示）
	// 实际应该使用真实的嵌入模型
	vector := make([]float32, 128)
	text = strings.ToLower(text)
	
	for i := 0; i < len(vector) && i < len(text); i++ {
		vector[i] = float32(text[i%len(text)]) / 255.0
	}
	
	// 归一化向量
	magnitude := float32(0)
	for _, v := range vector {
		magnitude += v * v
	}
	magnitude = float32(math.Sqrt(float64(magnitude)))
	
	if magnitude > 0 {
		for i := range vector {
			vector[i] /= magnitude
		}
	}
	
	return vector
}

// loadDefaultDocuments 加载默认示例文档
func (p *RAGPlugin) loadDefaultDocuments() {
	defaultDocs := []Document{
		{
			ID:      "doc1",
			Content: "ToolFS is a virtual filesystem for AI agents that provides secure file access.",
			Metadata: map[string]interface{}{
				"source": "documentation",
				"topic":  "introduction",
			},
		},
		{
			ID:      "doc2",
			Content: "Plugins in ToolFS can be written in Go and compiled to WASM for sandboxed execution.",
			Metadata: map[string]interface{}{
				"source": "documentation",
				"topic":  "plugins",
			},
		},
		{
			ID:      "doc3",
			Content: "RAG (Retrieval-Augmented Generation) combines retrieval and generation for better AI responses.",
			Metadata: map[string]interface{}{
				"source": "documentation",
				"topic":  "rag",
			},
		},
		{
			ID:      "doc4",
			Content: "Vector databases store documents as high-dimensional vectors for semantic search.",
			Metadata: map[string]interface{}{
				"source": "documentation",
				"topic":  "vector-db",
			},
		},
		{
			ID:      "doc5",
			Content: "Semantic search finds documents based on meaning rather than exact keyword matching.",
			Metadata: map[string]interface{}{
				"source": "documentation",
				"topic":  "semantic-search",
			},
		},
	}
	
	for _, doc := range defaultDocs {
		doc.Vector = p.generateVector(doc.Content)
		p.vectorDB.AddDocument(doc)
	}
}

// getString 从 map 中安全获取字符串
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// 导出插件实例（用于 WASM 导出）
var PluginInstance ToolFSPlugin = NewRAGPlugin()

