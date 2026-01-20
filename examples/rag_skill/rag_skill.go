package rag_skill

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// To run in WASM, we cannot directly import toolfs package
// So we need to define interfaces and types

// SkillExecutor is the interface that skills must implement
type SkillExecutor interface {
	Name() string
	Version() string
	Init(config map[string]interface{}) error
	Execute(input []byte) ([]byte, error)
}

// SkillRequest represents a skill execution request
type SkillRequest struct {
	Operation string                 `json:"operation"`
	Path      string                 `json:"path"`
	Data      map[string]interface{} `json:"data"`
}

// SkillResponse represents a skill execution response
type SkillResponse struct {
	Success  bool                   `json:"success"`
	Result   interface{}            `json:"result,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// Document represents a document in the vector database
type Document struct {
	ID       string                 `json:"id"`
	Content  string                 `json:"content"`
	Vector   []float32              `json:"vector"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// SearchResult represents a search result
type SearchResult struct {
	Document Document `json:"document"`
	Score    float32  `json:"score"`
}

// SearchResponse represents a search response
type SearchResponse struct {
	Query   string         `json:"query"`
	TopK    int            `json:"top_k"`
	Results []SearchResult `json:"results"`
	Count   int            `json:"count"`
}

// RAGSkill implements RAG search skill
type RAGSkill struct {
	name        string
	version     string
	vectorDB    *VectorDatabase
	initialized bool
}

// NewRAGSkill creates a new RAG skill instance
func NewRAGSkill() *RAGSkill {
	return &RAGSkill{
		name:     "rag-skill",
		version:  "1.0.0",
		vectorDB: NewVectorDatabase(),
	}
}

// Name returns the skill name
func (p *RAGSkill) Name() string {
	return p.name
}

// Version returns the skill version
func (p *RAGSkill) Version() string {
	return p.version
}

// Init initializes the skill
func (p *RAGSkill) Init(config map[string]interface{}) error {
	// Load document data from config (if provided)
	if docs, ok := config["documents"].([]interface{}); ok {
		for _, doc := range docs {
			if docMap, ok := doc.(map[string]interface{}); ok {
				document := Document{
					ID:       getString(docMap, "id"),
					Content:  getString(docMap, "content"),
					Metadata: make(map[string]interface{}),
				}
				if metadata, ok := docMap["metadata"].(map[string]interface{}); ok {
					document.Metadata = metadata
				}
				// Generate simple vector (in reality, an embedding model should be used)
				document.Vector = p.generateVector(document.Content)
				p.vectorDB.AddDocument(document)
			}
		}
	}

	// If no documents were provided, load some default example documents
	if p.vectorDB.Count() == 0 {
		p.loadDefaultDocuments()
	}

	p.initialized = true
	return nil
}

// Execute performs skill operation
func (p *RAGSkill) Execute(input []byte) ([]byte, error) {
	if !p.initialized {
		return nil, errors.New("skill not initialized, call Init() first")
	}

	var request SkillRequest
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

// handleSearch handles search request
func (p *RAGSkill) handleSearch(request SkillRequest) ([]byte, error) {
	// Extract query and top_k from Data
	query := ""
	topK := 5

	if request.Data != nil {
		if q, ok := request.Data["query"].(string); ok && q != "" {
			query = q
		} else if q, ok := request.Data["input"].(string); ok && q != "" {
			query = q
		} else if request.Path != "" {
			// Extract query parameter from path
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
		topK = 100 // limit maximum top_k
	}

	// Perform vector search
	results := p.vectorDB.Search(query, topK)

	response := SearchResponse{
		Query:   query,
		TopK:    topK,
		Results: results,
		Count:   len(results),
	}

	skillResponse := SkillResponse{
		Success: true,
		Result:  response,
		Metadata: map[string]interface{}{
			"skill_name":    p.name,
			"skill_version": p.version,
		},
	}

	return json.Marshal(skillResponse)
}

// handleListDir handles directory list request
func (p *RAGSkill) handleListDir() ([]byte, error) {
	response := SkillResponse{
		Success: true,
		Result: map[string]interface{}{
			"entries": []string{"query", "search"},
		},
	}
	return json.Marshal(response)
}

// GetSkillDocument implements SkillDocumentProvider interface
func (p *RAGSkill) GetSkillDocument() string {
	return `---
name: rag-skill
description: Custom RAG search skill. Use this when the user needs to retrieve information from a specific document base, perform semantic search, or answer questions based on a knowledge base, such as "Search document base for X", "Answer this question based on knowledge base", or "Find references related to Y".
version: 1.0.0
---

# RAG Skill

A custom RAG (Retrieval-Augmented Generation) skill illustrating semantic search in ToolFS.

## Features

- Vector Search
- Document Retrieval
- Simple Semantic Matching
`
}

// errorResponse creates error response
func (p *RAGSkill) errorResponse(message string) []byte {
	response := SkillResponse{
		Success: false,
		Error:   message,
	}
	data, _ := json.Marshal(response)
	return data
}

// generateVector generates a simple vector representation for text (mock embedding)
func (p *RAGSkill) generateVector(text string) []float32 {
	vector := make([]float32, 128)
	text = strings.ToLower(text)

	for i := 0; i < len(vector) && i < len(text); i++ {
		vector[i] = float32(text[i%len(text)]) / 255.0
	}

	// Normalize vector
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

// loadDefaultDocuments loads default example documents
func (p *RAGSkill) loadDefaultDocuments() {
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
			Content: "Skills in ToolFS can be written in Go and compiled to WASM for sandboxed execution.",
			Metadata: map[string]interface{}{
				"source": "documentation",
				"topic":  "skills",
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

// getString gets string from map safely
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// SkillInstance is the exported skill instance for WASM execution
var SkillInstance SkillExecutor = NewRAGSkill()
