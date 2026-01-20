package toolfs

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// BuiltinMemorySkill is the built-in memory skill that wraps InMemoryStore
type BuiltinMemorySkill struct {
	store *InMemoryStore
}

// NewBuiltinMemorySkill creates a new built-in memory skill
func NewBuiltinMemorySkill(store *InMemoryStore) *BuiltinMemorySkill {
	return &BuiltinMemorySkill{
		store: store,
	}
}

func (p *BuiltinMemorySkill) Name() string {
	return "toolfs-memory"
}

func (p *BuiltinMemorySkill) Version() string {
	return "1.0.0"
}

func (p *BuiltinMemorySkill) Init(config map[string]interface{}) error {
	// Memory skill is already initialized with store
	return nil
}

func (p *BuiltinMemorySkill) Execute(input []byte) ([]byte, error) {
	var request SkillRequest
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	switch request.Operation {
	case "read_file", "read":
		// Extract entry ID from path or data
		entryID := p.extractEntryID(request.Path, request.Data)
		if entryID == "" {
			return json.Marshal(SkillResponse{
				Success: false,
				Error:   "memory entry ID is required",
			})
		}

		entry, err := p.store.Get(entryID)
		if err != nil {
			return json.Marshal(SkillResponse{
				Success: false,
				Error:   err.Error(),
			})
		}

		return json.Marshal(SkillResponse{
			Success: true,
			Result:  entry,
		})

	case "write_file", "write":
		entryID := p.extractEntryID(request.Path, request.Data)
		if entryID == "" {
			return json.Marshal(SkillResponse{
				Success: false,
				Error:   "memory entry ID is required",
			})
		}

		var content string
		var metadata map[string]interface{}

		// Try to parse content from data
		if contentStr, ok := request.Data["content"].(string); ok {
			content = contentStr
		} else if inputStr, ok := request.Data["input"].(string); ok {
			content = inputStr
		}

		// Parse metadata if available
		if meta, ok := request.Data["metadata"].(map[string]interface{}); ok {
			metadata = meta
		}

		// Try to parse from JSON input if content is structured
		if content == "" && request.Data["input"] != nil {
			if inputBytes, err := json.Marshal(request.Data["input"]); err == nil {
				var entry MemoryEntry
				if json.Unmarshal(inputBytes, &entry) == nil {
					content = entry.Content
					if entry.Metadata != nil {
						metadata = entry.Metadata
					}
				}
			}
		}

		err := p.store.Set(entryID, content, metadata)
		if err != nil {
			return json.Marshal(SkillResponse{
				Success: false,
				Error:   err.Error(),
			})
		}

		return json.Marshal(SkillResponse{
			Success: true,
			Result: map[string]interface{}{
				"id":      entryID,
				"message": "memory entry written",
			},
		})

	case "list_dir", "list":
		entries, err := p.store.List()
		if err != nil {
			return json.Marshal(SkillResponse{
				Success: false,
				Error:   err.Error(),
			})
		}

		return json.Marshal(SkillResponse{
			Success: true,
			Result: map[string]interface{}{
				"entries": entries,
			},
		})

	default:
		return json.Marshal(SkillResponse{
			Success: false,
			Error:   fmt.Sprintf("unknown operation: %s", request.Operation),
		})
	}
}

func (p *BuiltinMemorySkill) extractEntryID(path string, data map[string]interface{}) string {
	// Try to extract from path first
	if path != "" {
		parts := strings.Split(strings.Trim(path, "/"), "/")
		for i, part := range parts {
			if part == "memory" && i+1 < len(parts) {
				return parts[i+1]
			}
		}
		// If path doesn't contain "memory", use the last part
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}

	// Try to extract from data
	if data != nil {
		if id, ok := data["entry_id"].(string); ok {
			return id
		}
		if id, ok := data["id"].(string); ok {
			return id
		}
	}

	return ""
}

// GetSkillDocument implements SkillDocumentProvider
func (p *BuiltinMemorySkill) GetSkillDocument() string {
	return `---
name: toolfs-memory
description: Persistent key-value storage for session data, conversation context, and agent state. Use this skill when the user requests storing or retrieving memory entries such as "Store this in memory", "Remember this preference", "Recall the previous conversation", or "List all memory entries".
metadata:
  author: toolfs
  version: "1.0.0"
  module: memory
---

# ToolFS Memory

Persistent key-value storage for session data, conversation context, and agent state.

## Usage

### Read Memory Entry
GET /toolfs/memory/<entry_id>

### Write Memory Entry  
PUT /toolfs/memory/<entry_id>

### List Memory Entries
LIST /toolfs/memory
`
}

// BuiltinRAGSkill is the built-in RAG skill that wraps InMemoryRAGStore
type BuiltinRAGSkill struct {
	store *InMemoryRAGStore
}

// NewBuiltinRAGSkill creates a new built-in RAG skill
func NewBuiltinRAGSkill(store *InMemoryRAGStore) *BuiltinRAGSkill {
	return &BuiltinRAGSkill{
		store: store,
	}
}

func (p *BuiltinRAGSkill) Name() string {
	return "toolfs-rag"
}

func (p *BuiltinRAGSkill) Version() string {
	return "1.0.0"
}

func (p *BuiltinRAGSkill) Init(config map[string]interface{}) error {
	// RAG skill is already initialized with store
	return nil
}

func (p *BuiltinRAGSkill) Execute(input []byte) ([]byte, error) {
	var request SkillRequest
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	switch request.Operation {
	case "read_file", "read", "query", "search":
		// Extract query from path or data
		query, topK := p.extractQuery(request.Path, request.Data)
		if query == "" {
			return json.Marshal(SkillResponse{
				Success: false,
				Error:   "query text is required",
			})
		}

		results, err := p.store.Search(query, topK)
		if err != nil {
			return json.Marshal(SkillResponse{
				Success: false,
				Error:   err.Error(),
			})
		}

		return json.Marshal(SkillResponse{
			Success: true,
			Result: RAGSearchResults{
				Query:   query,
				TopK:    topK,
				Results: results,
			},
		})

	default:
		return json.Marshal(SkillResponse{
			Success: false,
			Error:   fmt.Sprintf("unknown operation: %s", request.Operation),
		})
	}
}

func (p *BuiltinRAGSkill) extractQuery(path string, data map[string]interface{}) (string, int) {
	var query string
	topK := 5

	// Try to extract from path (e.g., /toolfs/rag/query?text=AI+agent&top_k=3)
	if path != "" && strings.Contains(path, "query") {
		parts := strings.SplitN(path, "?", 2)
		if len(parts) == 2 {
			queryURL, err := url.ParseQuery(parts[1])
			if err == nil {
				query = queryURL.Get("text")
				if query == "" {
					query = queryURL.Get("q")
				}
				if query != "" {
					decoded, err := url.QueryUnescape(query)
					if err == nil {
						query = decoded
					}
				}
				if topKStr := queryURL.Get("top_k"); topKStr != "" {
					if k, err := strconv.Atoi(topKStr); err == nil && k > 0 {
						topK = k
					}
				}
			}
		}
	}

	// Try to extract from data
	if query == "" && data != nil {
		if q, ok := data["query"].(string); ok {
			query = q
		} else if q, ok := data["text"].(string); ok {
			query = q
		}
		if k, ok := data["top_k"].(float64); ok {
			topK = int(k)
		}
	}

	return query, topK
}

// GetSkillDocument implements SkillDocumentProvider
func (p *BuiltinRAGSkill) GetSkillDocument() string {
	return `---
name: toolfs-rag
description: Semantic search over vector databases for document retrieval. Use this skill when the user requests searching documents, finding relevant content, or performing semantic queries such as "Search for information about X", "Find documents related to Y", or "Query the knowledge base".
metadata:
  author: toolfs
  version: "1.0.0"
  module: rag
---

# ToolFS RAG

Semantic search over vector databases for document retrieval.

## Usage

### Semantic Search
GET /toolfs/rag/query?text=<query_text>&top_k=<number>
`
}

// BuiltinSkills holds references to all built-in skills
type BuiltinSkills struct {
	Memory *BuiltinMemorySkill
	RAG    *BuiltinRAGSkill
}

// RegisterBuiltinSkills registers all built-in skills with the skill manager
// Only registers skills if the stores are the built-in implementations
func RegisterBuiltinSkills(fs *ToolFS, manager *SkillExecutorManager, session *Session) (*BuiltinSkills, error) {
	ctx := NewSkillContext(fs, session)

	var memorySkill *BuiltinMemorySkill
	var ragSkill *BuiltinRAGSkill

	// Create memory skill if using built-in store
	if inMemoryStore, ok := fs.memoryStore.(*InMemoryStore); ok {
		memorySkill = NewBuiltinMemorySkill(inMemoryStore)
		if err := manager.InjectSkill(memorySkill, ctx, nil); err != nil {
			return nil, fmt.Errorf("failed to register memory skill: %w", err)
		}
	}

	// Create RAG skill if using built-in store
	if inMemoryRAGStore, ok := fs.ragStore.(*InMemoryRAGStore); ok {
		ragSkill = NewBuiltinRAGSkill(inMemoryRAGStore)
		if err := manager.InjectSkill(ragSkill, ctx, nil); err != nil {
			return nil, fmt.Errorf("failed to register RAG skill: %w", err)
		}
	}

	// Return nil if neither skill could be registered
	if memorySkill == nil && ragSkill == nil {
		return nil, fmt.Errorf("built-in skills require built-in store implementations")
	}

	return &BuiltinSkills{
		Memory: memorySkill,
		RAG:    ragSkill,
	}, nil
}
