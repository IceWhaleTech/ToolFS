# ToolFS 统一 Skill 设计

## 核心理念

**Skill 就是 Skill，Skill 就是 Skill**

不再区分 "code" 和 "external skill"，所有功能扩展都是 **Skill**。

## 统一架构

### Skill 的三种实现方式

1. **Filesystem Skill** - 基于文件系统的技能
   - 包含 `SKILL.md`、`references/`、`scripts/` 等
   - 通过文件系统提供功能

2. **Code Skill** - 基于代码的技能（原 Skill）
   - 实现 `SkillExecutor` 接口
   - 可以是 WASM 或 native 代码
   - 通过 `Execute()` 方法提供功能

3. **Builtin Skill** - 内置技能
   - ToolFS 核心功能（Memory、RAG 等）
   - 预先注册和加载

### 统一的 Skill 结构

```go
type Skill struct {
    Name        string
    Description string
    Type        SkillType  // filesystem, code, builtin
    Path        string     // Virtual mount path
    
    // Filesystem-based
    BasePath       string
    SkillMdPath    string
    ReferencesPath string
    ScriptsPath    string
    
    // Code-based 
    Executor SkillExecutor  // 执行接口
    
    // Common
    Metadata map[string]interface{}
    Document *SkillDocument
}
```

### SkillExecutor 接口（统一的执行接口）

```go
type SkillExecutor interface {
    Name() string
    Version() string
    Init(config map[string]interface{}) error
    Execute(input []byte) ([]byte, error)
}
```

这个接口就是原来的 `SkillExecutor` 接口，但现在它是 Skill 的一部分。

## 使用场景

### 场景 1: 注册文件系统 Skill

```go
fs := toolfs.NewToolFS("/toolfs")

// 注册单个文件系统 skill
skill, _ := fs.RegisterSkill("/path/to/my-skill")

// 批量加载
fs.LoadSkillsFromDirectory("./skills")
```

### 场景 2: 注册代码 Skill（原 Skill）

```go
// 创建一个代码 skill
type MyCodeSkill struct {
    name    string
    version string
}

func (s *MyCodeSkill) Name() string { return s.name }
func (s *MyCodeSkill) Version() string { return s.version }
func (s *MyCodeSkill) Init(config map[string]interface{}) error { return nil }
func (s *MyCodeSkill) Execute(input []byte) ([]byte, error) {
    // 执行逻辑
    return []byte("result"), nil
}

// 注册
skill := &MyCodeSkill{name: "data-processor", version: "1.0.0"}
fs.RegisterCodeSkill(skill, "/toolfs/skills/data-processor")
```

### 场景 3: 统一管理

```go
// 列出所有 skills（不区分类型）
allSkills := fs.ListSkills()

// 按类型过滤
filesystemSkills := fs.ListSkillsByType(SkillTypeFilesystem)
codeSkills := fs.ListSkillsByType(SkillTypeCode)

// 执行 skill（统一接口）
result, _ := fs.ExecuteSkill("my-skill", input)
```

## 优势

1. **概念统一**: 不再有 skill vs skill 的困惑
2. **接口统一**: 所有扩展都通过相同的方式管理
3. **简化代码**: 减少重复的管理逻辑
4. **灵活性**: 可以轻松混合不同类型的 skills
5. **易于理解**: 用户只需要理解 "Skill" 这一个概念

## 实现计划

1. ✅ 统一 Skill 数据结构
2. ✅ 重命名 `SkillExecutor` 为 `SkillExecutor`（或保留兼容）
3. ✅ 合并 SkillExecutorManager 到 SkillRegistry
4. ✅ 统一注册接口
5. ✅ 统一执行接口
6. ✅ 更新文档和示例

## 向后兼容

为了保持向后兼容，可以：

```go
// SkillExecutor 系统已经统一
type SkillExecutor = SkillExecutor

// 或者保留旧接口
type SkillExecutor interface {
    SkillExecutor
}
```

## 目录结构示例

```
project/
├── skills/                    # 所有 skills 统一管理
│   ├── data-processor/        # 文件系统 skill
│   │   ├── SKILL.md
│   │   ├── references/
│   │   └── scripts/
│   ├── ml-analyzer.wasm       # WASM code skill
│   └── web-scraper/           # 混合型 skill
│       ├── SKILL.md
│       └── executor.wasm      # 可以同时有文档和代码
└── main.go
```

## API 示例

```go
// 统一的注册接口
fs.RegisterSkill(skillPath)  // 自动检测类型

// 或明确指定类型
fs.RegisterFilesystemSkill(path)
fs.RegisterCodeSkill(executor, mountPath)

// 统一的执行接口
result, err := fs.ExecuteSkill("skill-name", input)

// 统一的查询接口
skill, err := fs.GetSkill("skill-name")
skills := fs.ListSkills()
```
