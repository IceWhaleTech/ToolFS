package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/IceWhaleTech/toolfs"
)

// This example demonstrates how to register and use skills in ToolFS
// Skills can be:
// 1. External filesystem-based skills (with SKILL.md, references/, scripts/)
// 2. Skill-based skills (WASM or native skills)
// 3. Built-in skills (embedded in ToolFS)

func main() {
	// Create a ToolFS instance
	fs := toolfs.NewToolFS("/toolfs")

	// Example 1: Register external skills from a directory
	// This is useful when different projects want to inject their own skills
	fmt.Println("=== Example 1: Loading External Skills ===")

	// Assume we have a skills directory structure like:
	// /path/to/project/skills/
	//   ├── custom-analyzer/
	//   │   ├── SKILL.md
	//   │   ├── references/
	//   │   └── scripts/
	//   └── data-processor/
	//       ├── SKILL.md
	//       └── scripts/

	skillsDir := "./my-project-skills"
	loadedSkills, err := fs.LoadSkillsFromDirectory(skillsDir)
	if err != nil {
		log.Printf("Warning: Failed to load some skills: %v", err)
	}

	fmt.Printf("Loaded %d skills: %v\n", len(loadedSkills), loadedSkills)

	// Example 2: Register a single filesystem skill
	fmt.Println("\n=== Example 2: Register Single Filesystem Skill ===")

	singleSkillPath := "./my-project-skills/custom-skill"
	skillInfo, err := fs.RegisterFilesystemSkill(singleSkillPath)
	if err != nil {
		log.Printf("Failed to register skill: %v", err)
	} else {
		fmt.Printf("Registered skill: %s (%s)\n", skillInfo.Name, skillInfo.Description)
		fmt.Printf("  Path: %s\n", skillInfo.Path)
		fmt.Printf("  Type: %s\n", skillInfo.Type)
	}

	// Example 3: List all registered skills
	fmt.Println("\n=== Example 3: List All Skills ===")

	allSkills := fs.ListSkills()
	for _, skill := range allSkills {
		fmt.Printf("- %s: %s [%s]\n", skill.Name, skill.Description, skill.Type)
	}

	// Example 4: Get a specific skill and access its document
	fmt.Println("\n=== Example 4: Access Skill Documentation ===")

	if len(allSkills) > 0 {
		skillName := allSkills[0].Name
		skill, err := fs.GetSkill(skillName)
		if err != nil {
			log.Printf("Failed to get skill: %v", err)
		} else {
			fmt.Printf("Skill: %s\n", skill.Name)
			if skill.Document != nil {
				fmt.Printf("Description: %s\n", skill.Document.Description)
				fmt.Printf("Content preview: %.100s...\n", skill.Document.Content)
			}
		}
	}

	// Example 5: Register Skill as Skill
	fmt.Println("\n=== Example 5: Register Skill as Skill ===")

	// Create a skill manager
	executorManager := toolfs.NewSkillExecutorManager()
	fs.SetSkillExecutorManager(executorManager)

	// Load a skill (this would typically load from a .wasm file or native skill)
	// For this example, we'll create a mock skill
	skill := &CustomSkill{
		name:    "data-transformer",
		version: "1.0.0",
	}

	// Inject skill with the manager
	ctx := toolfs.NewSkillContext(fs, nil)
	err = executorManager.InjectSkill(skill, ctx, nil)
	if err != nil {
		log.Printf("Failed to inject skill: %v", err)
	}

	// Get the skill registry and register the skill as a skill
	registry := fs.GetSkillRegistry()
	if registry == nil {
		registry = toolfs.NewSkillRegistry(fs.GetSkillDocumentManager())
		fs.AddSkillRegistry(registry)
	}

	// Register code skill
	codeSkill, err := registry.RegisterCodeSkill(skill, "/toolfs/skills/data-transformer")
	if err != nil {
		log.Printf("Failed to register skill skill: %v", err)
	} else {
		fmt.Printf("Registered skill skill: %s\n", codeSkill.Name)
	}

	// Example 6: Export skills configuration
	fmt.Println("\n=== Example 6: Export Skills Configuration ===")

	if registry != nil {
		jsonData, err := registry.ExportSkillsJSON()
		if err != nil {
			log.Printf("Failed to export skills: %v", err)
		} else {
			fmt.Printf("Exported skills configuration:\n%s\n", string(jsonData))

			// Save to file
			configPath := "./skills-config.json"
			err = os.WriteFile(configPath, jsonData, 0o644)
			if err != nil {
				log.Printf("Failed to write config: %v", err)
			} else {
				fmt.Printf("Saved configuration to %s\n", configPath)
			}
		}
	}

	// Example 7: Different projects can have different skill configurations
	fmt.Println("\n=== Example 7: Project-Specific Skills ===")

	// Project A: AI/ML focused
	projectASkills := []string{
		"./skills/ml-model-loader",
		"./skills/data-preprocessor",
		"./skills/inference-engine",
	}

	// Project B: Web scraping focused
	projectBSkills := []string{
		"./skills/web-scraper",
		"./skills/html-parser",
		"./skills/data-extractor",
	}

	fmt.Println("Project A would load:", projectASkills)
	fmt.Println("Project B would load:", projectBSkills)
	fmt.Println("Each project can inject its own skills based on requirements")
}

// CustomSkill is an example skill implementation
type CustomSkill struct {
	name    string
	version string
}

func (p *CustomSkill) Name() string {
	return p.name
}

func (p *CustomSkill) Version() string {
	return p.version
}

func (p *CustomSkill) Init(config map[string]interface{}) error {
	return nil
}

func (p *CustomSkill) Execute(input []byte) ([]byte, error) {
	// Skill execution logic here
	return []byte(`{"success": true, "result": "transformed data"}`), nil
}

// GetSkillDocument implements SkillDocumentProvider interface
func (p *CustomSkill) GetSkillDocument() string {
	return `---
name: data-transformer
description: Transform and process data using custom algorithms
version: 1.0.0
---

# Data Transformer Skill

This skill provides data transformation capabilities.

## Operations

- transform: Apply transformation rules
- validate: Validate data format
- convert: Convert between formats
`
}

// Helper function to create a sample skill directory structure
func createSampleSkillDirectory(basePath, skillName string) error {
	skillPath := filepath.Join(basePath, skillName)

	// Create directories
	dirs := []string{
		skillPath,
		filepath.Join(skillPath, "references"),
		filepath.Join(skillPath, "scripts"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	// Create SKILL.md
	skillMd := fmt.Sprintf(`---
name: %s
description: Sample skill for demonstration
version: 1.0.0
author: example
---

# %s

This is a sample skill that demonstrates the skill structure.

## Features

- Feature 1: Description
- Feature 2: Description
- Feature 3: Description

## Usage

Instructions on how to use this skill.

## Examples

Example usage code or commands.
`, skillName, skillName)

	skillMdPath := filepath.Join(skillPath, "SKILL.md")
	if err := os.WriteFile(skillMdPath, []byte(skillMd), 0o644); err != nil {
		return err
	}

	// Create a sample script
	scriptContent := `#!/bin/bash
# Sample script for ` + skillName + `
echo "Running ` + skillName + ` script"
`
	scriptPath := filepath.Join(skillPath, "scripts", "run.sh")
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		return err
	}

	// Create a sample reference document
	refContent := `# Reference Documentation for ` + skillName + `

This document provides reference information.
`
	refPath := filepath.Join(skillPath, "references", "README.md")
	if err := os.WriteFile(refPath, []byte(refContent), 0o644); err != nil {
		return err
	}

	return nil
}
