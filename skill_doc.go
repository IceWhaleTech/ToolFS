package toolfs

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

//go:embed skills/*.md skills/*/*.md
var skillDocsFS embed.FS

// SkillDocument represents a SKILL.md document
type SkillDocument struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Content     string                 `json:"content"`
	Metadata    map[string]interface{} `json:"metadata"`
	Path        string                 `json:"path"` // File path for reference
}

// SkillDocumentManager manages skill documents from plugins and filesystem
type SkillDocumentManager struct {
	documents map[string]*SkillDocument // plugin name or path -> document
	plugins   map[string]ToolFSPlugin  // plugin name -> plugin
}

// NewSkillDocumentManager creates a new skill document manager
func NewSkillDocumentManager() *SkillDocumentManager {
	return &SkillDocumentManager{
		documents: make(map[string]*SkillDocument),
		plugins:   make(map[string]ToolFSPlugin),
	}
}

// RegisterPlugin registers a plugin and extracts its skill document if available
func (sdm *SkillDocumentManager) RegisterPlugin(plugin ToolFSPlugin) error {
	name := plugin.Name()
	sdm.plugins[name] = plugin

	// Check if plugin implements SkillDocumentProvider
	if provider, ok := plugin.(SkillDocumentProvider); ok {
		content := provider.GetSkillDocument()
		if content != "" {
			doc, err := sdm.parseSkillDocument(content)
			if err != nil {
				return fmt.Errorf("failed to parse skill document for plugin %s: %w", name, err)
			}
			doc.Path = fmt.Sprintf("plugin:%s", name)
			sdm.documents[name] = doc
		}
	}

	return nil
}

// RegisterDocument registers a skill document from filesystem or other source
func (sdm *SkillDocumentManager) RegisterDocument(path string, content string) error {
	doc, err := sdm.parseSkillDocument(content)
	if err != nil {
		return fmt.Errorf("failed to parse skill document at %s: %w", path, err)
	}
	doc.Path = path
	key := filepath.Base(path)
	if strings.HasSuffix(key, ".md") {
		key = strings.TrimSuffix(key, ".md")
	}
	sdm.documents[key] = doc
	return nil
}

// GetDocument retrieves a skill document by plugin name or path key
func (sdm *SkillDocumentManager) GetDocument(key string) (*SkillDocument, error) {
	doc, exists := sdm.documents[key]
	if !exists {
		return nil, fmt.Errorf("skill document not found: %s", key)
	}
	return doc, nil
}

// ListDocuments returns all registered skill documents
func (sdm *SkillDocumentManager) ListDocuments() []*SkillDocument {
	docs := make([]*SkillDocument, 0, len(sdm.documents))
	for _, doc := range sdm.documents {
		docs = append(docs, doc)
	}
	return docs
}

// ListDocumentNames returns all registered skill document names/keys
func (sdm *SkillDocumentManager) ListDocumentNames() []string {
	names := make([]string, 0, len(sdm.documents))
	for name := range sdm.documents {
		names = append(names, name)
	}
	return names
}

// LoadBuiltinSkillDocs loads skill documents from embedded filesystem
func (sdm *SkillDocumentManager) LoadBuiltinSkillDocs() error {
	return fs.WalkDir(skillDocsFS, "skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, "SKILL.md") && !strings.HasSuffix(path, ".md") {
			return nil
		}

		content, err := skillDocsFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read skill doc %s: %w", path, err)
		}

		return sdm.RegisterDocument(path, string(content))
	})
}

// parseSkillDocument parses a SKILL.md document and extracts front matter and content
func (sdm *SkillDocumentManager) parseSkillDocument(content string) (*SkillDocument, error) {
	doc := &SkillDocument{
		Content:  content,
		Metadata: make(map[string]interface{}),
	}

	// Parse front matter if present
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		// Extract front matter
		var frontMatter []string
		endIdx := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				endIdx = i
				break
			}
			frontMatter = append(frontMatter, lines[i])
		}

		if endIdx > 0 {
			// Parse front matter (simple YAML-like parser)
			for _, line := range frontMatter {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					key := strings.TrimSpace(parts[0])
					value := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
					
					switch key {
					case "name":
						doc.Name = value
					case "description":
						doc.Description = value
					default:
						if doc.Metadata == nil {
							doc.Metadata = make(map[string]interface{})
						}
						doc.Metadata[key] = value
					}
				}
			}

			// Extract content after front matter
			if endIdx+1 < len(lines) {
				doc.Content = strings.Join(lines[endIdx+1:], "\n")
			}
		}
	}

	// If name not in front matter, try to extract from first heading
	if doc.Name == "" {
		for _, line := range lines {
			if strings.HasPrefix(line, "# ") {
				doc.Name = strings.TrimSpace(strings.TrimPrefix(line, "# "))
				break
			}
		}
	}

	return doc, nil
}

