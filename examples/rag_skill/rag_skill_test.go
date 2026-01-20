package main

import (
	"encoding/json"
	"testing"
)

func TestRAGSkill_Execute_Search(t *testing.T) {
	skill := NewRAGSkill()
	
	// 初始化插件
	err := skill.Init(nil)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	
	// 创建搜索请求
	request := SkillRequest{
		Operation: "search",
		Data: map[string]interface{}{
			"query": "ToolFS virtual filesystem",
			"top_k": 3,
		},
	}
	
	input, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}
	
	// 执行插件
	output, err := skill.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	
	// 解析响应
	var response SkillResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}
	
	// 验证响应
	if !response.Success {
		t.Errorf("Expected success, got error: %s", response.Error)
	}
	
	// 验证结果类型
	resultMap, ok := response.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected result to be a map, got %T", response.Result)
	}
	
	// 验证搜索响应结构
	if results, ok := resultMap["results"].([]interface{}); ok {
		if len(results) == 0 {
			t.Error("Expected at least one search result")
		}
		
		if len(results) > 3 {
			t.Errorf("Expected at most 3 results, got %d", len(results))
		}
		
		// 验证第一个结果的结构
		if len(results) > 0 {
			firstResult, ok := results[0].(map[string]interface{})
			if !ok {
				t.Error("Expected result to be a map")
			} else {
				if _, ok := firstResult["document"]; !ok {
					t.Error("Expected result to have 'document' field")
				}
				if _, ok := firstResult["score"]; !ok {
					t.Error("Expected result to have 'score' field")
				}
			}
		}
	} else {
		t.Error("Expected 'results' field in response")
	}
	
	if query, ok := resultMap["query"].(string); ok {
		if query != "ToolFS virtual filesystem" {
			t.Errorf("Expected query to be 'ToolFS virtual filesystem', got '%s'", query)
		}
	} else {
		t.Error("Expected 'query' field in response")
	}
	
	if topK, ok := resultMap["top_k"].(float64); ok {
		if int(topK) != 3 {
			t.Errorf("Expected top_k to be 3, got %d", int(topK))
		}
	} else {
		t.Error("Expected 'top_k' field in response")
	}
}

func TestRAGSkill_Execute_SearchWithPath(t *testing.T) {
	skill := NewRAGSkill()
	skill.Init(nil)
	
	request := SkillRequest{
		Operation: "read_file",
		Path:      "/toolfs/rag/query?text=skills+WASM",
		Data: map[string]interface{}{
			"top_k": 2,
		},
	}
	
	input, _ := json.Marshal(request)
	output, err := skill.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	
	var response SkillResponse
	json.Unmarshal(output, &response)
	
	if !response.Success {
		t.Errorf("Expected success, got error: %s", response.Error)
	}
}

func TestRAGSkill_Execute_ListDir(t *testing.T) {
	skill := NewRAGSkill()
	skill.Init(nil)
	
	request := SkillRequest{
		Operation: "list_dir",
	}
	
	input, _ := json.Marshal(request)
	output, err := skill.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	
	var response SkillResponse
	json.Unmarshal(output, &response)
	
	if !response.Success {
		t.Errorf("Expected success, got error: %s", response.Error)
	}
	
	if resultMap, ok := response.Result.(map[string]interface{}); ok {
		if entries, ok := resultMap["entries"].([]interface{}); ok {
			if len(entries) == 0 {
				t.Error("Expected at least one entry")
			}
		}
	}
}

func TestRAGSkill_Execute_InvalidOperation(t *testing.T) {
	skill := NewRAGSkill()
	skill.Init(nil)
	
	request := SkillRequest{
		Operation: "invalid_operation",
	}
	
	input, _ := json.Marshal(request)
	output, err := skill.Execute(input)
	if err != nil {
		t.Fatalf("Execute should not return error: %v", err)
	}
	
	var response SkillResponse
	json.Unmarshal(output, &response)
	
	if response.Success {
		t.Error("Expected failure for invalid operation")
	}
	
	if response.Error == "" {
		t.Error("Expected error message")
	}
}

func TestRAGSkill_Execute_MissingQuery(t *testing.T) {
	skill := NewRAGSkill()
	skill.Init(nil)
	
	request := SkillRequest{
		Operation: "search",
		Data: map[string]interface{}{
			"top_k": 3,
		},
	}
	
	input, _ := json.Marshal(request)
	output, err := skill.Execute(input)
	if err != nil {
		t.Fatalf("Execute should not return error: %v", err)
	}
	
	var response SkillResponse
	json.Unmarshal(output, &response)
	
	if response.Success {
		t.Error("Expected failure for missing query")
	}
}

func TestRAGSkill_Init_WithDocuments(t *testing.T) {
	skill := NewRAGSkill()
	
	config := map[string]interface{}{
		"documents": []interface{}{
			map[string]interface{}{
				"id":      "test1",
				"content": "This is a test document about AI",
				"metadata": map[string]interface{}{
					"category": "test",
				},
			},
			map[string]interface{}{
				"id":      "test2",
				"content": "Another document about machine learning",
				"metadata": map[string]interface{}{
					"category": "test",
				},
			},
		},
	}
	
	err := skill.Init(config)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	
	// 验证文档已加载
	if skill.vectorDB.Count() != 2 {
		t.Errorf("Expected 2 documents, got %d", skill.vectorDB.Count())
	}
	
	// 测试搜索
	request := SkillRequest{
		Operation: "search",
		Data: map[string]interface{}{
			"query": "AI",
			"top_k": 1,
		},
	}
	
	input, _ := json.Marshal(request)
	output, err := skill.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	
	var response SkillResponse
	json.Unmarshal(output, &response)
	
	if !response.Success {
		t.Errorf("Expected success, got error: %s", response.Error)
	}
	
	resultMap := response.Result.(map[string]interface{})
	if results, ok := resultMap["results"].([]interface{}); ok {
		if len(results) == 0 {
			t.Error("Expected at least one search result")
		}
	}
}

func TestRAGSkill_Execute_NotInitialized(t *testing.T) {
	skill := NewRAGSkill()
	// 不调用 Init()
	
	request := SkillRequest{
		Operation: "search",
		Data: map[string]interface{}{
			"query": "test",
		},
	}
	
	input, _ := json.Marshal(request)
	_, err := skill.Execute(input)
	
	if err == nil {
		t.Error("Expected error for uninitialized skill")
	}
}

func TestVectorDatabase_Search(t *testing.T) {
	db := NewVectorDatabase()
	
	doc1 := Document{
		ID:      "1",
		Content: "ToolFS is great",
		Vector:  generateQueryVector("ToolFS is great"),
	}
	doc2 := Document{
		ID:      "2",
		Content: "WASM skills are secure",
		Vector:  generateQueryVector("WASM skills are secure"),
	}
	
	db.AddDocument(doc1)
	db.AddDocument(doc2)
	
	results := db.Search("ToolFS", 1)
	
	if len(results) == 0 {
		t.Error("Expected at least one result")
	}
	
	if results[0].Document.ID != "1" {
		t.Errorf("Expected document ID '1', got '%s'", results[0].Document.ID)
	}
	
	if results[0].Score <= 0 {
		t.Error("Expected positive score")
	}
}

func TestCosineSimilarity(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	
	score := cosineSimilarity(a, b)
	if score != 1.0 {
		t.Errorf("Expected similarity 1.0, got %f", score)
	}
	
	c := []float32{0, 1, 0}
	score = cosineSimilarity(a, c)
	if score != 0.0 {
		t.Errorf("Expected similarity 0.0, got %f", score)
	}
}

