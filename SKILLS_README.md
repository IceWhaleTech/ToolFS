# ToolFS Skills 系统 - 完整实现

## 概述

ToolFS Skills 系统提供了一个统一的框架来管理和执行各种类型的功能扩展。**Skill 和 Skill 已完全统一**，所有扩展都通过相同的 API 进行管理。

## 核心特性

✅ **统一的概念** - 所有功能扩展都是 "Skill"  
✅ **统一的接口** - 单一的 API 管理所有类型  
✅ **统一的执行** - 一致的执行接口  
✅ **灵活的类型** - 支持文件系统、插件、内置三种类型  
✅ **简单易用** - 直观的 API 设计  

## 快速开始

### 1. 注册文件系统 Skill

```go
import toolfs "github.com/IceWhaleTech/toolfs"

fs := toolfs.NewToolFS("/toolfs")

// 注册单个文件系统 skill
skill, err := fs.RegisterFilesystemSkill("/path/to/my-skill")

// 批量加载
loadedSkills, err := fs.LoadSkillsFromDirectory("./skills")
```

### 2. 注册插件 Skill

```go
// 创建插件
type MySkill struct {
    name    string
    version string
}

func (p *MySkill) Name() string { return p.name }
func (p *MySkill) Version() string { return p.version }
func (p *MySkill) Init(config map[string]interface{}) error { return nil }
func (p *MySkill) Execute(input []byte) ([]byte, error) {
    return []byte("result"), nil
}

// 注册为 skill
skill := &MySkill{name: "my-skill", version: "1.0.0"}
skill, err := fs.RegisterCodeSkill(skill, "/toolfs/skills/my-skill")
```

### 3. 统一管理

```go
// 列出所有 skills
allSkills := fs.ListSkills()

// 按类型过滤
codeSkills := fs.ListSkillsByType(toolfs.SkillTypeCode)
filesystemSkills := fs.ListSkillsByType(toolfs.SkillTypeFilesystem)

// 获取特定 skill
skill, err := fs.GetSkill("my-skill")

// 执行 skill
result, err := fs.ExecuteSkill("my-skill", inputData, session)

// 初始化 skill
err = fs.InitializeSkill("my-skill", config)

// 挂载 skill
err = fs.MountSkill(skill)
```

## Skill 类型

### 1. Filesystem Skill (文件系统技能)

基于文件系统的技能，包含：
- `SKILL.md` (必需) - 技能描述文档
- `references/` (可选) - 参考文档
- `scripts/` (可选) - 相关脚本

**目录结构:**
```
my-skill/
├── SKILL.md
├── references/
│   └── README.md
└── scripts/
    └── run.sh
```

**SKILL.md 格式:**
```markdown
---
name: my-skill
description: 技能描述
version: 1.0.0
---

# My Skill

详细说明...
```

### 2. Code Skill (插件技能)

基于代码的技能，实现 `SkillExecutor` 接口：

```go
type SkillExecutor interface {
    Name() string
    Version() string
    Init(config map[string]interface{}) error
    Execute(input []byte) ([]byte, error)
}
```

可选实现 `SkillDocumentProvider` 接口提供文档：

```go
type SkillDocumentProvider interface {
    GetSkillDocument() string
}
```

### 3. Builtin Skill (内置技能)

ToolFS 核心功能，如 Memory、RAG、Filesystem 等。

## API 参考

### SkillRegistry 方法

```go
// 注册
RegisterSkill(skill *Skill) error
RegisterFilesystemSkill(basePath string) (*Skill, error)
RegisterCodeSkill(skill SkillExecutor, mountPath string) (*Skill, error)
RegisterBuiltinSkill(name, path string) (*Skill, error)

// 查询
GetSkill(name string) (*Skill, error)
GetSkillByPath(path string) (*Skill, error)
ListSkills() []*Skill
ListSkillsByType(skillType SkillType) []*Skill
ListSkillNames() []string

// 管理
UnregisterSkill(name string) error
LoadSkillsFromDirectory(dirPath string) ([]string, error)

// 执行
ExecuteSkill(name string, input []byte, session *Session) ([]byte, error)
InitializeSkill(name string, config map[string]interface{}) error

// 配置
ExportSkillsJSON() ([]byte, error)
ImportSkillsJSON(data []byte) error
```

### ToolFS 便捷方法

所有 `SkillRegistry` 的方法都可以直接在 `ToolFS` 实例上调用：

```go
fs.RegisterFilesystemSkill(path)
fs.RegisterCodeSkill(skill, mountPath)
fs.ListSkills()
fs.ExecuteSkill(name, input, session)
fs.MountSkill(skill)
// ... 等等
```

## 数据结构

### Skill

```go
type Skill struct {
    Name        string                 // 技能名称
    Description string                 // 描述
    Type        SkillType              // filesystem, skill, builtin
    Path        string                 // ToolFS 虚拟路径
    BasePath    string                 // 文件系统技能的物理路径
    Metadata    map[string]interface{} // 元数据
    Document    *SkillDocument         // 文档
    
    // 文件系统技能专用
    SkillMdPath    string
    ReferencesPath string
    ScriptsPath    string
    
    // 插件技能专用
    Code SkillExecutor
}
```

### SkillType

```go
type SkillType string

const (
    SkillTypeFilesystem SkillType = "filesystem"
    SkillTypeCode     SkillType = "code"
    SkillTypeBuiltin    SkillType = "builtin"
)
```

## 使用场景

### 场景 1: 项目启动时加载 Skills

```go
func initializeApp() *toolfs.ToolFS {
    fs := toolfs.NewToolFS("/toolfs")
    
    // 加载文件系统 skills
    fs.LoadSkillsFromDirectory("./project-skills")
    
    // 注册插件 skills
    skill := &MySkill{}
    fs.RegisterCodeSkill(skill, "/toolfs/skills/my-skill")
    
    return fs
}
```

### 场景 2: 动态管理 Skills

```go
// 列出所有 skills
for _, skill := range fs.ListSkills() {
    fmt.Printf("%s [%s]\n", skill.Name, skill.Type)
}

// 按类型操作
codeSkills := fs.ListSkillsByType(toolfs.SkillTypeCode)
for _, skill := range codeSkills {
    fs.InitializeSkill(skill.Name, config)
    fs.MountSkill(skill)
}
```

### 场景 3: 统一执行接口

```go
// 不需要关心 skill 类型
skills := []string{"skill-a", "skill-b", "fs-skill"}

for _, name := range skills {
    result, err := fs.ExecuteSkill(name, input, session)
    // 处理结果
}
```

### 场景 4: 配置导入导出

```go
// 导出配置
registry := fs.GetSkillRegistry()
jsonData, _ := registry.ExportSkillsJSON()
os.WriteFile("skills.json", jsonData, 0644)

// 导入配置
configData, _ := os.ReadFile("skills.json")
registry.ImportSkillsJSON(configData)
```

## 示例代码

完整的示例代码位于：

- `cmd/unified-skill-demo/main.go` - 统一 API 演示
- `cmd/skill-demo/main.go` - 基础功能演示
- `examples/skill_registration_example.go` - 注册示例

运行演示：

```bash
go run ./cmd/unified-skill-demo/main.go
```

## 文档

- `SKILLS_FINAL_SUMMARY.md` - 完整实现总结
- `SKILL_UNIFIED_DESIGN.md` - 统一设计文档
- `SKILLS_API.md` - API 详细文档
- `SKILLS_IMPLEMENTATION.md` - 实现细节

## 测试

```bash
# 运行所有测试
go test ./...

# 运行 skill 相关测试
go test -v -run TestSkill
```

## 项目结构

```
ToolFS/
├── skill_api.go              # 统一的 Skill API
├── skill_api_test.go         # 测试
├── skill_doc.go              # 文档管理
├── skill.go                 # SkillExecutor 接口
├── cmd/
│   ├── unified-skill-demo/   # 统一 API 演示
│   └── skill-demo/           # 基础演示
├── examples/
│   └── skill_registration_example.go
├── skills/                   # 内置 skills
│   ├── SKILL.md
│   ├── memory/
│   ├── rag/
│   └── ...
└── docs/
    ├── SKILLS_FINAL_SUMMARY.md
    ├── SKILL_UNIFIED_DESIGN.md
    └── SKILLS_API.md
```

## 最佳实践

1. **组织 Skills**
   - 按功能分类
   - 使用清晰的命名
   - 提供完整的文档

2. **版本管理**
   - 在 SKILL.md 或 skill 中指定版本
   - 使用语义化版本
   - 记录变更历史

3. **插件开发**
   - 实现 `SkillExecutor` 接口
   - 可选实现 `SkillDocumentProvider`
   - 提供清晰的错误信息

4. **错误处理**
   - 检查所有返回的错误
   - 提供有意义的错误消息
   - 实现回退机制

5. **性能优化**
   - 缓存常用的 skill 查询
   - 延迟初始化
   - 批量操作

## 向后兼容

保留了原有的 `SkillExecutor` 接口和相关功能，现有的插件代码无需修改即可作为 Skill 使用。

## 贡献

欢迎贡献新的 skills 或改进现有功能！

## 许可

查看 LICENSE 文件了解详情。
