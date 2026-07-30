# ToolFS 技术方案：基于 RBAC 的多用户挂载文件系统

> 状态：设计稿阶段（v3，已被 v5 取代；仅供追溯，勿据此实施）
> 日期：2026-07-25
> 关联系统：[IceWhale-Files](https://github.com/IceWhaleTech/IceWhale-Files)（**权限事实来源**）、[ZimaOS-UserService](https://github.com/IceWhaleTech/ZimaOS-UserService)（JWT 签发）、ZimaOS-LocalStorage（`/DATA` 挂载）
>
> **版本演进**
> - v1：设计了一套 ToolFS 自有的 Policy 配置文件。
> - v2：实证复核发现 v1 有事实错误（误认为 `normalizeVirtualPath` 可防 `../` 逃逸）及多处危险默认值，新增 P0 路径安全阶段。
> - **v3（本版）：调查 ZimaOS 全栈后推翻 v1/v2 的核心设计**——ToolFS 不应自定义权限，而应消费 ZimaOS 既有的权限事实来源。同时依据 `workplan` 将「按用户的挂载视图」确立为核心交付物。

---

## 1. 交付目标与验收标准

来自 `workplan/summary.md`：

> ToolFS 下的基于 RBAC 挂载文件系统 —— **"测试可以这样测：用户1能看到 xxx 挂载目录，用户2能看到 xxx 挂载目录"**。本周交技术方案，**8 月第二周交付产物**。

因此本方案的**核心交付物是按用户的 FUSE 挂载视图**：不同用户挂载 ToolFS 后，各自只看到自己被授权的目录。

上层业务目的：给 AI agent 一个统一、安全的文件访问工具，**防止误删与越权**。

---

## 2. 关键前提：ZimaOS 的文件权限到底在哪里定义

这是本方案最重要的一节。调查结论：

### 2.1 唯一事实来源

```
/var/lib/casaos/files.db  →  表 shares_model_dbs
每行 = (samba_user, path, read_only, is_deleted)
语义 = "用户 X 对路径前缀 P 拥有 只读 / 读写 权限"
```

表结构见 `IceWhale-Files/repository/ddl.go:64-81`，模型见 `IceWhale-Files/model/share.go`。

### 2.2 几个反直觉但必须知道的事实

| 事实 | 依据 |
|---|---|
| **UserService 里没有权限数据** | 只有 `role` 字段（`admin`/`user`）。`GetUserPermissions`/`UpdateUserPermissions`（`ZimaOS-UserService/service/user.go:664-784`）只是转发给 IceWhale-Files 的 HTTP 代理 |
| **POSIX 文件权限完全不参与** | `smb.conf` 写死 `force user = root` + `create/directory mask = 0777` + `nt acl support = no` + `inherit permissions = no`；两个仓库均无 `chown`/`setfacl`。所有文件落盘为 `root:root 0777` |
| **`role` 的唯一作用是 admin 全局放行** | `IceWhale-Files/service/shares.go:618` `CheckPermission` 第一行 `if role=="admin" { return true }` |
| **新用户默认权限为零** | `CreateSamabaUser` 只做 `useradd` + `smbpasswd`，不写任何 share 记录 → HTTP API 全 403、SMB 看不到任何共享 |
| **不存在 per-user home 目录** | `/DATA` 下是 LocalStorage 创建的 8 个公共目录（0777），所有用户共用一棵树，隔离 100% 依赖 share 表的路径白名单 |
| **btrfs 快照是绿地** | LocalStorage 用 `mkfs.btrfs` 格式化磁盘，但 `subvolume`/`snapshot` 在两个仓库中零命中 |

### 2.3 权限的两条执行通路（同一张表）

```
                    shares_model_dbs
                     ╱            ╲
        渲染 per-user conf      加载进内存 PathMatcher
                   ╱                ╲
    /etc/samba/casa.<user>.conf    CheckPermission()
    smb.conf: include=casa.%U.conf  最长前缀匹配，未命中即拒
                   ↓                ↓
             smbd 执行           HTTP 文件 API
       （你的 conf 里没写 = 看不见）
```

### 2.4 因此 ToolFS 的定位

> **ToolFS 不定义权限，只消费权限 + 收窄。**

v1/v2 设计的「ToolFS 自有 Policy 配置文件」会造成**第二套权限真相**：管理员在 ZimaOS UI 改了共享权限 ToolFS 不知道，ToolFS 策略改了 ZimaOS UI 看不到——两边分歧即安全事故。v3 废弃该设计。

---

## 3. 已确认的决策

| 议题 | 决策 |
|---|---|
| agent 权限与用户权限的关系 | **继承用户权限 + 操作收窄**，永不放大 |
| ToolFS 的 IO 路径 | **鉴权用 IceWhale-Files 的数据，IO 直连文件系统** |
| 权限数据获取方式 | **调 IceWhale-Files HTTP API** |
| 快照 | **先做文件级版本 + 回收站；btrfs 快照延后单独立项** |
| 消费形态 | Go 库 + FUSE（每用户一个挂载点） |
| 身份传递 | JWT（UserService 签发，ES256，claims `{username, role, id}`，access token 3h） |

---

## 4. 权限模型

```
ZimaOS share 表 (samba_user, path, read_only)      ← 唯一授权来源，ToolFS 只读不写
        ↓ PermissionProvider 拉取（HTTP + TTL 缓存）
Grant{Path, ReadOnly}    →   ro: {Read, List}    rw: {Read, List, Write}
        ↓ AgentCapabilities 收窄（只减不增）
OpDelete 默认不授予 / OpExecute 默认不授予 / 全局 deny 列表
        ↓ 在【解析后的真实本地路径】上判定
allow / deny
```

### 4.1 操作粒度——ToolFS 的增量价值

ZimaOS 只有 `read_only` 一个维度，「可写」即「可删」，这正是 agent 误删风险的根源。ToolFS 将其拆开：

| ZimaOS share | ToolFS 授予 |
|---|---|
| `read_only = true` | `OpRead`, `OpList` |
| `read_only = false` | `OpRead`, `OpList`, `OpWrite` |
| —— | `OpDelete`：**默认不授予**，需 `AgentCapabilities.AllowDelete` 显式开启 |
| —— | `OpExecute`：**默认不授予**（ZimaOS 无此概念，纯 ToolFS 侧约束） |

```go
type Operation uint8
const (
    OpRead Operation = 1 << iota // 读内容 / Stat
    OpList                        // 列目录
    OpWrite                       // 创建 / 修改 / truncate
    OpDelete                      // 删除 / rename 源 / 覆盖目标
    OpExecute                     // 命令 / 代码 skill
)

type AgentCapabilities struct {
    AllowDelete  bool       // 默认 false
    AllowExecute bool       // 默认 false
    DenyPatterns []string   // 如 **/.ssh/**、**/.env、**/AppData/**
}
```

`role == "admin"`：路径维度全放行（与 IceWhale-Files 语义一致），但**操作收窄与 deny 列表对 admin 同样生效**——防误删系统目录恰恰是对 admin 才最需要。

### 4.2 核心修正：在真实本地路径上鉴权

v1/v2 在虚拟路径上判权限，这是路径穿越能绕过一切的根因。v3 改为：

```
虚拟路径
  → sanitizeVirtualPath（path.Clean，拒绝残留 ..）
  → resolvePath（filepath.Join 清理 + EvalSymlinks + containment 校验）
  → 【在真实本地路径上做权限判定】     ← share 表里存的本来就是真实路径
  → 执行（贯穿同一路径值，消除 TOCTOU）
```

这使路径穿越与软链逃逸**在结构上不可能**，而非靠打补丁：share 表的 path 就是 `/media/ZimaOS-HD/...`，判定必须发生在这一层。

### 4.3 复用 `ZimaOS-Common/file.PathMatcher`

`ZimaOS-Common/file/path_matcher.go` 是**按路径段切分的 Trie**（非字符串前缀匹配），`sync.Map` 并发安全，正是 IceWhale-Files `CheckPermission` 所使用的组件。ToolFS 复用它 → 判定语义与 ZimaOS 其余部分天然一致，不会出现「Files 允许但 ToolFS 拒绝」的分歧。

> 更正 v1/v2 的一处担心：该 Trie 按段匹配，`/DATA/Doc` **不会**误命中 `/DATA/Documents`，前缀边界问题不存在。

### 4.4 路径别名（必须处理）

share 表中 `/DATA/x` 与 `/media/ZimaOS-HD/x` **历史混存**。判定时两种写法都要尝试，参照：
- `IceWhale-Files/pkg/ifile/public_path.go:198` `rewritePublicPathAlias`（`/DATA` → `/media/ZimaOS-HD`）
- `IceWhale-Files/route/public_path.go:137` `toDataAlias`（反向）

漏掉这一步会导致老数据的授权静默失效。

---

## 5. 权限数据接入（PermissionProvider）

```go
type Grant struct {
    Path     string
    ReadOnly bool
}

type PermissionProvider interface {
    GrantsFor(username string) ([]Grant, error)
    Invalidate(username string)
}

type FilesServiceProvider struct {
    baseURL string        // 读 /run/casaos/icewhale-files.url，回落 http://127.0.0.1
    ttl     time.Duration // 默认 30s
    cache   sync.Map
}
```

### 5.1 接口选择——规避已知上游缺陷

**主用 `GET /v2_1/files/share`，而非 `GET /v2_1/files/share/permission?username=X`。**

| 端点 | 问题 |
|---|---|
| `/share/permission?username=` | handler（`IceWhale-Files/route/api_route_share.go:479`）**未过滤 `is_deleted`** → 会把已禁用的共享当作有效权限返回，**造成权限放大**（安全问题） |
| `/share` ✅ | 返回 `[]ShareListModel{Path, Status, Users[], Permission[]}`，**带 `Status` 枚举**（`enable`/`disable`/`missing`），ToolFS 过滤 `Status == enable` 即可绕开该缺陷，且一次拿到全量映射 |

**调用方式**：`Z-Skip-Auth: true` header + 源地址 127.0.0.1（IceWhale-Files 的服务间免鉴权通道，`route/routers.go:71-74`），与 UserService 的既有做法一致。

### 5.2 缓存与失效

ZimaOS 没有 share 变更事件（MessageBus 只有 `samba:create-user-end` / `samba:delete-user-end`）。策略：

- TTL 30s；
- 订阅上述两个用户事件立即失效；
- 暴露 `Invalidate(username)` 供管理接口手动触发；
- **破坏性操作（Delete/Rename）前强制做一次新鲜度检查**——不可逆操作不允许基于陈旧权限执行。

> 注：该 HTTP 端点内部走 `GetSharesList()` 直查 DB，**不经过** Files 自身那个「撤销授权后不刷新」的 `permissionCache`。因此 ToolFS 读到的是实时数据，Files 的缓存缺陷不影响本方案。

---

## 6. 按用户的挂载视图（核心交付物）

`MountToolFSWithSession`（`fuse_adapter.go:326`）当前是**空壳**——注释写明 "placeholder for future implementation"，直接转调无 session 的 `MountToolFS`，各 FUSE handler 全部调用无 session 版本，**即当前 FUSE 路径完全绕过访问控制**。

本方案将其实现为真正的 per-user 视图：

```go
type NamespaceMode int
const (
    NamespaceHide   NamespaceMode = iota // 默认：无权路径不可见
    NamespaceEACCES                      // 可见但拒绝访问（admin 调试用）
)

func MountToolFSWithSession(toolfs *ToolFS, mountPoint string, session *Session,
    mode NamespaceMode, options *fuse.MountOptions) error
```

- `ToolFSRoot` / `ToolFSDir` / `ToolFSFile` / `ToolFSFileHandle` 均携带 `session`，所有 handler 改调 `*WithSession`（session 为 nil 时行为同现状，`MountToolFS` 不受影响，保证向后兼容）；
- **根目录呈现该用户的 Grant 列表**：用户被授权 `/media/ZimaOS-HD/Documents` → 挂载点下出现 `Documents/`；未授权的目录不出现；
- `Readdir` 对每个 entry 校验 `OpList`，`NamespaceHide` 下直接剔除；`Lookup` 对无权路径返回 `ENOENT`（不泄露目录结构）；
- 每用户一个挂载点（如 `/mnt/toolfs/<username>`），root 节点树天然隔离；
- 错误映射：拒绝 → `EACCES`；只读 → `EROFS`；无权/不存在 → `ENOENT`（替换现有一律返回 `EIO` 的粗糙映射）。

**这直接对应验收标准**：用户1 的挂载点看到 A、B 目录，用户2 看到 C 目录，互不可见。

---

## 7. 防误删

### 7.1 补齐缺失的破坏性操作 API

当前**没有** Delete/Rename/Mkdir 公共 API，FUSE 层也没有 Unlink/Rename/Mkdir/Create。新增 `destructive.go`：

```go
func (fs *ToolFS) DeleteFileWithSession(path string, s *Session) error
func (fs *ToolFS) RemoveAllWithSession(path string, s *Session) error
func (fs *ToolFS) RenameWithSession(oldPath, newPath string, s *Session) error
func (fs *ToolFS) MkdirWithSession(path string, s *Session) error
```

统一流水线：

```
sanitize → resolve + containment → 权限判定（真实路径）→ 保护钩子 → 执行 → 审计
```

FUSE 侧实现 `NodeUnlinker` / `NodeRmdirer` / `NodeRenamer` / `NodeMkdirer` / `NodeCreator` / `NodeSetattrer`，全部转调上述 API —— agent 在挂载点里执行 `rm`/`mv` 同样走保护链路。

### 7.2 回收站与文件版本

```go
type ProtectionPolicy struct {
    Mode               ProtectionMode // 默认 ProtectTrash
    VersionDebounce    time.Duration  // 同一文件在此窗口内只留一个版本，默认 60s
    MaxVersionsPerFile int            // 单文件版本上限，默认 10
    MaxVersionFileSize int64          // 超过则不做版本保护，默认 64MB
    TrashRetention     RetentionPolicy
}
```

- **删除**：`os.Rename` 移入回收站（原子、O(1)、零拷贝）。**递归删目录也只 rename 目录本身**，不做全量拷贝。
- **覆盖写 / truncate**：旧内容进 `versions/`，受上述三重限制约束——否则 agent 高频小写会迅速写爆磁盘。
- **StateDir 放在 mount 外部**：`/var/lib/toolfs/<mount-id>/{trash,versions}`。
  - **必须与 mount 同一文件系统**（`os.Rename` 跨设备返回 `EXDEV`）；在 `MountLocal` 时比对 `st_dev`，不一致则**启动即报错**，而非运行时才发现；
  - 放在 mount 内部会导致：copy 型快照递归自吞（第 N 次快照包含第 N-1 次）、`snapshotDirectory` 的 `filepath.Walk`（`toolfs.go:1475`）把整个回收站读进内存。
- 定时 `PurgeTrash`：rename 进回收站不释放空间，不清理会打满磁盘。

```go
func (fs *ToolFS) ListTrash(session *Session) ([]TrashEntry, error)
func (fs *ToolFS) RestoreFromTrash(entryID string, session *Session) error // 需目标路径 OpWrite
func (fs *ToolFS) PurgeTrash(olderThan time.Duration) error
```

> ZimaOS 已有一套完整回收站（`IceWhale-Files/service/trash.go`，~1470 行，带 `user_id` 归属、fsnotify 与 SMB recycle 联动）。ToolFS 直连文件系统不会经过它，故需自建；**目录布局与元数据字段对齐 Files 的 trash 模型**，为将来合并留出空间。

---

## 8. JWT 集成

```go
type Identity struct {
    UserID, Username, Role string
    ExpiresAt              time.Time
}

type TokenVerifier interface {
    Verify(tokenString string) (*Identity, error)
}

func (fs *ToolFS) NewSessionWithToken(sessionID, jwtToken string) (*Session, error)
func (fs *ToolFS) RefreshSessionToken(sessionID, newJWT string) error // 续期不丢 session 状态
```

- **JWKSVerifier**：限定 `ES256` + issuer `casaos` + exp 校验（防 `alg=none`/HS256 混淆攻击）；`kid` 未命中或验签失败时，若距上次刷新超过 `RefreshMinGap`（默认 10s）则强制重拉 JWKS 重试一次——对应 UserService **进程重启即轮换密钥**的特性。
- **过期处理**：access token 3h。默认 Strict 模式，过期后返回哨兵错误 `ErrTokenExpired`，调用方用新 token `RefreshSessionToken` 续期，session 不销毁。
- **降级**：UserService 不可用时用缓存公钥验签（`AllowStaleKeys`，默认 true）；从未成功拉取过公钥则 **fail closed**。
- **开发环境**：`StaticKeyVerifier`（注入测试公钥）与 `NewInsecureMockVerifier`（跳过验签，命名明示不安全），**仅显式注入生效，绝不作为自动 fallback**。

---

## 9. 审计

```go
type AuditLogEntry struct {
    // ...现有字段...
    UserID    string `json:"user_id,omitempty"`
    Username  string `json:"username,omitempty"`
    Role      string `json:"role,omitempty"`
    Decision  string `json:"decision,omitempty"`  // 命中的授权规则摘要
    Protected string `json:"protected,omitempty"` // "trash:<entryID>"，恢复凭据
}

func NewFileAuditLogger(dir string) (*FileAuditLogger, error) // JSONL，按日切分
```

`AccessDenied` 判定改为哨兵错误 `errors.Is(err, ErrAccessDenied)`，替换现有脆弱的字符串 `Contains("access denied")`（`toolfs.go:163`）。

---

## 10. 前置条件

| # | 前置条件 | 状态 |
|---|---|---|
| 1 | **P0 路径安全必须先做**（见 §11.0）。路径穿越/软链逃逸/只读绕过均已实测复现；不修则 RBAC 可被 `../` 直接穿透 | ⚠️ **阻塞项** |
| 2 | IceWhale-Files 服务运行，`/run/casaos/icewhale-files.url` 可读 | 依赖 |
| 3 | ToolFS 进程以 **root** 运行（文件均 `root:root 0777`，需读 runtime 文件） | 依赖 |
| 4 | **agent 宿主如何取得用户 JWT**——参照 `zimactl` 的 `--token` / `ZIMACTL_TOKEN` / profile 模型。需产品侧明确 agent 部署形态与凭据下发方式 | ❓ 待确认 |
| 5 | FUSE 环境：Linux 需 `/dev/fuse`；macOS 开发需 macFUSE | 依赖 |
| 6 | 多用户挂载点的生命周期归属（登录时挂载、登出卸载？常驻？） | ❓ 待确认 |

> 第 4、6 项不阻塞编码（可先用注入式 token 与手动挂载验证），但影响最终集成形态。

---

## 11. 实施计划

### 11.0 P0 路径安全与并发正确性（~2d，最高优先级）

以下四项**均已在真实代码上写测试复现**（临时测试文件已删除，仓库未改动）：

| 编号 | 问题 | 实测结果 |
|---|---|---|
| **S1** | 路径穿越绕过访问控制 | `IsPathAllowed("/toolfs/data/../secret.txt") = true`，读到挂载外文件，审计记 `success:true` |
| **S2** | 符号链接逃逸 | mount 内软链指向挂载外文件，可直接读出 |
| **S3** | 只读挂载被绕过 | 从可写挂载 `/toolfs/rw/../ro/x` 成功写入 `ReadOnly=true` 挂载的本地目录 |
| **S4** | 并发数据竞态 | `go test -race` 三处 DATA RACE：`fs.mounts` 写（`toolfs.go:430`）vs 读（`toolfs.go:705`） |

**根因**：`normalizeVirtualPath`（`toolfs.go:346`）只做反斜杠转换、去重复斜杠、去 `./` 前缀、去尾斜杠，**不清理 `..`**；而 `resolvePath` 用 `filepath.Join`（会 clean）。于是**判定用未清理的路径、执行用清理后的路径**——经典的检查/使用不一致。

> v1 方案曾写「复用 `normalizeVirtualPath` 防 `../` 逃逸」，该表述是错误的。

**交付内容**：

1. 新增 `sanitizeVirtualPath(path) (string, error)`：在 `normalizeVirtualPath` 之上加语义校验（`path.Clean` 后拒绝仍含 `..` 或逃出 `rootPath`）。**不修改 `normalizeVirtualPath` 本身**——它被多处依赖，只负责字符规范化。
2. `resolvePath` 增加 containment 校验：`filepath.EvalSymlinks` 求真实路径，确认仍在 `mount.LocalPath` 内；写入用 `O_NOFOLLOW`（Linux 上可进一步用 `openat2` + `RESOLVE_BENEATH`）。
3. 判定与执行同源：同一个 sanitize 后的路径值贯穿全流程。
4. 并发：`mounts` / `sessions` / `snapshots` 加 `sync.RWMutex`；`pathResolveCache` 缓存值改为不可变副本。
5. 审计正确性：拒绝路径上记 `success:false`。

**验收**：S1–S4 回归测试**先红后绿**；`go test -race ./...` 无 DATA RACE；现有测试全绿（已确认基线全绿且不依赖穿越行为）。

### 11.1 阶段表

| 阶段 | 内容 | 关键文件 | 验收 |
|---|---|---|---|
| **P0**（~2d） | 路径安全 + 并发正确性 | `toolfs.go` | 见 §11.0 |
| **P1**（~3d） | 权限接入：`PermissionProvider`、Grant→Ops 映射、`AgentCapabilities` 收窄、复用 `PathMatcher`、路径别名、`Session.CheckAccess` | 新增 `rbac.go`、`permission_provider.go`；改 `toolfs.go` | 表驱动单测；mock Files 服务；`Status != enable` 被正确过滤 |
| **P2**（~3d） | JWT 集成：JWKS 验签、Session 身份、过期与续期、开发用 mock verifier | 新增 `auth.go`、`auth_jwks.go`、`auth_mock.go` | httptest JWKS + 自签 ES256；alg 混淆攻击被拒 |
| **P3**（~4d） | 破坏性 API + 回收站 + 审计落盘 | 新增 `destructive.go`、`trash.go`、`audit_file.go` | 删除→恢复 e2e；跨设备检测；审计含身份字段 |
| **P4**（~4d） | **FUSE per-user 挂载视图**（核心验收） | `fuse_adapter.go` 全量改造 | **用户1/用户2 挂载点分别只见各自授权目录**；`rm`/`mv` 走保护链路 |
| 延后 | btrfs 快照 / 闪回 | —— | 单独立项：需把目录转为 subvolume，属侵入式存储改造 |

合计约 **16 人天**，留出联调与缓冲，可在 8 月第二周前交付。

---

## 12. 风险与取舍

1. **FUSE 现状完全绕过 session**（已核实为空壳）：P4 落地前需在 README 明确「FUSE 挂载 = 无访问控制，仅限受信任场景」。这是当前最大的实际安全缺口。
2. **权限缓存与撤权时延**：TTL 30s 意味着撤销授权后最长 30s 内仍可能放行。破坏性操作前强制刷新可把不可逆风险降到最低；如需更强实时性，需推动 IceWhale-Files 增加 share 变更事件。
3. **上游 API 缺陷**：`/share/permission` 不过滤 `is_deleted`（权限放大）。本方案改用 `/share` + `Status` 过滤规避，但建议同步向 IceWhale-Files 提修复。
4. **与 IceWhale-Files 判定一致性**：两者共用 `PathMatcher` 可保证语义一致，但 Files 侧有「撤权后 `permissionCache` 不刷新」的缺陷，短期内可能出现 Files 放行而 ToolFS 拒绝的情况（ToolFS 更严格，方向安全）。
5. **回收站空间**：rename 不释放空间，必须默认启用 `PurgeTrash` 定时。
6. **`DangerousCommandFilter` 子串误杀**：现有 contains 匹配会误杀含 "rd"/"dd" 的正常命令（如 "wordcount"），建议 P3 顺手改为精确命令名匹配。
7. **依赖增加**：`golang-jwt/jwt/v5` 等均为纯 Go、无 CGO，不破坏轻库形态。`ZimaOS-Common` 的引入会让 ToolFS 与 ZimaOS 耦合——这是换取权限语义一致性的自觉取舍；若需保持 ToolFS 通用性，可将 `PathMatcher` 抽为可替换接口。

---

## 13. 验证方式

1. **P0 回归**：`go test -race ./...`；四个逃逸用例（穿越读 / 软链读 / 只读挂载穿越写 / 并发挂载）全部被拒且审计记 `success:false`。
2. **权限接入**：起 mock IceWhale-Files 返回构造的 share 列表，断言——`Status=disable` 的共享不产生权限；`/DATA` 与 `/media/ZimaOS-HD` 两种写法都能命中；`read_only=true` 时 `OpWrite` 被拒；`OpDelete` 默认被拒。
3. **端到端（对齐验收标准）**：ZimaOS 上建两个子用户，分别授权不同目录 → 各自挂载 `/mnt/toolfs/<username>` → `ls` 只看到自己被授权的目录 → 交叉访问对方目录返回 `ENOENT`/`EACCES`。
4. **防误删**：挂载点内 `rm somefile` → 文件进 StateDir 回收站 → `RestoreFromTrash` 可恢复；`echo x > existing` → 旧版本进 `versions/`。
5. **一致性交叉验证**：同一 (用户, 路径) 分别问 ToolFS 与 IceWhale-Files 的 `CheckPermission`，结论必须一致。
6. **JWT**：`ecdsa.GenerateKey(elliptic.P256())` 自签 + `httptest.Server` 充当 JWKS；覆盖正常验签、过期、错 issuer、alg 混淆、kid miss 重拉、端点宕机降级。
