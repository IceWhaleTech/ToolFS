# ToolFS 技术方案：RBAC 访问控制 + 文件快照/闪回

> 状态：设计稿阶段（v1，已被 v5 取代；仅供追溯，勿据此实施）
> 日期：2026-07-23
> 关联系统：[ZimaOS-UserService](https://github.com/IceWhaleTech/ZimaOS-UserService)（用户/角色数据源与 JWT 签发方）

## 1. 背景与目标

ToolFS 定位为**给 AI agent 的统一、安全的文件访问工具**。当前 agent 直接操作文件系统存在两类核心风险：

- **误删/误改**：agent 执行 `rm`、覆盖写等破坏性操作后无法恢复；
- **越权**：agent 可访问、修改不属于当前用户权限范围内的路径。

本方案在 ToolFS 现有能力（虚拟命名空间、Session、审计、FUSE 挂载）之上补齐三块能力：

1. **RBAC 访问控制**：用户与角色数据来自 ZimaOS-UserService 签发的 JWT；ToolFS 侧维护"角色 → 路径 → 操作"的策略配置，在每次文件操作前强制校验。
2. **文件快照与闪回（Flashback）**：以 btrfs 文件系统级快照为主、copy-based 为降级方案；破坏性操作前自动保护（回收站/版本目录），支持按文件、按时间点、按快照恢复。
3. **VFS 层命名空间隔离**：保持 Go 库 + FUSE 形态，通过 VFS 挂载目录，在 VFS 层完成权限校验和按用户的命名空间分配（每个用户/会话看到不同的文件视图）。

### 1.1 现状与差距（已核实）

| 现状 | 差距 |
|---|---|
| `Session` 仅有 `AllowedPaths` 路径前缀白名单（空 = 全放行），`toolfs.go:97` | 无用户/角色概念；`IsPathAllowed` 纯 `HasPrefix`，存在 `/data` 误匹配 `/database` 的边界缺陷 |
| 只有 Read/Write/List/Stat API | **没有** Delete/Rename/Mkdir 公共 API，破坏性操作能力空白 |
| FUSE 层只实现 Read/Write/Lookup/Readdir/Getattr/Open | 无 Unlink/Rename/Mkdir/Create；`MountToolFSWithSession`（`fuse_adapter.go:326`）是空壳，**FUSE 路径完全绕过访问控制** |
| 快照 `CreateSnapshot`/`RollbackSnapshot` 为内存态 CoW | 进程重启即丢失，非文件系统级 |
| 审计 `StdoutAuditLogger`，`AccessDenied` 用字符串 `Contains("access denied")` 判定 | 无身份字段、不落盘、判定脆弱 |
| `fs.sessions`/`fs.snapshots` 为裸 map | 无并发保护 |

### 1.2 UserService 侧事实（集成契约）

- JWT：**ES256（ECDSA P-256）**，claims 为 `{username, role, id}` + RegisteredClaims；access token issuer `casaos`、有效期 **3h**；refresh token issuer `refresh`、7 天。
- 角色仅 `admin` / `user` 两个字符串值，**无权限表**（因此策略放 ToolFS 侧）。
- 密钥对在 UserService **进程启动时内存生成**（重启即轮换），通过 **JWKS 公钥端点**暴露供其他服务离线验签；服务地址 `127.0.0.1:37815`，写入 runtime 地址文件供发现。

## 2. 总体架构

保持扁平单包 `package toolfs`，**扩展 `Session` 而非推翻**；所有新能力通过接口注入，零配置时行为与现状完全一致（现有测试不改一行仍全绿）。

```
                          ┌────────────────────────────────────────────────┐
  JWT (agent 持有)         │                    ToolFS                      │
       │                  │   ┌──────────┐      ┌─────────────────┐        │
       ▼                  │   │  RBAC    │      │ SnapshotManager │        │
  TokenVerifier ─────────►│   │ Enforcer │      └──────┬──────────┘        │
  (JWKS / Static / Mock)  │   └────┬─────┘             │  SnapshotBackend  │
       │                  │        ▼                   ├─ BtrfsBackend(linux)
       ▼                  │  CheckAccess(path, op)     ├─ CopyBackend(通用)│
    Session ─────────────►│        │                   └─ MemoryBackend    │
  {Identity, Enforcer}    │        ▼                                       │
                          │  *WithSession APIs ──► 保护钩子 ──► Trash 回收站 │
                          │        │              (删除/覆盖前自动保留旧版本) │
                          │        ▼                                       │
                          │  执行 ──► 审计 AuditLogger (JSONL 落盘, 含身份)  │
                          └────────▲───────────────────────────────────────┘
                                   │
                     FUSE per-session 节点树（命名空间隔离，无权路径隐藏）
```

**请求链路**（所有入口收敛到同一条流水线）：

```
CheckAccess(RBAC/legacy) → resolvePath → mount 类型/只读校验 → 保护钩子(trash/snapshot)
→ 执行 → 审计(含身份与保护凭据) → TrackChange
```

### 2.1 新增文件

| 文件 | 内容 |
|---|---|
| `rbac.go` | `Operation`/`Effect`/`PathRule`/`Policy`、`PolicyEnforcer`、匹配算法 |
| `rbac_config.go` | JSON/YAML 策略文件加载、校验、`{username}` 等变量展开 |
| `auth.go` | `Identity`、`TokenVerifier` 接口、Session 绑定逻辑 |
| `auth_jwks.go` | `JWKSVerifier`：JWKS 拉取/缓存/kid-miss 强制刷新、ES256 验签 |
| `auth_mock.go` | `StaticKeyVerifier`、`NewInsecureMockVerifier`（开发/测试用，显式注入才生效） |
| `destructive.go` | `DeleteFile`/`RemoveAll`/`Rename`/`Mkdir` 的 WithSession API |
| `trash.go` | 回收站（entries + versions）：进入/列出/恢复/清理 |
| `snapshot_backend.go` | `SnapshotBackend` 接口、`SnapshotManager`、保留策略/Prune、闪回 API |
| `snapshot_copy.go` | `CopyBackend`：磁盘持久化（hardlink/clonefile 优化）+ `index.json` |
| `snapshot_btrfs.go` / `snapshot_btrfs_stub.go` | `BtrfsBackend`（`//go:build linux`）/ 非 Linux stub |
| `audit_file.go` | `FileAuditLogger`：JSONL 落盘，按日切分 |
| 对应 `*_test.go` | 见第 11 节测试策略 |

### 2.2 修改的现有文件

- **`toolfs.go`**：`Session` 增加 `Identity`/`Enforcer`/`ExpiryMode` 字段；新增 `CheckAccess(path, op)` 统一判定入口；4 个现有 `*WithSession` API 接入；`WriteFileWithSession` 覆盖写前旧版本进 versions；`sessions`/`snapshots` map 加 `sync.RWMutex`；旧快照 API 内部委托 `SnapshotManager`（签名不变）；`ListDir` 隐藏 `.toolfs-trash/`、`.toolfs-snapshots/`。
- **`fuse_adapter.go`**：所有节点携带 `session`；`MountToolFSWithSession` 实现真实逻辑；补全 `Unlink/Rmdir/Rename/Mkdir/Create/Setattr`；`Readdir` 按权限过滤；错误映射 EACCES/EROFS/ENOENT。
- **`go.mod`**：新增 `github.com/golang-jwt/jwt/v5`、`github.com/bmatcuk/doublestar/v4`、`gopkg.in/yaml.v3`（均为纯 Go 轻依赖，无 CGO）。

## 3. RBAC 模型设计

### 3.1 核心类型

```go
type Operation uint8

const (
    OpRead    Operation = 1 << iota // 读文件内容 / Stat
    OpList                          // 列目录（独立于 Read，支持"可见不可读"）
    OpWrite                         // 创建 / 修改 / truncate
    OpDelete                        // 删除 / rename 源 / rename 覆盖目标
    OpExecute                       // ExecuteCommandWithSession / 代码 skill
)

type OpSet uint8 // 位集合，JSON 序列化为 ["read","write",...]

type Effect string

const (
    EffectAllow Effect = "allow"
    EffectDeny  Effect = "deny"
)

type PathRule struct {
    Pattern string `json:"pattern"` // 前缀（"/data/"）或 glob（"/data/**/*.log"）
    Effect  Effect `json:"effect"`
    Ops     OpSet  `json:"ops"`
}

type Policy struct {
    Version         int                   `json:"version"`
    DefaultEffect   Effect                `json:"default_effect"`    // 默认 "deny"
    AdminFullAccess bool                  `json:"admin_full_access"` // 默认 true
    Roles           map[string][]PathRule `json:"roles"`             // "admin" / "user"
    Users           map[string][]PathRule `json:"users"`             // 按 UserID，优先于角色规则
}

type Decision struct {
    Allowed bool
    Rule    *PathRule // 命中的规则（审计用）
    Reason  string
}

func NewPolicyEnforcer(p *Policy) (*PolicyEnforcer, error)
func LoadPolicyFile(path string) (*Policy, error) // .json / .yaml 双轨
func (e *PolicyEnforcer) Decide(id *Identity, path string, op Operation) Decision
```

### 3.2 匹配算法

1. 路径先经 `normalizeVirtualPath`（复用现有函数，与 `resolvePath` 同一套规范化，防 `../` 逃逸）。
2. **Pattern 语义**：不含 `* ? [` 元字符 → **边界对齐的前缀匹配**（`/data` 匹配 `/data/x`，**不**匹配 `/database`，修复现有 `IsPathAllowed` 纯 `HasPrefix` 的越界缺陷）；含元字符 → `doublestar.Match`（支持 `**`）。
3. **评估顺序**：
   - `role == "admin"` 且 `AdminFullAccess` → 默认 allow，但 `Users[id]`/`Roles["admin"]` 中匹配的**显式 deny 仍然生效**（admin 也可被限制，防误删场景需要，如禁止删固件目录）；
   - 收集 `Users[UserID]` + `Roles[Role]` 中 Pattern 匹配且 Ops 覆盖请求操作的所有规则；
   - **deny 一票否决** → 任一 allow 通过 → 否则 `DefaultEffect`（默认 deny）。
4. **隐含规则**：允许对某路径 `OpWrite`/`OpDelete` 隐含允许其祖先目录 `OpList`（否则 FUSE 下路径不可达）；`Rename(old, new)` = `OpDelete(old) + OpWrite(new)`，目标已存在时额外要求 `OpDelete(new)`。

> 取舍：选 deny-wins 而非"最长前缀优先"——行为可预测、实现简单、安全上保守；两角色两层规则的规模下无性能压力。

### 3.3 策略配置示例

```json
{
  "version": 1,
  "default_effect": "deny",
  "admin_full_access": true,
  "roles": {
    "user": [
      {"pattern": "/toolfs/data/users/{username}/", "effect": "allow", "ops": ["read", "list", "write", "delete"]},
      {"pattern": "/toolfs/data/shared/", "effect": "allow", "ops": ["read", "list"]},
      {"pattern": "/toolfs/data/**/.ssh/**", "effect": "deny", "ops": ["read", "list", "write", "delete", "execute"]}
    ],
    "admin": [
      {"pattern": "/toolfs/data/system/firmware/**", "effect": "deny", "ops": ["delete", "write"]}
    ]
  },
  "users": {}
}
```

策略变量 `{username}`、`{user_id}`、`{role}` 在 **session 创建时**展开为具体规则并缓存在 Session 内（热路径零字符串替换），实现"每个用户只拥有自己的 home"这类模板化策略。

### 3.4 与现有 AllowedPaths 的兼容

```go
// 唯一判定入口：
func (s *Session) CheckAccess(path string, op Operation) error {
    // 1. token 过期检查（见 §4.3）
    // 2. s.Enforcer != nil → RBAC Decide
    // 3. 否则 → 现有 AllowedPaths 前缀语义（空 = 全放行），所有 op 同权
}
```

- `IsPathAllowed` 保留为 `CheckAccess(path, OpRead) == nil` 的包装，不破坏现有调用方与测试；
- 提供 `PolicyFromAllowedPaths(paths []string) *Policy` 迁移辅助（每个前缀生成 allow-all-ops 规则）。

## 4. JWT 集成（对接 ZimaOS-UserService）

### 4.1 类型与验签流程

```go
type Identity struct {
    UserID    string
    Username  string
    Role      string // "admin" | "user"
    ExpiresAt time.Time
    RawClaims map[string]any
}

type TokenVerifier interface {
    Verify(tokenString string) (*Identity, error)
}

type JWKSVerifierConfig struct {
    JWKSEndpoint   string        // 从 UserService runtime 地址文件发现（127.0.0.1:37815）
    RefreshMinGap  time.Duration // 默认 10s，防止坏 token 打爆刷新
    CacheTTL       time.Duration // 默认 1h 定期刷新（可选）
    HTTPTimeout    time.Duration // 默认 3s
    ExpectedIssuer string        // "casaos"
    AllowStaleKeys bool          // UserService 不可用时是否使用缓存公钥，默认 true
}

func NewJWKSVerifier(cfg JWKSVerifierConfig) *JWKSVerifier
```

验签流程：

1. `jwt.Parse` 限定 `WithValidMethods(["ES256"])` + `WithIssuer("casaos")` + exp 校验（防 `alg=none`/HS256 混淆攻击）；
2. keyfunc 按 header `kid` 查本地缓存 `map[kid]*ecdsa.PublicKey`；
3. **kid 未命中或验签失败**（对应 UserService 重启轮换密钥）：距上次刷新超过 `RefreshMinGap` 则强制重拉 JWKS 后重试一次，仍失败 → `ErrTokenInvalid`；
4. JWKS 解析手写（约 60 行：JSON → base64url 解 x/y → `ecdsa.PublicKey`），避免引入 jwx 重依赖；签名验证用 `golang-jwt/jwt/v5`。

### 4.2 Session 扩展与创建 API

```go
type Session struct {
    ID               string
    CreatedAt        time.Time
    AllowedPaths     []string // 保留：legacy 模式
    AuditLogger      AuditLogger
    CommandValidator CommandValidator
    // 新增：
    Identity   *Identity       // nil = legacy 匿名 session
    Enforcer   *PolicyEnforcer // nil = 走 AllowedPaths
    ExpiryMode ExpiryMode      // Strict（默认） / DestructiveOnly / Ignore
    mu         sync.RWMutex    // 保护 token 刷新
}

func (fs *ToolFS) SetTokenVerifier(v TokenVerifier)
func (fs *ToolFS) SetPolicy(p *Policy) error // 全局策略，NewSessionWithToken 自动挂载
func (fs *ToolFS) NewSessionWithToken(sessionID, jwtToken string) (*Session, error)
func (fs *ToolFS) RefreshSessionToken(sessionID, newJWT string) error // 续期不丢 session 状态
```

### 4.3 Token 过期处理（access token 3h）

- `CheckAccess` 每次比较 `Identity.ExpiresAt`（纳秒级开销）；
- 默认 `ExpiryModeStrict`：过期后所有操作返回哨兵错误 `ErrTokenExpired`，调用方（agent 宿主）拿新 access token 调 `RefreshSessionToken` 续期——**session 不销毁**，快照/审计上下文延续；
- 可选 `DestructiveOnly`：过期后读操作放行、写/删拒绝（长任务容忍度更高的场景）。

### 4.4 降级策略

- **UserService 不可用**：本进程曾成功拉过 JWKS 且 `AllowStaleKeys=true` → 用缓存公钥验签（密钥仅在 UserService 重启时轮换，重启后旧 token 本就该失效，安全）；**从未拉到过公钥 → fail closed**，拒绝创建带身份的 session（可由调用方显式决策降级为 legacy AllowedPaths session，绝不自动发生）；
- **macOS 开发**：`StaticKeyVerifier{PublicKey}`（测试自造 ES256 密钥对）与 `NewInsecureMockVerifier(identity)`（跳过验签直给身份，命名明示不安全），仅显式注入生效，绝不作为自动 fallback。

## 5. 破坏性操作 API + 回收站（防误删第一道防线）

### 5.1 库 API（`destructive.go`）

```go
func (fs *ToolFS) DeleteFileWithSession(path string, session *Session) error
func (fs *ToolFS) RemoveAllWithSession(path string, session *Session) error // 递归删目录，同样进回收站
func (fs *ToolFS) RenameWithSession(oldPath, newPath string, session *Session) error
func (fs *ToolFS) MkdirWithSession(path string, session *Session) error     // MkdirAll 语义
// 另提供无 session 包装 DeleteFile/Rename/Mkdir，与 ReadFile 风格一致
```

统一流水线：

```
CheckAccess(op) → resolvePath → 拒绝 virtual/skill mount 上不支持的操作（明确错误）
→ 拒绝 ReadOnly mount → 保护钩子 fs.protect(path, localPath, op, session)
→ 执行 → session.logAudit → fs.TrackChange
```

### 5.2 保护钩子

```go
type ProtectionMode int

const (
    ProtectTrash    ProtectionMode = iota // 默认
    ProtectSnapshot
    ProtectBoth
    ProtectNone
)

type ProtectionPolicy struct {
    Mode                 ProtectionMode
    SnapshotOnDeleteDirs bool // 递归删目录前额外做一次全量快照，默认 true
    TrashRetention       RetentionPolicy
}

func (fs *ToolFS) SetProtectionPolicy(p ProtectionPolicy)
```

- **删除 / RemoveAll**：`os.Rename` 移入同 mount 的回收站（同文件系统原子 rename，零拷贝，远快于复制快照）；
- **Rename 覆盖已存在目标**：目标先进回收站再 rename；
- **覆盖写 / truncate**：旧内容进回收站 `versions/` 子区（即"文件版本"），使覆盖写也可闪回——这是 `WriteFileWithSession` 的增量改动点。

### 5.3 回收站布局与 API（`trash.go`）

```
<mount.LocalPath>/.toolfs-trash/
  ├── entries/<unix_nano>-<rand8>/payload    # 被删文件/目录本体（rename 而来）
  │                             /meta.json   # {virtual_path, op, session_id, user_id, deleted_at, is_dir}
  └── versions/...                           # 覆盖写前的旧版本，同结构
```

```go
type TrashEntry struct {
    ID, VirtualPath, Operation, SessionID, UserID string
    DeletedAt time.Time
    IsDir     bool
}

func (fs *ToolFS) ListTrash(session *Session) ([]TrashEntry, error)
func (fs *ToolFS) RestoreFromTrash(entryID string, session *Session) error // 需目标路径 OpWrite
func (fs *ToolFS) PurgeTrash(olderThan time.Duration) error
```

`.toolfs-trash/`、`.toolfs-snapshots/` 在 `ListDirWithSession` 与 FUSE Readdir/Lookup 中**无条件隐藏**，且 RBAC 引擎内置对这两个前缀的 deny（agent 只能通过上述受控 API 访问）。

### 5.4 FUSE 层补全（`fuse_adapter.go`）

```go
var (
    _ fs.NodeUnlinker  = (*ToolFSDir)(nil)  // Unlink  → DeleteFileWithSession
    _ fs.NodeRmdirer   = (*ToolFSDir)(nil)  // Rmdir   → 仅空目录，否则 ENOTEMPTY
    _ fs.NodeRenamer   = (*ToolFSDir)(nil)  // Rename  → RenameWithSession
    _ fs.NodeMkdirer   = (*ToolFSDir)(nil)  // Mkdir   → MkdirWithSession
    _ fs.NodeCreator   = (*ToolFSDir)(nil)  // Create  → OpWrite 校验 + 占位 + 句柄
    _ fs.NodeSetattrer = (*ToolFSFile)(nil) // Setattr(size=0 truncate) → 视为 OpWrite，旧内容进 versions
)
```

即 agent 在 FUSE 挂载点执行 `rm`/`mv` 也自动走保护链路进回收站。错误映射：`ErrAccessDenied`/`ErrTokenExpired` → `EACCES`；隐藏路径 → `ENOENT`；只读 mount → `EROFS`；不存在 → `ENOENT`（替换现有一律 `EIO` 的粗糙映射）。

## 6. 快照 / 闪回设计

### 6.1 接口（`snapshot_backend.go`）

```go
type SnapshotInfo struct {
    ID        string    `json:"id"`         // "<backend>-<unixnano>"
    Name      string    `json:"name"`       // 用户可读名，兼容旧 API 的 name
    CreatedAt time.Time `json:"created_at"`
    Backend   string    `json:"backend"`    // "btrfs" | "copy" | "memory"
    Trigger   string    `json:"trigger"`    // "manual" | "periodic" | "pre-destructive"
    Target    string    `json:"target"`     // mount 本地路径
    SizeBytes int64     `json:"size_bytes,omitempty"`
}

type SnapshotBackend interface {
    Name() string
    Available(localPath string) bool                            // 运行时探测
    Create(target string, info *SnapshotInfo) error
    Restore(id string, target string) error                     // 整体恢复
    RestoreFile(id string, relPath string, target string) error // 单文件恢复
    List(target string) ([]SnapshotInfo, error)
    Delete(id string) error
}

type RetentionPolicy struct {
    MaxCount int           // 默认 20
    MaxAge   time.Duration // 默认 7d
    MinKeep  int           // 默认 3
}

func NewSnapshotManager(backends ...SnapshotBackend) *SnapshotManager // 按序探测，首个 Available 者胜
func (m *SnapshotManager) Prune() error
```

**闪回 API**（挂在 ToolFS 上）：

```go
func (fs *ToolFS) Flashback(snapshotID string, session *Session) error // 整卷恢复，需 admin / OpWrite+OpDelete
func (fs *ToolFS) FlashbackFile(path string, at time.Time, session *Session) error
// FlashbackFile 检索顺序：versions/ 与 trash 中 ≤at 的最新版本 → 快照（CreatedAt ≤ at 的最新）→ 命中即恢复
func (fs *ToolFS) ListFileVersions(path string, session *Session) ([]FileVersion, error) // 供 agent/UI 选择
```

### 6.2 BtrfsBackend（`snapshot_btrfs.go`，linux only）

- **检测**：`unix.Statfs` 判 `f.Type == BTRFS_SUPER_MAGIC (0x9123683E)`；再以 `btrfs subvolume show <path>` 确认目录本身是子卷。
- **目录非子卷**：**不自动转换**（自动搬移用户数据风险过高）。提供显式管理 API `ConvertToSubvolume(localPath) error`（临时子卷 → rename 数据 → 原子换名），由 ZimaOS 部署脚本/管理员一次性调用；未转换时 Manager 自动降级 CopyBackend 并在日志告警。
- **存放约定**：`<localPath>/.toolfs-snapshots/btrfs/<id>`，创建命令 `btrfs subvolume snapshot -r <localPath> <snapdir>/<id>`（只读快照，CoW 秒级零拷贝）。
- **CLI vs ioctl**：选 **CLI**（`exec.Command("btrfs", ...)`）。ZimaOS 必带 btrfs-progs；ioctl（`BTRFS_IOC_SNAP_CREATE_V2`）实现与测试成本高。接口已抽象，未来可无痛替换。
- **恢复语义**：`RestoreFile` 直接从只读快照目录拷出（reflink：`unix.IoctlFileClone`，零拷贝）；整卷 `Restore` 用**子卷换名**：`mv live live.rollback-<ts>` → `btrfs subvolume snapshot <snap> live`（可写快照落位）→ 旧卷保留一份进 retention。**前提：恢复时无打开的文件句柄**，文档要求调用方先静默会话。

### 6.3 CopyBackend（`snapshot_copy.go`，全平台降级）

- 现有内存快照逻辑改造为**磁盘持久化**：`<localPath>/.toolfs-snapshots/copy/<id>/` 存全量树 + `manifest.json`；
- 空间优化：同文件系统优先 `os.Link`（硬链接）；macOS APFS 用 `unix.Clonefile`；均失败退化 `io.Copy`；
- `.toolfs-snapshots/index.json` 持久化 `[]SnapshotInfo`，**修复进程重启快照列表丢失**。

### 6.4 旧 API 兼容

`CreateSnapshot(name)` / `RollbackSnapshot(name)` / `ListSnapshots` / `DeleteSnapshot` / `GetSnapshot` **签名不动**，内部委托 `SnapshotManager`（name ↔ ID 映射存 index）；现有内存快照逻辑包装为 `MemoryBackend`（测试/纯内存场景）；`SandboxBackend` 接口保留，可包装注册为可选 backend。

### 6.5 自动快照策略

```go
type AutoSnapshotConfig struct {
    PeriodicInterval time.Duration // 0 = 关闭；ZimaOS 建议 1h
    Retention        RetentionPolicy
}

func (fs *ToolFS) StartAutoSnapshot(cfg AutoSnapshotConfig) (stop func())
```

- **破坏性操作前的 per-file 保护 = 回收站/versions（§5）**，不做全量快照（太重）；`SnapshotOnDeleteDirs=true` 时递归删目录前额外做一次全量快照；
- 周期性/手动 = 全量 backend 快照，以 `Trigger` 字段区分；Prune 时 `pre-destructive` 与 `periodic` 分池计数，**手动快照不被自动清理**；
- `PurgeTrash` 定时清理与自动快照同一 goroutine 调度（否则回收站磁盘打满）。

## 7. 按用户命名空间分配（FUSE 视图）

把 `MountToolFSWithSession` 从空壳变为真实现：

```go
type NamespaceMode int

const (
    NamespaceHide   NamespaceMode = iota // 默认：无权路径不可见
    NamespaceEACCES                      // 可见但拒绝访问（admin 调试用）
)

func MountToolFSWithSession(toolfs *ToolFS, mountPoint string, session *Session,
    mode NamespaceMode, options *fuse.MountOptions) error
```

- `ToolFSRoot/ToolFSDir/ToolFSFile/ToolFSFileHandle` 均增加 `session` 字段，所有 handler 转调 `*WithSession(…, n.session)`（session 为 nil 时行为同现状，`MountToolFS` 不受影响）；
- **Readdir**：对每个 entry 调 `CheckAccess(child, OpList)`，`NamespaceHide` 下无权项直接剔除（user 角色不泄露目录结构）；`Lookup` 对无权路径返回 `ENOENT`；
- **每用户独立视图** = 每 session 一次 mount（如 `/mnt/agent/<session_id>`），root 节点树天然隔离；配合策略变量 `{username}` 实现"每用户只见自己的 home"；
- Lookup/Readdir 同时过滤 `.toolfs-trash`、`.toolfs-snapshots`。

## 8. 审计增强

```go
type AuditLogEntry struct {
    // ...现有字段不动...
    UserID    string `json:"user_id,omitempty"`
    Username  string `json:"username,omitempty"`
    Role      string `json:"role,omitempty"`
    Decision  string `json:"decision,omitempty"`  // 命中的 RBAC 规则摘要
    Protected string `json:"protected,omitempty"` // "trash:<entryID>" / "snapshot:<id>"，恢复凭据
}

func NewFileAuditLogger(dir string) (*FileAuditLogger, error)
// JSONL 追加写，内部 mutex + bufio，按日切分 audit-2026-07-23.jsonl，提供 Flush()/Close()
```

- `Session.logAudit` 自动填充身份三字段；Operation 枚举扩展 `DeleteFile`/`Rename`/`Mkdir`/`Flashback`/`RestoreTrash`；
- `AccessDenied` 判定改为哨兵错误 `errors.Is(err, ErrAccessDenied)`，替换现有脆弱的字符串 `Contains("access denied")`。

## 9. 分阶段实施计划

| 阶段 | 交付物 | 依赖 | 验收标准 |
|---|---|---|---|
| **P1 RBAC 核心**（~3d） | `rbac.go`/`rbac_config.go`；Session 扩展 + `CheckAccess`；4 个现有 WithSession 接入；sessions map 加锁 | 无 | 表驱动单测全绿；**现有 toolfs_test.go 不改一行仍全绿**（兼容性验收） |
| **P2 破坏性 API + 回收站 + 审计**（~4d） | `destructive.go`/`trash.go`/`audit_file.go`；WriteFile 版本保护 | P1 | 删除→恢复 e2e；JSONL 审计含身份字段 |
| **P3 JWT 集成**（~3d，可与 P2 并行） | `auth*.go`；`NewSessionWithToken`/`RefreshSessionToken` | P1 | httptest JWKS + 自签 ES256 全流程单测；kid 轮换重拉测试 |
| **P4 快照/闪回**（~5d） | `snapshot_*.go`；旧快照 API 委托改造；Flashback API；Prune | P2 | macOS 上 CopyBackend 全测；Linux btrfs loopback 集成测试 |
| **P5 FUSE 补全 + 命名空间**（~4d） | `fuse_adapter.go` 全量改造；`MountToolFSWithSession` 真实现 | P1/P2 | Linux CI 挂载 e2e：`rm`/`mv`/`mkdir` 走保护链路；user 视图隐藏验证 |

## 10. 关键风险与取舍

1. **FUSE 现状完全绕过 session**（已核实空壳）：P5 落地前在文档中明确"FUSE 挂载 = 无访问控制，仅限受信任场景"，这是当前最大的实际安全缺口；
2. **btrfs 整卷恢复需无打开句柄**：换名策略在有活跃 FUSE 读写时会失败/产生混乱视图，API 要求调用方先静默/销毁会话；退路是文件级逐个恢复（慢但安全）；
3. **回收站不释放空间**：rename 进回收站不减少磁盘占用，**必须默认启用 `PurgeTrash` 定时**，否则磁盘打满；
4. **`DangerousCommandFilter` 子串误杀**：现有 contains 匹配会把含 "rd"/"dd" 的正常命令误杀（如 "wordcount"），建议 P2 顺手改为精确命令名 + 参数模式匹配；
5. **依赖增加**：golang-jwt / doublestar / yaml.v3 均纯 Go、无 CGO，不破坏"轻库"形态；若坚持零依赖，ES256 验签可用 stdlib 手写 JWT 解析（+1d），glob 退化为仅前缀。

## 11. 测试策略

- **RBAC**：`rbac_test.go` 纯表驱动（role/user/path/op/期望），覆盖 deny 优先、前缀边界（`/data` vs `/database`）、glob、变量展开、admin 显式 deny、legacy AllowedPaths 回退。零外部依赖，全平台可跑。
- **JWT**：测试内 `ecdsa.GenerateKey(elliptic.P256())` 自签 token；`httptest.Server` 充当 JWKS 端点；用例覆盖：正常验签、过期、错 issuer、算法混淆攻击（alg=none/HS256）、kid miss 触发重拉、端点宕机 + 缓存公钥降级、`RefreshSessionToken`。
- **btrfs 集成测试**：双保险——`//go:build linux && btrfs_integration` build tag + 运行时探测（`btrfs` 二进制存在且环境变量 `TOOLFS_BTRFS_TEST_DIR` 指向 btrfs 挂载点，否则 `t.Skip`）；CI 专门 job：`truncate -s 1G img && mkfs.btrfs && mount -o loop`。CopyBackend 与 BtrfsBackend **共享同一套 `SnapshotBackend` 接口契约用例**。
- **FUSE**：沿用现有测试模式 + 运行时探测（`/dev/fuse` 不存在或 macFUSE 未装 → skip）；Linux CI 真实挂载 e2e，断言 `os.Remove`/`os.Rename` 触发保护链路、trash 落盘。
- **时间确定性**：回收站/闪回相关逻辑注入 `now func() time.Time`，保证 `FlashbackFile(at)` 可确定性测试。
