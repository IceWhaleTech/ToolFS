package rag_skill

import (
	"math"
	"sort"
	"strings"
)

// VectorDatabase is a simple in-memory vector database implementation
type VectorDatabase struct {
	documents []Document
}

// NewVectorDatabase creates a new vector database
func NewVectorDatabase() *VectorDatabase {
	return &VectorDatabase{
		documents: make([]Document, 0),
	}
}

// AddDocument adds a document to the database
func (db *VectorDatabase) AddDocument(doc Document) {
	if doc.Vector == nil {
		// If no vector, generate one
		doc.Vector = make([]float32, 128)
	}
	db.documents = append(db.documents, doc)
}

// Count returns the number of documents
func (db *VectorDatabase) Count() int {
	return len(db.documents)
}

// Search performs vector search and returns top_k results
func (db *VectorDatabase) Search(query string, topK int) []SearchResult {
	if len(db.documents) == 0 {
		return []SearchResult{}
	}

	// Generate query vector
	queryVector := generateQueryVector(query)

	// Calculate similarity scores
	type scoreDoc struct {
		document Document
		score    float32
	}

	scores := make([]scoreDoc, 0, len(db.documents))

	for _, doc := range db.documents {
		score := cosineSimilarity(queryVector, doc.Vector)
		// Also consider text similarity (simple keyword matching)
		textScore := textSimilarity(query, doc.Content)
		// Combine vector similarity and text similarity
		finalScore := score*0.7 + textScore*0.3

		scores = append(scores, scoreDoc{
			document: doc,
			score:    finalScore,
		})
	}

	// Sort by score
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	// Return top_k
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

// cosineSimilarity calculates cosine similarity
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

// generateQueryVector generates a vector for a query
func generateQueryVector(query string) []float32 {
	// Use the same vector generation logic as in the skill
	vector := make([]float32, 128)
	query = strings.ToLower(query)

	for i := 0; i < len(vector) && i < len(query); i++ {
		vector[i] = float32(query[i%len(query)]) / 255.0
	}

	// Normalize
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

// textSimilarity calculates simple text similarity (keyword based)
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
