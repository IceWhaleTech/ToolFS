package main

import (
	"math"
	"sort"
	"strings"
)

// VectorDatabase 是一个简单的内存向量数据库实现
type VectorDatabase struct {
	documents []Document
}

// NewVectorDatabase 创建新的向量数据库
func NewVectorDatabase() *VectorDatabase {
	return &VectorDatabase{
		documents: make([]Document, 0),
	}
}

// AddDocument 添加文档到数据库
func (db *VectorDatabase) AddDocument(doc Document) {
	if doc.Vector == nil {
		// 如果没有向量，生成一个
		doc.Vector = make([]float32, 128)
	}
	db.documents = append(db.documents, doc)
}

// Count 返回文档数量
func (db *VectorDatabase) Count() int {
	return len(db.documents)
}

// Search 执行向量搜索，返回 top_k 结果
func (db *VectorDatabase) Search(query string, topK int) []SearchResult {
	if len(db.documents) == 0 {
		return []SearchResult{}
	}
	
	// 生成查询向量
	queryVector := generateQueryVector(query)
	
	// 计算相似度分数
	type scoreDoc struct {
		document Document
		score    float32
	}
	
	scores := make([]scoreDoc, 0, len(db.documents))
	
	for _, doc := range db.documents {
		score := cosineSimilarity(queryVector, doc.Vector)
		// 同时考虑文本相似度（简单的关键词匹配）
		textScore := textSimilarity(query, doc.Content)
		// 组合向量相似度和文本相似度
		finalScore := score*0.7 + textScore*0.3
		
		scores = append(scores, scoreDoc{
			document: doc,
			score:    finalScore,
		})
	}
	
	// 按分数排序
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})
	
	// 返回 top_k
	if topK > len(scores) {
		topK = len(scores)
	}
	
	results := make([]SearchResult, topK)
	for i := 0; i < topK; i++ {
		results[i] = SearchResult{
			Document: scores[i].document,
			Score:    scores[i].score,
		}
	}
	
	return results
}

// cosineSimilarity 计算余弦相似度
func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	
	dotProduct := float32(0)
	magnitudeA := float32(0)
	magnitudeB := float32(0)
	
	for i := 0; i < len(a); i++ {
		dotProduct += a[i] * b[i]
		magnitudeA += a[i] * a[i]
		magnitudeB += b[i] * b[i]
	}
	
	if magnitudeA == 0 || magnitudeB == 0 {
		return 0
	}
	
	return dotProduct / (float32(math.Sqrt(float64(magnitudeA))) * float32(math.Sqrt(float64(magnitudeB))))
}

// generateQueryVector 为查询生成向量
func generateQueryVector(query string) []float32 {
	// 使用与插件相同的向量生成逻辑
	vector := make([]float32, 128)
	query = strings.ToLower(query)
	
	for i := 0; i < len(vector) && i < len(query); i++ {
		vector[i] = float32(query[i%len(query)]) / 255.0
	}
	
	// 归一化
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

// textSimilarity 计算简单的文本相似度（基于关键词匹配）
func textSimilarity(query, text string) float32 {
	query = strings.ToLower(query)
	text = strings.ToLower(text)
	
	queryWords := strings.Fields(query)
	if len(queryWords) == 0 {
		return 0
	}
	
	matches := 0
	for _, word := range queryWords {
		if strings.Contains(text, word) {
			matches++
		}
	}
	
	return float32(matches) / float32(len(queryWords))
}

