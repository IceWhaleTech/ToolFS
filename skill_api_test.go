package toolfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSkillRegistry(t *testing.T) {
	// Create a new skill registry
	docManager := NewSkillDocumentManager()
	registry := NewSkillRegistry(docManager)

	// Test registering a skill
	skill := &Skill{
		Name:        "test-skill",
		Description: "A test skill",
		Type:        SkillTypeBuiltin,
		Path:        "/toolfs/skills/test",
		Metadata:    map[string]interface{}{"version": "1.0.0"},
	}

	err := registry.RegisterSkill(skill)
	if err != nil {
		t.Fatalf("Failed to register skill: %v", err)
	}

	// Test getting a skill
	retrieved, err := registry.GetSkill("test-skill")
	if err != nil {
		t.Fatalf("Failed to get skill: %v", err)
	}

	if retrieved.Name != "test-skill" {
		t.Errorf("Expected skill name 'test-skill', got '%s'", retrieved.Name)
	}

	// Test listing skills
	skills := registry.ListSkills()
	if len(skills) != 1 {
		t.Errorf("Expected 1 skill, got %d", len(skills))
	}

	// Test unregistering a skill
	err = registry.UnregisterSkill("test-skill")
	if err != nil {
		t.Fatalf("Failed to unregister skill: %v", err)
	}

	skills = registry.ListSkills()
	if len(skills) != 0 {
		t.Errorf("Expected 0 skills after unregister, got %d", len(skills))
	}
}

func TestFilesystemSkillRegistration(t *testing.T) {
	// Create a temporary directory structure for testing
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "my-skill")
	err := os.MkdirAll(skillDir, 0o755)
	if err != nil {
		t.Fatalf("Failed to create skill directory: %v", err)
	}

	// Create SKILL.md
	skillMdContent := `---
name: my-skill
description: A custom skill for testing
version: 1.0.0
---

# My Skill

This is a test skill with custom functionality.

## Usage

Use this skill to test filesystem skill registration.
`
	skillMdPath := filepath.Join(skillDir, "SKILL.md")
	err = os.WriteFile(skillMdPath, []byte(skillMdContent), 0o644)
	if err != nil {
		t.Fatalf("Failed to write SKILL.md: %v", err)
	}

	// Create references directory
	referencesDir := filepath.Join(skillDir, "references")
	err = os.MkdirAll(referencesDir, 0o755)
	if err != nil {
		t.Fatalf("Failed to create references directory: %v", err)
	}

	// Create scripts directory
	scriptsDir := filepath.Join(skillDir, "scripts")
	err = os.MkdirAll(scriptsDir, 0o755)
	if err != nil {
		t.Fatalf("Failed to create scripts directory: %v", err)
	}

	// Create a skill registry
	docManager := NewSkillDocumentManager()
	registry := NewSkillRegistry(docManager)

	// Register the filesystem skill
	skill, err := registry.RegisterFilesystemSkill(skillDir)
	if err != nil {
		t.Fatalf("Failed to register filesystem skill: %v", err)
	}

	if skill.Name != "my-skill" {
		t.Errorf("Expected skill name 'my-skill', got '%s'", skill.Name)
	}

	if skill.Type != SkillTypeFilesystem {
		t.Errorf("Expected skill type 'filesystem', got '%s'", skill.Type)
	}

	if skill.BasePath != skillDir {
		t.Errorf("Expected base path '%s', got '%s'", skillDir, skill.BasePath)
	}

	// Check metadata for references and scripts
	if hasRefs, ok := skill.Metadata["has_references"].(bool); !ok || !hasRefs {
		t.Error("Expected has_references to be true")
	}

	if hasScripts, ok := skill.Metadata["has_scripts"].(bool); !ok || !hasScripts {
		t.Error("Expected has_scripts to be true")
	}
}

func TestLoadSkillsFromDirectory(t *testing.T) {
	// Create a temporary directory with multiple skills
	tmpDir := t.TempDir()

	// Create skill 1
	skill1Dir := filepath.Join(tmpDir, "skill1")
	err := os.MkdirAll(skill1Dir, 0o755)
	if err != nil {
		t.Fatalf("Failed to create skill1 directory: %v", err)
	}

	skill1Content := `---
name: skill1
description: First test skill
---
# Skill 1
`
	err = os.WriteFile(filepath.Join(skill1Dir, "SKILL.md"), []byte(skill1Content), 0o644)
	if err != nil {
		t.Fatalf("Failed to write skill1 SKILL.md: %v", err)
	}

	// Create skill 2
	skill2Dir := filepath.Join(tmpDir, "skill2")
	err = os.MkdirAll(skill2Dir, 0o755)
	if err != nil {
		t.Fatalf("Failed to create skill2 directory: %v", err)
	}

	skill2Content := `---
name: skill2
description: Second test skill
---
# Skill 2
`
	err = os.WriteFile(filepath.Join(skill2Dir, "SKILL.md"), []byte(skill2Content), 0o644)
	if err != nil {
		t.Fatalf("Failed to write skill2 SKILL.md: %v", err)
	}

	// Create a skill registry
	docManager := NewSkillDocumentManager()
	registry := NewSkillRegistry(docManager)

	// Load all skills from directory
	loadedSkills, err := registry.LoadSkillsFromDirectory(tmpDir)
	if err != nil {
		t.Fatalf("Failed to load skills from directory: %v", err)
	}

	if len(loadedSkills) != 2 {
		t.Errorf("Expected 2 skills to be loaded, got %d", len(loadedSkills))
	}

	// Verify both skills are registered
	_, err = registry.GetSkill("skill1")
	if err != nil {
		t.Errorf("skill1 not found: %v", err)
	}

	_, err = registry.GetSkill("skill2")
	if err != nil {
		t.Errorf("skill2 not found: %v", err)
	}
}

func TestToolFSSkillIntegration(t *testing.T) {
	// Create a ToolFS instance
	fs := NewToolFS("/toolfs")

	// Create a temporary skill directory
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "integration-skill")
	err := os.MkdirAll(skillDir, 0o755)
	if err != nil {
		t.Fatalf("Failed to create skill directory: %v", err)
	}

	skillContent := `---
name: integration-skill
description: Integration test skill
---
# Integration Skill
`
	err = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644)
	if err != nil {
		t.Fatalf("Failed to write SKILL.md: %v", err)
	}

	// Register the skill using ToolFS convenience method
	skill, err := fs.RegisterFilesystemSkill(skillDir)
	if err != nil {
		t.Fatalf("Failed to register skill via ToolFS: %v", err)
	}

	if skill.Name != "integration-skill" {
		t.Errorf("Expected skill name 'integration-skill', got '%s'", skill.Name)
	}

	// List skills
	skills := fs.ListSkills()
	if len(skills) != 1 {
		t.Errorf("Expected 1 skill, got %d", len(skills))
	}

	// Get skill
	retrieved, err := fs.GetSkill("integration-skill")
	if err != nil {
		t.Fatalf("Failed to get skill: %v", err)
	}

	if retrieved.Name != "integration-skill" {
		t.Errorf("Expected skill name 'integration-skill', got '%s'", retrieved.Name)
	}
}

func TestSkillExportImport(t *testing.T) {
	// Create a temporary directory with a skill
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "export-skill")
	err := os.MkdirAll(skillDir, 0o755)
	if err != nil {
		t.Fatalf("Failed to create skill directory: %v", err)
	}

	skillContent := `---
name: export-skill
description: Skill for export/import testing
---
# Export Skill
`
	err = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644)
	if err != nil {
		t.Fatalf("Failed to write SKILL.md: %v", err)
	}

	// Create registry and register skill
	docManager := NewSkillDocumentManager()
	registry := NewSkillRegistry(docManager)
	_, err = registry.RegisterFilesystemSkill(skillDir)
	if err != nil {
		t.Fatalf("Failed to register skill: %v", err)
	}

	// Export skills to JSON
	jsonData, err := registry.ExportSkillsJSON()
	if err != nil {
		t.Fatalf("Failed to export skills: %v", err)
	}

	// Verify JSON structure
	var skills []*Skill
	err = json.Unmarshal(jsonData, &skills)
	if err != nil {
		t.Fatalf("Failed to parse exported JSON: %v", err)
	}

	if len(skills) != 1 {
		t.Errorf("Expected 1 skill in export, got %d", len(skills))
	}

	if skills[0].Name != "export-skill" {
		t.Errorf("Expected skill name 'export-skill', got '%s'", skills[0].Name)
	}

	// Create a new registry and import
	newRegistry := NewSkillRegistry(NewSkillDocumentManager())
	err = newRegistry.ImportSkillsJSON(jsonData)
	if err != nil {
		t.Fatalf("Failed to import skills: %v", err)
	}

	// Verify imported skill
	imported, err := newRegistry.GetSkill("export-skill")
	if err != nil {
		t.Fatalf("Failed to get imported skill: %v", err)
	}

	if imported.Name != "export-skill" {
		t.Errorf("Expected imported skill name 'export-skill', got '%s'", imported.Name)
	}
}

func TestSkillRegistration(t *testing.T) {
	// Create a mock skill
	skill := &MockSkill{
		name:    "test-skill",
		version: "1.0.0",
	}

	// Create registry
	docManager := NewSkillDocumentManager()
	registry := NewSkillRegistry(docManager)

	// Register code skill
	regSkill, err := registry.RegisterCodeSkill(skill, "/toolfs/skills/test")
	if err != nil {
		t.Fatalf("Failed to register code skill: %v", err)
	}

	if regSkill.Name != "test-skill" {
		t.Errorf("Expected skill name 'test-skill', got '%s'", regSkill.Name)
	}

	if regSkill.Type != SkillTypeCode {
		t.Errorf("Expected skill type 'code', got '%s'", regSkill.Type)
	}

	if regSkill.Path != "/toolfs/skills/test" {
		t.Errorf("Expected path '/toolfs/skills/test', got '%s'", regSkill.Path)
	}

	if regSkill.Executor == nil {
		t.Error("Expected executor instance to be set")
	}
}

func TestListSkillsByType(t *testing.T) {
	// Create registry
	docManager := NewSkillDocumentManager()
	registry := NewSkillRegistry(docManager)

	// Register different types of skills
	filesystemSkill := &Skill{
		Name: "fs-skill",
		Type: SkillTypeFilesystem,
		Path: "/toolfs/skills/fs",
	}
	registry.RegisterSkill(filesystemSkill)

	codeSkill := &Skill{
		Name: "skill-skill",
		Type: SkillTypeCode,
		Path: "/toolfs/skills/test",
	}
	registry.RegisterSkill(codeSkill)

	builtinSkill := &Skill{
		Name: "builtin-skill",
		Type: SkillTypeBuiltin,
		Path: "/toolfs/builtin/test",
	}
	registry.RegisterSkill(builtinSkill)

	// Test listing by type
	fsSkills := registry.ListSkillsByType(SkillTypeFilesystem)
	if len(fsSkills) != 1 {
		t.Errorf("Expected 1 filesystem skill, got %d", len(fsSkills))
	}

	codeSkills := registry.ListSkillsByType(SkillTypeCode)
	if len(codeSkills) != 1 {
		t.Errorf("Expected 1 skill skill, got %d", len(codeSkills))
	}

	builtinSkills := registry.ListSkillsByType(SkillTypeBuiltin)
	if len(builtinSkills) != 1 {
		t.Errorf("Expected 1 builtin skill, got %d", len(builtinSkills))
	}
}

// MockSkill is a simple mock skill for testing
type MockSkill struct {
	name    string
	version string
}

func (p *MockSkill) Name() string {
	return p.name
}

func (p *MockSkill) Version() string {
	return p.version
}

func (p *MockSkill) Init(config map[string]interface{}) error {
	return nil
}

func (p *MockSkill) Execute(input []byte) ([]byte, error) {
	response := SkillResponse{
		Success: true,
		Result:  "mock result",
	}
	return json.Marshal(response)
}
