package main

import (
	"encoding/json"
	"fmt"
	"log"

	toolfs "github.com/IceWhaleTech/toolfs"
)

// Demonstrates how to use the unified Skill API

func main() {
	fmt.Println("=== ToolFS Unified Skill System Demo ===")

	// Create ToolFS instance
	fs := toolfs.NewToolFS("/toolfs")

	// Example 1: Register code skill
	fmt.Println("1. Registering Code Skill")
	dataProcessor := &DataProcessorSkill{
		name:    "data-processor",
		version: "1.0.0",
	}

	codeSkill, err := fs.RegisterCodeSkill(dataProcessor, "/toolfs/skills/data-processor")
	if err != nil {
		log.Printf("Failed to register code skill: %v", err)
	} else {
		fmt.Printf("   ✓ Registration successful: %s [%s]\n", codeSkill.Name, codeSkill.Type)
	}

	// Example 2: Register another code skill
	fmt.Println("\n2. Registering more Code Skills")
	mlAnalyzer := &MLAnalyzerSkill{
		name:    "ml-analyzer",
		version: "2.0.0",
	}

	mlSkill, err := fs.RegisterCodeSkill(mlAnalyzer, "/toolfs/skills/ml-analyzer")
	if err != nil {
		log.Printf("Failed to register code skill: %v", err)
	} else {
		fmt.Printf("   ✓ Registration successful: %s [%s]\n", mlSkill.Name, mlSkill.Type)
	}

	// Example 3: List all Skills (regardless of type)
	fmt.Println("\n3. List all Skills")
	allSkills := fs.ListSkills()
	fmt.Printf("   Total %d skills:\n", len(allSkills))
	for _, skill := range allSkills {
		fmt.Printf("   - %s [%s] @ %s\n", skill.Name, skill.Type, skill.Path)
	}

	// Example 4: Filter by type
	fmt.Println("\n4. Filter Skills by type")
	codeSkills := fs.ListSkillsByType(toolfs.SkillTypeCode)
	fmt.Printf("   Code type skills: %d\n", len(codeSkills))
	for _, skill := range codeSkills {
		fmt.Printf("   - %s (v%s)\n", skill.Name, skill.Metadata["version"])
	}

	// Example 5: Initialize Skills
	fmt.Println("\n5. Initialize Skills")
	config := map[string]interface{}{
		"timeout":      5000,
		"memory_limit": 1024 * 1024,
	}

	for _, skill := range codeSkills {
		err := fs.InitializeSkill(skill.Name, config)
		if err != nil {
			log.Printf("   Failed to initialize %s: %v", skill.Name, err)
		} else {
			fmt.Printf("   ✓ Initialization successful: %s\n", skill.Name)
		}
	}

	// Example 6: Execute Skills
	fmt.Println("\n6. Execute Skills")
	input := []byte(`{"operation": "process", "data": "test data"}`)

	result, err := fs.ExecuteSkill("data-processor", input, nil)
	if err != nil {
		log.Printf("   Execution failed: %v", err)
	} else {
		fmt.Printf("   ✓ Execution result: %s\n", string(result))
	}

	// Example 7: Mount Skills
	fmt.Println("\n7. Mount Skills to ToolFS")
	for _, skill := range codeSkills {
		err := fs.MountSkill(skill)
		if err != nil {
			log.Printf("   Failed to mount %s: %v", skill.Name, err)
		} else {
			fmt.Printf("   ✓ Mounted: %s -> %s\n", skill.Name, skill.Path)
		}
	}

	// Example 8: Get specific Skill details
	fmt.Println("\n8. Get Skill details")
	skill, err := fs.GetSkill("data-processor")
	if err != nil {
		log.Printf("   Failed to get skill: %v", err)
	} else {
		fmt.Printf("   Name: %s\n", skill.Name)
		fmt.Printf("   Type: %s\n", skill.Type)
		fmt.Printf("   Path: %s\n", skill.Path)
		fmt.Printf("   Description: %s\n", skill.Description)
		if skill.Document != nil {
			fmt.Printf("   Document: %s\n", skill.Document.Description)
		}
	}

	fmt.Println("\n=== Demo Complete ===")
}

// DataProcessorSkill is an example code skill
type DataProcessorSkill struct {
	name    string
	version string
	config  map[string]interface{}
}

func (p *DataProcessorSkill) Name() string {
	return p.name
}

func (p *DataProcessorSkill) Version() string {
	return p.version
}

func (p *DataProcessorSkill) Init(config map[string]interface{}) error {
	p.config = config
	fmt.Printf("   [%s] Config loaded\n", p.name)
	return nil
}

func (p *DataProcessorSkill) Execute(input []byte) ([]byte, error) {
	// Parse input
	var req map[string]interface{}
	json.Unmarshal(input, &req)

	// Process data
	result := map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Processed: %v", req["data"]),
		"code":    p.name,
	}

	return json.Marshal(result)
}

// GetSkillDocument implements SkillDocumentProvider interface
func (p *DataProcessorSkill) GetSkillDocument() string {
	return `---
name: data-processor
description: Data processing skill. Use this when the user needs to clean, transform, or format raw data, such as "Format this JSON", "Clean this CSV", or "Process recent logs".
version: 1.0.0
---

# Data Processor

A skill for processing various data formats.

## Features

- Data Transformation
- Data Validation
- Data Cleaning
`
}

// MLAnalyzerSkill is another example code skill
type MLAnalyzerSkill struct {
	name    string
	version string
	config  map[string]interface{}
}

func (p *MLAnalyzerSkill) Name() string {
	return p.name
}

func (p *MLAnalyzerSkill) Version() string {
	return p.version
}

func (p *MLAnalyzerSkill) Init(config map[string]interface{}) error {
	p.config = config
	fmt.Printf("   [%s] Config loaded\n", p.name)
	return nil
}

func (p *MLAnalyzerSkill) Execute(input []byte) ([]byte, error) {
	result := map[string]interface{}{
		"success": true,
		"message": "ML analysis complete",
		"code":    p.name,
	}
	return json.Marshal(result)
}

func (p *MLAnalyzerSkill) GetSkillDocument() string {
	return `---
name: ml-analyzer
description: Machine learning analysis skill. Use this when the user needs to perform predictions, classification, or complex data pattern recognition, such as "Analyze the trend", "Classify these users", or "Predict next month's sales".
version: 2.0.0
---

# ML Analyzer

Machine learning analysis tools.

## Features

- Model Inference
- Data Analysis
- Feature Extraction
`
}
