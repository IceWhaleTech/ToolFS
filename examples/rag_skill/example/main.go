//go:build ignore
// +build ignore

// This is an integration example showing how to use the RAG skill in ToolFS
// Run: go run main.go

package main

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/IceWhaleTech/toolfs"
	"github.com/IceWhaleTech/toolfs/examples/rag_skill"
)

func main() {
	// 1. Create ToolFS instance
	fs := toolfs.NewToolFS("/toolfs")

	// 2. Create and initialize RAG skill
	ragSkill := rag_skill.NewRAGSkill()
	err := ragSkill.Init(map[string]interface{}{
		"documents": []interface{}{
			map[string]interface{}{
				"id":      "doc1",
				"content": "ToolFS provides secure file access for AI agents with skill support",
				"metadata": map[string]interface{}{
					"source": "documentation",
					"topic":  "introduction",
				},
			},
			map[string]interface{}{
				"id":      "doc2",
				"content": "WASM skills enable sandboxed execution in ToolFS",
				"metadata": map[string]interface{}{
					"source": "documentation",
					"topic":  "skills",
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
		log.Fatalf("Failed to initialize skill: %v", err)
	}

	// 3. Register code skill with ToolFS
	// This automatically handles doc management and execution registration
	skill, err := fs.RegisterCodeSkill(ragSkill, "/toolfs/rag")
	if err != nil {
		log.Fatalf("Failed to register skill: %v", err)
	}

	// 4. Mount skill (RegisterCodeSkill already assigns the mount path, but we could explicitly mount it)
	err = fs.MountSkill(skill)
	if err != nil {
		log.Fatalf("Failed to mount skill: %v", err)
	}

	fmt.Println("=== RAG Skill Integration Example ===\n")

	// 5. Search via Filesystem API
	fmt.Println("1. Searching via ReadFile API:")
	query := "ToolFS skills"
	// ToolFS handles the query parameters and routes them to the skill
	data, err := fs.ReadFile(fmt.Sprintf("/toolfs/rag/query?text=%s", query))
	if err != nil {
		log.Fatalf("ReadFile failed: %v", err)
	}

	// Parse response
	// Skills return SkillResponse as JSON when called via ReadFile
	var response toolfs.SkillResponse
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

	fmt.Println("\n3. Using ExecuteSkill directly:")
	// We can also call ExecuteSkill on the ToolFS instance
	request := &toolfs.SkillRequest{
		Operation: "search",
		Data: map[string]interface{}{
			"query": "WASM sandbox",
			"top_k": 2,
		},
	}
	requestBytes, _ := json.Marshal(request)

	outputBytes, err := fs.ExecuteSkill("rag-skill", requestBytes, nil)
	if err != nil {
		log.Fatalf("ExecuteSkill failed: %v", err)
	}

	var directResponse toolfs.SkillResponse
	if err := json.Unmarshal(outputBytes, &directResponse); err != nil {
		log.Fatalf("Failed to unmarshal direct response: %v", err)
	}

	if directResponse.Success {
		if resultMap, ok := directResponse.Result.(map[string]interface{}); ok {
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
