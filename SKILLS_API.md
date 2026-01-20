# ToolFS Skills API

ToolFS 提供了一个灵活的 Skills 系统，允许不同项目注入和管理自定义技能。Skills 可以是文件系统中的目录结构、WASM 插件或内置功能。

## 概述

### Skill 类型

1. **Filesystem Skills (外部技能)**
   - 基于文件系统的技能目录
   - 包含 `SKILL.md`、`references/`、`scripts/` 等
   - 适合项目特定的工具和脚本

2. **Code Skills (插件技能)**
   - WASM 或原生插件
   - 实现 `SkillExecutor` 接口
   - 可选实现 `SkillDocumentProvider` 提供文档

3. **Builtin Skills (内置技能)**
   - ToolFS 内嵌的核心功能
   - Memory、RAG、Filesystem 等

## Skill 目录结构

每个外部 skill 应该遵循以下目录结构：

```
my-skill/
├── SKILL.md          # 必需：技能描述文档
├── references/       # 可选：参考文档和资源
│   └── README.md
└── scripts/          # 可选：相关脚本
    └── run.sh
```

### SKILL.md 格式

```markdown
---
name: my-skill
description: 技能简短描述
version: 1.0.0
author: your-name
metadata:
  category: data-processing
  tags: [ml, data]
---

# My Skill

详细的技能说明文档...

## 功能特性

- 特性 1
- 特性 2

## 使用方法

使用示例和说明...
```

## API 使用

### 1. 注册单个外部 Skill

```go
import "github.com/IceWhaleTech/ToolFS"

fs := toolfs.NewToolFS("/toolfs")

// 注册单个 skill
skill, err := fs.RegisterFilesystemSkill("/path/to/my-skill")
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Registered: %s\n", skill.Name)
```

### 2. 批量加载 Skills 目录

```go
// 从目录加载所有 skills
// 目录结构：
// /path/to/skills/
//   ├── skill1/
//   │   └── SKILL.md
//   └── skill2/
//       └── SKILL.md

loadedSkills, err := fs.LoadSkillsFromDirectory("/path/to/skills")
if err != nil {
    log.Printf("Some skills failed to load: %v", err)
}

fmt.Printf("Loaded %d skills: %v\n", len(loadedSkills), loadedSkills)
```

### 3. 查询 Skills

```go
// 列出所有 skills
allSkills := fs.ListSkills()
for _, skill := range allSkills {
    fmt.Printf("- %s: %s [%s]\n", 
        skill.Name, 
        skill.Description, 
        skill.Type)
}

// 获取特定 skill
skill, err := fs.GetSkill("my-skill")
if err != nil {
    log.Fatal(err)
}

// 访问 skill 文档
if skill.Document != nil {
    fmt.Println(skill.Document.Content)
}
```

### 4. 将 Skill 注册为 Skill

```go
// 创建插件
skill := &MySkill{
    name:    "data-processor",
    version: "1.0.0",
}

// 注册到 skill manager
executorManager := toolfs.NewSkillExecutorManager()
fs.SetSkillExecutorManager(executorManager)
executorManager.InjectSkill(skill, nil, nil)

// 将 skill 注册为 skill
registry := fs.GetSkillRegistry()
if registry == nil {
    registry = toolfs.NewSkillRegistry(fs.GetSkillDocumentManager())
    fs.AddSkillExecutorRegistry(registry)
}

regSkill, err := registry.RegisterCodeSkill(
    skill, 
    "/toolfs/skills/data-processor",
)
```

### 5. 导出和导入 Skills 配置

```go
// 导出配置
registry := fs.GetSkillRegistry()
jsonData, err := registry.ExportSkillsJSON()
if err != nil {
    log.Fatal(err)
}

// 保存到文件
os.WriteFile("skills-config.json", jsonData, 0644)

// 导入配置
newRegistry := toolfs.NewSkillRegistry(nil)
err = newRegistry.ImportSkillsJSON(jsonData)
```

## 项目特定 Skills 注入

不同项目可以注入不同的 skills：

### 示例：AI/ML 项目

```go
// ai-project/main.go
fs := toolfs.NewToolFS("/toolfs")

// 加载 AI 相关 skills
fs.LoadSkillsFromDirectory("./skills/ml")

// 注册特定插件
mlSkill := &MLInferenceSkill{}
registry.RegisterCodeSkill(mlSkill, "/toolfs/ml/inference")
```

### 示例：Web 开发项目

```go
// web-project/main.go
fs := toolfs.NewToolFS("/toolfs")

// 加载 Web 相关 skills
fs.LoadSkillsFromDirectory("./skills/web")

// 注册特定插件
scraperSkill := &WebScraperSkill{}
registry.RegisterCodeSkill(scraperSkill, "/toolfs/web/scraper")
```

## SkillRegistry API 参考

### 核心方法

```go
type SkillRegistry struct {
    // ...
}

// 注册 skill
func (sr *SkillRegistry) RegisterSkill(info *Skill) error

// 注册外部 skill
func (sr *SkillRegistry) RegisterFilesystemSkill(basePath string) (*Skill, error)

// 注册内置 skill
func (sr *SkillRegistry) RegisterBuiltinSkill(name, path string) (*Skill, error)

// 注册插件 skill
func (sr *SkillRegistry) RegisterCodeSkill(skill SkillExecutor, mountPath string) (*Skill, error)

// 取消注册 skill
func (sr *SkillRegistry) UnregisterSkill(name string) error

// 获取 skill
func (sr *SkillRegistry) GetSkill(name string) (*Skill, error)

// 通过路径获取 skill
func (sr *SkillRegistry) GetSkillByPath(path string) (*Skill, error)

// 列出所有 skills
func (sr *SkillRegistry) ListSkills() []*Skill

// 列出 skill 名称
func (sr *SkillRegistry) ListSkillNames() []string

// 从目录加载 skills
func (sr *SkillRegistry) LoadSkillsFromDirectory(dirPath string) ([]string, error)

// 获取外部 skill 配置
func (sr *SkillRegistry) GetExternalSkill(name string) (*ExternalSkill, error)

// 获取 skill 文档
func (sr *SkillRegistry) GetSkillDocument(name string) (*SkillDocument, error)

// 导出为 JSON
func (sr *SkillRegistry) ExportSkillsJSON() ([]byte, error)

// 从 JSON 导入
func (sr *SkillRegistry) ImportSkillsJSON(data []byte) error
```

### ToolFS 便捷方法

```go
// ToolFS 提供的便捷方法
func (fs *ToolFS) RegisterSkill(info *Skill) error
func (fs *ToolFS) RegisterFilesystemSkill(basePath string) (*Skill, error)
func (fs *ToolFS) LoadSkillsFromDirectory(dirPath string) ([]string, error)
func (fs *ToolFS) ListSkills() []*Skill
func (fs *ToolFS) GetSkill(name string) (*Skill, error)
func (fs *ToolFS) AddSkillExecutorRegistry(registry *SkillRegistry)
func (fs *ToolFS) GetSkillRegistry() *SkillRegistry
```

## 数据结构

### Skill

```go
type Skill struct {
    Name        string                 `json:"name"`
    Description string                 `json:"description"`
    Path        string                 `json:"path"`         // ToolFS 虚拟路径
    Type  string                 `json:"source_type"`  // "builtin", "code", "external"
    SourcePath  string                 `json:"source_path"`  // 物理路径或插件名
    Metadata    map[string]interface{} `json:"metadata"`
    Document    *SkillDocument         `json:"document,omitempty"`
}
```

### ExternalSkill

```go
type ExternalSkill struct {
    Name           string                 `json:"name"`
    Description    string                 `json:"description"`
    BasePath       string                 `json:"base_path"`       // 基础目录
    SkillMdPath    string                 `json:"skill_md_path"`   // SKILL.md 路径
    ReferencesPath string                 `json:"references_path"` // references 目录
    ScriptsPath    string                 `json:"scripts_path"`    // scripts 目录
    Metadata       map[string]interface{} `json:"metadata"`
}
```

## 最佳实践

1. **组织 Skills**
   - 按功能分类组织 skills 目录
   - 使用清晰的命名约定
   - 提供完整的 SKILL.md 文档，**在 description 中明确说明触发场景**（例如：提及用户在问到什么问题或什么特定场景下触发该技能）。

2. **版本管理**
   - 在 SKILL.md 中指定版本号
   - 使用语义化版本控制
   - 记录变更历史

3. **项目集成**
   - 在项目启动时加载所需 skills
   - 使用配置文件管理 skill 列表
   - 支持环境特定的 skills

4. **插件作为 Skills**
   - 实现 `SkillDocumentProvider` 接口
   - 提供清晰的文档
   - 遵循插件最佳实践

5. **错误处理**
   - 优雅处理 skill 加载失败
   - 记录详细的错误信息
   - 提供回退机制

## 示例项目结构

```
my-project/
├── main.go
├── skills/                    # 项目特定 skills
│   ├── data-processor/
│   │   ├── SKILL.md
│   │   ├── references/
│   │   └── scripts/
│   └── custom-analyzer/
│       ├── SKILL.md
│       └── scripts/
├── skills/                   # 插件目录
│   ├── ml-inference.wasm
│   └── data-transformer.wasm
└── skills-config.json         # Skills 配置
```

## 完整示例

参见 `examples/skill_registration_example.go` 获取完整的使用示例。

## 相关文档

- [Skill API](skill.go) - 插件接口定义
- [Skill Documents](skill_doc.go) - Skill 文档管理
- [Built-in Skills](skills/) - 内置 skills 文档
