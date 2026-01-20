package main

import (
	"encoding/json"
	"fmt"
	"log"

	toolfs "github.com/IceWhaleTech/toolfs"
)

// 演示如何使用统一的 Skill API

func main() {
	fmt.Println("=== ToolFS 统一 Skill 系统演示 ===")

	// Create ToolFS instance
	fs := toolfs.NewToolFS("/toolfs")

	// Example 1: Register code skill
	fmt.Println("1. 注册代码 Skill")
	dataProcessor := &DataProcessorSkill{
		name:    "data-processor",
		version: "1.0.0",
	}

	codeSkill, err := fs.RegisterCodeSkill(dataProcessor, "/toolfs/skills/data-processor")
	if err != nil {
		log.Printf("注册代码技能失败: %v", err)
	} else {
		fmt.Printf("   ✓ 注册成功: %s [%s]\n", codeSkill.Name, codeSkill.Type)
	}

	// Example 2: Register another code skill
	fmt.Println("\n2. 注册更多代码 Skills")
	mlAnalyzer := &MLAnalyzerSkill{
		name:    "ml-analyzer",
		version: "2.0.0",
	}

	mlSkill, err := fs.RegisterCodeSkill(mlAnalyzer, "/toolfs/skills/ml-analyzer")
	if err != nil {
		log.Printf("注册代码技能失败: %v", err)
	} else {
		fmt.Printf("   ✓ 注册成功: %s [%s]\n", mlSkill.Name, mlSkill.Type)
	}

	// 示例 3: 列出所有 Skills（不区分类型）
	fmt.Println("\n3. 列出所有 Skills")
	allSkills := fs.ListSkills()
	fmt.Printf("   共 %d 个 skills:\n", len(allSkills))
	for _, skill := range allSkills {
		fmt.Printf("   - %s [%s] @ %s\n", skill.Name, skill.Type, skill.Path)
	}

	// 示例 4: 按类型过滤
	fmt.Println("\n4. 按类型过滤 Skills")
	codeSkills := fs.ListSkillsByType(toolfs.SkillTypeCode)
	fmt.Printf("   插件类型 skills: %d 个\n", len(codeSkills))
	for _, skill := range codeSkills {
		fmt.Printf("   - %s (v%s)\n", skill.Name, skill.Metadata["version"])
	}

	// 示例 5: 初始化 Skills
	fmt.Println("\n5. 初始化 Skills")
	config := map[string]interface{}{
		"timeout":      5000,
		"memory_limit": 1024 * 1024,
	}

	for _, skill := range codeSkills {
		err := fs.InitializeSkill(skill.Name, config)
		if err != nil {
			log.Printf("   初始化 %s 失败: %v", skill.Name, err)
		} else {
			fmt.Printf("   ✓ 初始化成功: %s\n", skill.Name)
		}
	}

	// 示例 6: 执行 Skills
	fmt.Println("\n6. 执行 Skills")
	input := []byte(`{"operation": "process", "data": "test data"}`)

	result, err := fs.ExecuteSkill("data-processor", input, nil)
	if err != nil {
		log.Printf("   执行失败: %v", err)
	} else {
		fmt.Printf("   ✓ 执行结果: %s\n", string(result))
	}

	// 示例 7: 挂载 Skills
	fmt.Println("\n7. 挂载 Skills 到 ToolFS")
	for _, skill := range codeSkills {
		err := fs.MountSkill(skill)
		if err != nil {
			log.Printf("   挂载 %s 失败: %v", skill.Name, err)
		} else {
			fmt.Printf("   ✓ 已挂载: %s -> %s\n", skill.Name, skill.Path)
		}
	}

	// 示例 8: 获取特定 Skill 的详细信息
	fmt.Println("\n8. 获取 Skill 详细信息")
	skill, err := fs.GetSkill("data-processor")
	if err != nil {
		log.Printf("   获取失败: %v", err)
	} else {
		fmt.Printf("   名称: %s\n", skill.Name)
		fmt.Printf("   类型: %s\n", skill.Type)
		fmt.Printf("   路径: %s\n", skill.Path)
		fmt.Printf("   描述: %s\n", skill.Description)
		if skill.Document != nil {
			fmt.Printf("   文档: %s\n", skill.Document.Description)
		}
	}

	fmt.Println("\n=== 演示完成 ===")
}

// DataProcessorSkill 是一个示例插件
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
	fmt.Printf("   [%s] 配置已加载\n", p.name)
	return nil
}

func (p *DataProcessorSkill) Execute(input []byte) ([]byte, error) {
	// 解析输入
	var req map[string]interface{}
	json.Unmarshal(input, &req)

	// 处理数据
	result := map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("已处理: %v", req["data"]),
		"code":    p.name,
	}

	return json.Marshal(result)
}

// GetSkillDocument 实现 SkillDocumentProvider 接口
func (p *DataProcessorSkill) GetSkillDocument() string {
	return `---
name: data-processor
description: 数据处理插件。当用户需要对原始数据进行清洗、转换或格式化时使用此技能，例如：“帮我清洗这段数据”、“将此 JSON 转换为 CSV” 或 “处理最近的日志文件”。
version: 1.0.0
---

# Data Processor

这是一个数据处理插件，可以处理各种数据格式。

## 功能

- 数据转换
- 数据验证
- 数据清洗
`
}

// MLAnalyzerSkill 是另一个示例插件
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
	fmt.Printf("   [%s] 配置已加载\n", p.name)
	return nil
}

func (p *MLAnalyzerSkill) Execute(input []byte) ([]byte, error) {
	result := map[string]interface{}{
		"success": true,
		"message": "ML 分析完成",
		"code":    p.name,
	}
	return json.Marshal(result)
}

func (p *MLAnalyzerSkill) GetSkillDocument() string {
	return `---
name: ml-analyzer
description: 机器学习分析插件。当用户需要执行预测、分类或复杂数据模式识别时使用此技能，例如：“分析这些数据的趋势”、“对这些用户进行分类” 或 “预测下个月的销售额”。
version: 2.0.0
---

# ML Analyzer

机器学习分析工具。

## 功能

- 模型推理
- 数据分析
- 特征提取
`
}
