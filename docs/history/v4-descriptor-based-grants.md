# ToolFS 技术方案：基于 ZimaOS 用户授权的多会话挂载文件系统（RBAC/ACL）

> 状态：设计稿阶段（v4，已被 v5 取代；仅供追溯，勿据此实施）
> 日期：2026-07-26
> 关联系统：[IceWhale-Files](https://github.com/IceWhaleTech/IceWhale-Files)（**权限事实来源**）、[ZimaOS-UserService](https://github.com/IceWhaleTech/ZimaOS-UserService)（JWT 签发）、ZimaOS-LocalStorage（`/DATA` 挂载）
>
> **版本演进**
> - v1：设计了一套 ToolFS 自有的 Policy 配置文件。
> - v2：实证复核发现 v1 有事实错误（误认为 `normalizeVirtualPath` 可防 `../` 逃逸）及多处危险默认值，新增 P0 路径安全阶段。
> - v3：调查 ZimaOS 全栈后推翻 v1/v2 的核心设计——ToolFS 不再自定义授权，而是消费 ZimaOS 既有的权限事实来源；将「按用户的挂载视图」确立为核心交付物。
> - **v4（本版）**：对 v3 做安全与可实施性分析，修正 `/share` 聚合接口不适合作授权来源、`EvalSymlinks` 无法消除 TOCTOU、FUSE 本机调用者与内核缓存绕过、固定 `/var/lib/toolfs` 跨设备失败、现有 JWKS 无 `kid` 等问题；引入逐行 Grant 接口、descriptor-based IO、`SessionView`、`MountHandle` 和 per-device StateRoot，并把核心 FUSE 验收前移。

---

## 1. 交付目标与验收标准

来自 `workplan/summary.md`：

> ToolFS 下的基于 RBAC 挂载文件系统 —— **"测试可以这样测：用户1能看到 xxx 挂载目录，用户2能看到 xxx 挂载目录"**。本周交技术方案，**8 月第二周交付产物**。

因此本方案的**核心交付物是绑定用户身份与 agent 能力的 FUSE 挂载视图**：不同用户或不同 agent session 挂载 ToolFS 后，各自只看到自己被授权且未被 ToolFS 收窄掉的目录。

上层业务目的：给 AI agent 一个统一、安全的文件访问工具，**防止误删与越权**。

### 1.1 交付边界

本方案分为两个可独立验收的交付物：

1. **核心挂载 MVP**：安全路径解析、逐用户 Grant、per-session 只读视图、本机 UID 隔离、动态撤权和完整审计。它直接满足「用户1/用户2看到不同目录」的验收标准。
2. **受保护写入**：创建、修改、删除、重命名、回收站和文件版本。所有可能覆盖或丢失数据的操作在保护链未完成前保持禁用。

这样可以先纵向跑通最关键的安全闭环，而不是等所有写入与恢复能力完成后才首次验证 FUSE。

### 1.2 术语说明

ZimaOS 当前模型实质上是：

- `role=admin/user` 的粗粒度角色；
- `(username, path, read_only)` 的逐用户路径 ACL；
- ToolFS 在其上增加 per-agent capability 收窄。

因此本文沿用项目中的 “RBAC” 名称，但实现上不是纯角色继承模型，而是 **RBAC + 用户路径 ACL + session capability**。

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
| **`role` 在授权层的唯一作用是 admin 放行** | `IceWhale-Files/service/shares.go:618` `CheckPermission` 第一行 `if role=="admin" { return true }`；但 Files handler 在此之前仍会执行 public-path 规范化和根目录白名单，不能解释为宿主机 `/` 全放行 |
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

v1/v2 设计的「ToolFS 自有授权 Policy 配置文件」会造成**第二套权限真相**：管理员在 ZimaOS UI 改了共享权限 ToolFS 不知道，ToolFS 授权改了 ZimaOS UI 看不到——两边分歧即安全事故。v3 起废弃该设计。

但“唯一授权来源”不等于只复用 share 表。IceWhale-Files 的真实安全边界由两部分共同组成：

1. **授权事实**：share 表与 admin role；
2. **宿主路径范围**：`NormalizePublicPath` / `ValidatePublicPath` 对 `/media`、`/DATA` 等受管根目录的约束。

ToolFS 必须同时复用或等价实现这两个约束。尤其 admin 只能在配置的 `PublicRoots` 内跳过 Grant 匹配，绝不能获得宿主机根目录访问权。

---

## 3. 已确认的决策

| 议题 | 决策 |
|---|---|
| agent 权限与用户权限的关系 | **继承用户权限 + 操作收窄**，永不放大 |
| ToolFS 的 IO 路径 | **鉴权用 IceWhale-Files 的逐行 Grant，IO 通过受 Grant 根 dirfd 约束的 descriptor-based backend 直连文件系统** |
| 权限数据获取方式 | **新增/修复 IceWhale-Files 内部逐用户 Grant HTTP API；不使用 `/share` UI 聚合模型做授权** |
| 快照 | **先做文件级版本 + 回收站；btrfs 快照延后单独立项** |
| 消费形态 | Go 库 + FUSE（每 session 一个挂载实例；同一用户的不同 agent capability 不共用挂载） |
| 身份传递 | JWT（UserService 签发，ES256，claims `{username, role, id}`，access token 3h） |
| FUSE 缓存 | 授权敏感挂载默认禁用内容缓存；Grant 变更主动失效 inode/dentry |
| 生产平台 | Linux 为安全基线；macOS 仅开发验证，使用逐段 `openat` fallback |

---

## 4. 权限模型

```
ZimaOS share 表 (samba_user, path, read_only, is_deleted)  ← 授权事实，ToolFS 只读不写
        + IceWhale-Files PublicRoots / public-path 规则    ← 宿主路径上界
        ↓ Internal Grant API
不可变 AccessSnapshot{Username, Grants, Generation}
        ↓ AgentCapabilities 收窄（只减不增）
不可变 SessionView{NamespaceEntries, AccessMatcher}
        ↓ secure path backend（dirfd + openat2/*at）
allow / deny + 实际 IO
```

### 4.1 操作粒度——ToolFS 的增量价值

ZimaOS 只有 `read_only` 一个维度，「可写」即「可删」，这正是 agent 误删风险的根源。ToolFS 将其拆开：

| ZimaOS share | ToolFS 授予 |
|---|---|
| `read_only = true` | `OpRead`, `OpList` |
| `read_only = false` | `OpRead`, `OpList`, `OpCreate`, `OpModify` |
| `read_only = false` 且 session 显式开启删除 | `OpDelete`；只读 Grant 永远不能被 capability 提升为可删 |

```go
type Operation uint16
const (
    OpRead Operation = 1 << iota // 读内容 / Stat
    OpList                        // 列目录
    OpCreate                      // 创建文件 / 目录
    OpModify                      // 修改 / append / truncate / setattr
    OpDelete                      // 删除 / rename 源 / 覆盖目标
)

type AgentCapabilities struct {
    AllowDelete           bool     // 默认 false；只释放 RW Grant 的删除资格
    AllowCommandExecution bool     // 默认 false；仅约束 ToolFS command/skill API
    DenyPatterns          []string // 如 **/.ssh/**、**/.env、**/AppData/**
}
```

最终权限始终是「ZimaOS 允许的最大集合」与 session capability 的交集。`AllowDelete` 只能恢复 ToolFS 默认收窄掉的 RW 删除能力，不能提升只读 Grant。`DenyPatterns` 在 session 创建时针对 canonical 相对路径编译；非法 pattern 直接拒绝创建 session，匹配失败不能按 allow 处理。

`OpExecute` 不属于文件系统权限：FUSE 无法阻止进程先读取脚本、再执行 `bash script.sh`。命令/代码执行能力只在 ToolFS 自己的 command/skill API 中通过 `AllowCommandExecution` 控制，不能宣称由挂载文件系统强制执行。

`role == "admin"`：跳过逐用户 Grant 匹配，但仍受以下约束：

- 只能访问 `PublicRootProvider` 返回的受管根目录；
- `AgentCapabilities` 与 deny patterns 照常生效；
- `OpDelete` 仍默认关闭；
- storage root 本身不可删除、覆盖或重命名，只能作为 `WriteInto` 容器。

### 4.2 核心修正：descriptor-based 安全 IO

仅执行 `filepath.EvalSymlinks` 后再调用 `os.Open`/`os.Rename` **不能消除 TOCTOU**：检查和使用之间，父目录或软链仍可能被 SMB、其他本机进程或并发 agent 替换；`O_NOFOLLOW` 也只保护最终分量。v4 不再把“解析后的路径字符串”当作安全句柄。

统一流水线改为：

```
虚拟路径
  → sanitizeVirtualPath（拒绝 NUL、绝对重置、任何 .. path segment）
  → SessionView.Resolve（得到 NamespaceEntry + 相对路径）
  → AccessMatcher.Check（规范化 public path，最长 Grant 命中 + capability 收窄）
  → SecurePathBackend.Open/Unlink/Rename（以 NamespaceEntry 的预打开 root dirfd 为锚）
  → 在返回的 fd 或 parent-fd + basename 上执行 IO
  → 审计
```

Linux 生产实现：

- 每个 `NamespaceEntry` 创建时打开并持有 canonical Grant root dirfd；
- 读、写、创建使用 `openat2(RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS)`；
- 删除使用 `unlinkat`，创建目录使用 `mkdirat`；
- rename 同时持有源/目标 parent dirfd，使用 `renameat2`；
- 全程不重新从未经约束的绝对路径打开目标；
- 对不存在的新文件解析到安全 parent dirfd，再操作最终 basename。

macOS 没有 `openat2`，开发 fallback 采用逐段 `openat(O_NOFOLLOW|O_DIRECTORY)`。Linux 是生产安全基线；macOS 用于功能开发和单测，不把较弱 fallback 宣称为同级生产保证。

当前 `resolvePath`、`isSkillMount`、`isVirtualPath` 等处的 `strings.HasPrefix` 同时改为路径段匹配：

```go
path == prefix || strings.HasPrefix(path, prefix+"/")
```

避免 `/toolfs/data` 错误命中 `/toolfs/database`。

### 4.3 复用 `ZimaOS-Common/file.PathMatcher`

`ZimaOS-Common/file/path_matcher.go` 是**按路径段切分的 Trie**（非字符串前缀匹配），正是 IceWhale-Files `CheckPermission` 所使用的组件。ToolFS 复用其最长前缀语义，但不在请求路径上并发修改同一个 matcher：

- `PathMatcher.node.value` 本身没有锁，不能因内部使用 `sync.Map` 就认定整个类型可并发读写；
- 每次刷新先构建全新的 matcher 和 `AccessSnapshot`；
- 构建完成后用 `atomic.Pointer` 一次发布；
- 请求只读当前 snapshot；旧请求自然持有旧的不可变快照。

> 更正 v1/v2 的一处担心：该 Trie 按段匹配，`/DATA/Doc` **不会**误命中 `/DATA/Documents`，前缀边界问题不存在。

### 4.4 路径别名（必须处理）

share 表中 `/DATA/x` 与 `/media/ZimaOS-HD/x` **历史混存**。参照：

- `IceWhale-Files/pkg/ifile/public_path.go:198` `rewritePublicPathAlias`（`/DATA` → `/media/ZimaOS-HD`）
- `IceWhale-Files/route/public_path.go:137` `toDataAlias`（反向）

v4 的具体规则是：

1. Grant 进入 `AccessSnapshot` 前统一规范化为 `/media/ZimaOS-HD/...`；
2. 请求路径也只规范化一次，不以“任一别名命中即允许”的方式放大权限；
3. 同一用户、同一 canonical path 出现重复记录时，**只读优先**（deny-dominant）；
4. 不同深度 Grant 仍按最长路径命中，例如父目录 RW、子目录 RO 时，子目录保持 RO；
5. 无法规范化或逃出 `PublicRoots` 的 Grant 丢弃并记录安全告警。

漏掉别名会让老数据授权静默失效；盲目“两个路径都尝试，任一允许即放行”则可能把冲突数据合并成更高权限。

### 4.5 PublicRoots 是硬上界

ToolFS 新增可替换接口：

```go
type PublicRoot struct {
    Name      string
    LocalPath string
}

type PublicRootProvider interface {
    Roots(ctx context.Context) ([]PublicRoot, error)
    Normalize(path string, op Operation) (string, error)
}
```

ZimaOS 实现与 IceWhale-Files 的 `NormalizePublicPath` / `ValidatePublicPath` 对齐。通用 ToolFS 使用显式配置的 roots。任何 Grant、admin 请求和 StateRoot 都不能突破该硬上界。

---

## 5. 权限数据接入（PermissionProvider）

```go
type Grant struct {
    ID       int
    Name     string
    Path     string
    ReadOnly bool
}

type Freshness uint8
const (
    CachedOK Freshness = iota
    RequireFresh
)

type AccessSnapshot struct {
    Username   string
    Grants     []Grant
    Generation uint64
    FetchedAt  time.Time
}

type PermissionProvider interface {
    SnapshotFor(ctx context.Context, username string, freshness Freshness) (*AccessSnapshot, error)
    Invalidate(username string)
}

type FilesServiceProvider struct {
    baseURL string        // 读 /run/casaos/icewhale-files.url
    ttl     time.Duration // 默认 30s
    cache   sync.Map      // username -> immutable *AccessSnapshot
}
```

代码块为契约示意。实际实现必须对 `Grants`、`Entries` 和 `DenyPatterns` 做深拷贝并隐藏可变 map/slice，只通过只读方法访问；“不可变 snapshot”不能只靠注释约定。

### 5.1 接口选择——必须使用逐行授权模型

v4 不使用现有两个接口直接作为授权输入：

| 端点 | 问题 |
|---|---|
| `/share/permission?username=` | handler（`IceWhale-Files/route/api_route_share.go:479`）**未过滤 `is_deleted`** → 会把已禁用的共享当作有效权限返回，**造成权限放大**（安全问题） |
| `/share` | 面向 UI 的路径聚合模型：先把所有 DB 行加入 `Permission[]`，再以最新一行计算整个路径的 `Status`；它无法表达“同一路径下某个用户记录已删除、其他用户仍有效”，也不适合作为安全契约 |

实施前在 IceWhale-Files 增加内部接口，建议：

```http
GET /v2_1/files/internal/grants?username=<url-escaped>
Z-Skip-Auth: true

{
  "generation": 42,
  "grants": [
    {"id": 1, "name": "Documents", "path": "/media/ZimaOS-HD/Documents", "read_only": false}
  ]
}
```

服务端契约：

- endpoint 只能注册在 loopback/Unix socket 的内部 listener，或在 handler 侧独立校验请求来源；`Z-Skip-Auth: true` 本身不是鉴权机制，不能让公网/局域网请求仅凭该 Header 读取授权；
- 查询条件为 `samba_user = ? AND is_deleted = 0`，不做跨用户聚合；
- 每行执行 public-path 规范化；
- `missing` 或 storage 未挂载的路径不返回；
- 返回稳定 ID、展示名、canonical path 和 `read_only`；
- 响应 generation/ETag，供 ToolFS 判断是否需要重建 SessionView；
- 未知用户返回空列表，**空列表语义是 deny all**。

若不新增接口，则必须先修复 `/share/permission` 的 `is_deleted`、missing 与规范化问题，并把修复后的逐用户响应作为版本化契约；不能在 ToolFS 侧解析 `/share` 聚合结果猜测授权。

调用仍使用 `Z-Skip-Auth: true` + loopback，但客户端必须：

- 校验 runtime 文件解析出的 host 只能是 `127.0.0.1` 或 `::1`；
- 禁止 HTTP redirect，避免把内部 header 或授权数据带到其他地址；
- 设置连接、响应头和总请求超时；
- 限制响应体大小和 Grant 数量；
- runtime 文件不存在时默认 fail closed；开发 fallback 必须显式配置，不能静默请求未知的本机 80 端口。

### 5.2 缓存与失效

ZimaOS 没有 share 变更事件（MessageBus 只有 `samba:create-user-end` / `samba:delete-user-end`）。策略：

- 不可变 snapshot TTL 默认 30s；
- 订阅上述两个用户事件立即失效；
- 暴露 `Invalidate(username)` 供管理接口手动触发；
- 推动 IceWhale-Files 在 share create/update/delete/status 变化时发布 generation 事件；落地前由轮询兜底；
- snapshot 更新后通知对应 `MountHandle` 重建 SessionView，并失效 FUSE inode/dentry/content cache。

失败与新鲜度规则：

| 操作 | 缓存策略 |
|---|---|
| 已开始的只读请求 | 允许使用请求开始时持有的 TTL 内 snapshot |
| 新 open / list / stat | snapshot 过期且刷新失败时 fail closed |
| create / modify / truncate / setattr shrink | `RequireFresh`；刷新失败即拒绝 |
| delete / rename / 覆盖已有目标 / restore | `RequireFresh`；刷新失败即拒绝 |

`Invalidate` 与强制刷新必须通过 singleflight + generation compare 原子完成，不能采用“先删 cache、再普通读取”的竞态流程。任何上游错误、解析错误或空响应都不能自动回退到“无限期使用旧授权”。

---

## 6. per-session 挂载视图（核心交付物）

`MountToolFSWithSession`（`fuse_adapter.go:326`）当前是**空壳**——注释写明 "placeholder for future implementation"，直接转调无 session 的 `MountToolFS`，各 FUSE handler 全部调用无 session 版本，即当前 FUSE 路径完全绕过访问控制。

v4 不把授权规则直接塞进现有全局 `fs.mounts`，而是为每个 session 构建独立、不可变的命名空间：

```go
type NamespaceEntry struct {
    GrantID     int
    VirtualName string
    PublicPath  string
    LocalRoot   string
    ReadOnly    bool
    RootFD      int // Linux；由 SecurePathBackend 管理
}

type SessionView struct {
    Generation   uint64
    Entries      map[string]*NamespaceEntry
    Access       *AccessSnapshot
    Capabilities AgentCapabilities
}
```

### 6.1 命名空间规则

- 普通用户只从有效 Grant 构建 entry；
- 优先使用 IceWhale-Files 的 share `Name`，名称非法或为空时使用 storage name + basename；
- 虚拟名冲突时追加稳定 Grant ID，例如 `Documents~42`，禁止静默覆盖；
- exact canonical path 重复时按 §4.4 合并；父子 Grant 保留最长前缀权限语义；
- admin 从 `PublicRootProvider` 构建受管 storage entries，而不是展示宿主机 `/`；
- 同一用户的不同 agent 可能有不同 capabilities，因此挂载与 `SessionView` 按 session 隔离，建议路径 `/mnt/toolfs/<user-id>/<session-id>`；
- mount path 不直接使用未经校验的 username；目录名使用数字 user ID 和不可预测 session ID。

当前根节点固定暴露 `memory`、`rag` 和 skill mounts，它们没有 ZimaOS share 授权来源。核心 RBAC FUSE MVP **不暴露这些虚拟后端**；后续如需合入，统一放到 `/tools` 并使用独立 `ToolCapabilities`，不能因用户拥有某个文件 Grant 就自动开放。

### 6.2 MountHandle 与生命周期

```go
type NamespaceMode uint8
const (
    NamespaceHide NamespaceMode = iota // 默认：无权路径不可见
    NamespaceEACCES                    // 仅诊断场景
)

type MountConfig struct {
    Mode            NamespaceMode
    ExpectedUID     uint32
    ExpectedGID     uint32
    AllowRootCaller bool // 默认 false
    Options         *fuse.MountOptions
}

type MountHandle interface {
    MountPoint() string
    Refresh(ctx context.Context) error
    Close(ctx context.Context) error
}

func MountToolFSWithSession(
    ctx context.Context,
    toolfs *ToolFS,
    mountPoint string,
    session *Session,
    config MountConfig,
) (MountHandle, error)
```

`Options` 只接受性能类安全白名单；UID 校验、cache TTL、direct IO、`AllowOther` 等安全相关选项由挂载实现统一生成，调用方传入冲突配置时直接报错。

不能继续丢弃 `*fuse.Server`：`MountHandle` 持有 server、当前 `SessionView`、inode 索引和取消函数，用于主动撤权、cache invalidation、卸载和健康检查。

原 `MountToolFS` 无 session 即完全放行。为避免安全误用：

- 重命名为 `MountToolFSUnsafe` 并标记 deprecated；
- README 明示仅允许受信任单用户开发环境；
- 新 CLI 默认只暴露 authenticated/session mount；
- 不允许 authenticated mount 在 permission provider 失败时 fallback 到 unsafe mount。

### 6.3 本机调用者隔离

session 绑定 JWT 身份，FUSE request 还必须绑定本机调用者：

- mountpoint 由 root 创建为 `0700`，owner 设置为 `ExpectedUID:ExpectedGID`；
- 若 daemon 以 root 挂载并启用 `AllowOther`，每个 handler 用 `fuse.FromContext(ctx)` 校验 UID；
- UID 不匹配返回 `EACCES`，并记录 caller UID/PID；
- root 代操作是否允许采用显式 `AllowRootCaller` 配置，默认关闭；
- `default_permissions` 只作第二层防护，不能替代 handler 内身份检查。

否则 `AllowOther=false` 会导致目标普通用户无法使用；`AllowOther=true` 又会让其他本机用户有机会跨挂载访问。

### 6.4 动态可见性与 FUSE 缓存

- `Readdir`、`Lookup`、`Open`、`Read`、`Write`、`Setattr` 和所有 mutation handler 都校验同一 session；
- 根目录动态基于当前 `SessionView` 响应，不在 `OnAdd` 中永久固化 Grant 列表；
- `NamespaceHide` 下，无权路径 `Lookup` 返回 `ENOENT`；已知只读路径的 mutation 返回 `EROFS`；身份或 capability 拒绝返回 `EACCES`；
- 授权敏感文件不返回 `FOPEN_KEEP_CACHE`，默认使用 direct IO/禁用内容缓存；
- entry、attr、negative TTL 在 MVP 设为 0；后续如提升性能，必须在 generation 变化时调用 `NotifyEntry`、`NotifyContent`/`InodeNotify`；
- Grant 撤销、JWT 过期或 session 关闭后，即使 inode/handle 已存在，后续操作也必须重新拒绝；
- open handle 不能固定保存“已授权”布尔值，只保存 session、entry 和受约束 fd。

**核心验收**：用户1 的 session mount 只见 A、B，用户2 只见 C；用户2 即使直接访问用户1 mountpoint，也因 UID/session 不匹配被拒；撤销 Grant 后，已有路径和已打开文件在规定 TTL 后均不可继续读取。

---

## 7. 防误删

### 7.1 操作授权矩阵

当前**没有** Delete/Rename/Mkdir 公共 API，FUSE 层也没有 Unlink/Rename/Mkdir/Create。新增 `destructive.go`：

```go
func (fs *ToolFS) DeleteFileWithSession(ctx context.Context, path string, s *Session) error
func (fs *ToolFS) RemoveAllWithSession(ctx context.Context, path string, s *Session) error
func (fs *ToolFS) RenameWithSession(ctx context.Context, oldPath, newPath string, s *Session) error
func (fs *ToolFS) MkdirWithSession(ctx context.Context, path string, s *Session) error
```

所有入口使用同一矩阵，不能由 FUSE 自己拼权限：

| 文件系统行为 | 必须满足 |
|---|---|
| read / stat | 目标 `OpRead` |
| readdir | 目录 `OpList` |
| create / mkdir | 目标 parent `OpCreate`，且最终 basename 不存在 |
| append / overwrite | 目标 `OpModify` + `RequireFresh` + 版本保护 |
| truncate / setattr shrink | 目标 `OpModify` + `RequireFresh` + 版本保护 |
| unlink / rmdir | 源 `OpDelete` + `AllowDelete` + `RequireFresh` + 回收站 |
| rename | 源 `OpDelete`；目标 parent `OpCreate`；若覆盖目标，目标还需 `OpDelete`；全部 `RequireFresh` |
| restore | trash entry 属于当前 UserID；目标 parent `OpCreate`；若覆盖目标，目标需 `OpDelete` 并先保护 |

storage root、Grant root 和 StateRoot 本身永远不可删除或重命名。跨设备 rename 在 MVP 返回 `EXDEV`，不静默降级成未保护的 copy+delete。

mutation 按稳定顺序获取 source/target path 与 inode 锁，授权复核、保护和实际修改处于同一临界区。unlink、覆盖和 restore 前必须撤销或冻结 ToolFS 内仍可写的相关 handle，避免已移入保护区的 inode 被旧 handle 继续修改。

统一流水线：

```
sanitize
  → 取得 RequireFresh AccessSnapshot
  → SessionView + SecurePathBackend 打开受约束 parent/target fd
  → 权限矩阵
  → 持久化保护 journal
  → renameat2/unlinkat/write
  → 提交 journal
  → 审计
```

FUSE 侧实现 `NodeUnlinker` / `NodeRmdirer` / `NodeRenamer` / `NodeMkdirer` / `NodeCreator` / `NodeSetattrer`，全部转调上述 API —— agent 在挂载点里执行 `rm`/`mv` 同样走保护链路。

### 7.2 per-device 回收站与文件版本

```go
type ProtectionPolicy struct {
    Mode                       ProtectionMode // 默认 ProtectRequired
    VersionDebounce            time.Duration  // 默认 60s
    MaxVersionsPerFile         int            // 默认 10
    MaxVersionFileSize         int64          // 默认 64MB
    AllowUnprotectedLargeWrite bool           // 默认 false
    TrashRetention             RetentionPolicy
}
```

固定 `/var/lib/toolfs` 通常与 `/DATA`、外接盘不在同一文件系统，无法承载原子 rename。v4 引入：

```go
type StateRoot struct {
    RuntimeDev   uint64 // st_dev，仅用于本次启动内路由和同设备判断
    FilesystemID string // FS UUID 或持久化随机 marker，跨重启稳定
    InstanceID   string // 持久化的 ToolFS state-root ID
    Path         string
    RootFD       int
}

type StateRootResolver interface {
    ForTarget(ctx context.Context, targetFD int) (*StateRoot, error)
}
```

- 按实际目标 inode 的 `st_dev` 选择 state root，而不是只比较 `MountLocal.LocalPath`；一个 Grant 下可能存在多个子挂载；
- `st_dev` 不能作为持久化身份：journal/metadata 使用 filesystem UUID；无可靠 UUID 时在 state root 内创建并持久化随机 marker，启动时扫描和校验，绝不从当前设备号猜旧数据；
- 每个 backing filesystem 的根目录下建立 `.toolfs-state/<mount-id>/{trash,versions,journal}`（例如 `/media/<storage>/.toolfs-state/...`，不是宿主机根目录 `/.toolfs-state`），目录 root-owned `0700`；
- `.toolfs-state` 是全局 reserved path：ToolFS 永不展示，Files public-path validator 拒绝，Samba 配置增加 veto，快照 walker 显式跳过；
- 若无法在目标设备安全创建 StateRoot，则删除、truncate 和覆盖写 **fail closed**；
- 只有显式开启 `AllowUnprotectedLargeWrite` 时，大于 `MaxVersionFileSize` 的文件才允许无版本覆盖；默认返回可识别错误，不制造“看似受保护、实际裸写”的假象。

删除通常使用同设备 `renameat2` 移入 trash，递归删除目录也只移动目录本身。对仍有 ToolFS 可写 handle 或 `st_nlink > 1` 的普通文件，保护副本必须先通过 reflink/copy 与 live inode 解耦、落盘后再 unlink，不能把仍可被修改的 inode 当作唯一恢复副本。覆盖写或 truncate 在首次修改前保存旧版本；debounce 窗口内不重复保存相同基线。

### 7.3 元数据、归属与崩溃恢复

`TrashEntry` 至少记录：

- entry ID、原 public path、稳定 filesystem ID、运行时设备号、trash 相对路径；
- UserID、username、session ID；
- 原 mode、mtime、size、类型；
- create/commit 时间、保护原因和源 Grant generation。

保护流程使用 write-ahead journal：

1. 写临时 metadata，`fsync` 文件与 journal 目录；
2. 执行同设备 rename/copy；
3. 原子标记 committed；
4. 启动时扫描 pending journal 和孤儿 payload，恢复为可见 trash entry 或回滚。

```go
func (fs *ToolFS) ListTrash(ctx context.Context, session *Session) ([]TrashEntry, error)
func (fs *ToolFS) RestoreFromTrash(ctx context.Context, entryID string, session *Session) error
func (fs *ToolFS) PurgeTrash(ctx context.Context, policy RetentionPolicy, admin *Session) error
```

- `ListTrash` 只返回当前 UserID 的 entry；
- `RestoreFromTrash` 同时校验 entry 归属与当前目标权限，entry ID 使用不可预测标识；
- `PurgeTrash` 是不可恢复的管理操作，只接受 admin session，必须审计；
- retention 同时限制时长、总容量和每设备最低剩余空间。

> ZimaOS 已有一套完整回收站（`IceWhale-Files/service/trash.go`，带 `user_id` 归属、fsnotify 与 SMB recycle 联动）。ToolFS 直连文件系统不会经过它，故短期需自建；目录布局和元数据字段尽量对齐 Files，并把 `.toolfs-state` 的 Samba/public-path 排除作为同一变更交付，避免保护数据反而被用户直接访问。

---

## 8. JWT 集成

```go
type Identity struct {
    UserID, Username, Role string
    ExpiresAt              time.Time
}

type TokenVerifier interface {
    Verify(ctx context.Context, tokenString string) (*Identity, error)
}

func (fs *ToolFS) NewSessionWithToken(ctx context.Context, sessionID, jwtToken string) (*Session, error)
func (fs *ToolFS) RefreshSessionToken(ctx context.Context, sessionID, newJWT string) error
```

### 8.1 兼容现有无 `kid` JWKS

当前 UserService 的 JWK 只有 `{kty, crv, x, y}`，JWT header 也没有设置 `kid`。Verifier 必须同时支持：

1. **当前模式**：JWKS 恰好一个合法 P-256 key，token 无 `kid`，直接使用该 key；
2. **未来模式**：token 带 `kid`，按 `kid` 精确选择；未知 `kid` 刷新后仍未命中则拒绝；
3. key 数量大于 1 且 token 无 `kid` 时 fail closed，禁止逐个试验产生歧义。

验签固定 `ES256`、issuer `casaos`，校验 `exp`、`nbf`、`iat` 和有限 clock skew；拒绝 `alg=none`、HS/ES 混淆、非 P-256 曲线以及不在曲线上的坐标。

UserService 进程重启会轮换内存密钥。无 `kid` 模式下，签名失败且距上次刷新超过 `RefreshMinGap`（默认 10s）时重拉 JWKS并只重试一次；并发刷新用 singleflight。

### 8.2 过期、续期与身份不可变性

- access token 3h，默认 Strict 模式；过期后所有新 FUSE 操作返回 `ErrTokenExpired` 映射的 `EACCES`；
- 调用方通过受保护的本机 control socket 提交新 token，`RefreshSessionToken` 不接收未经鉴权的远程调用；
- 新 token 的 `UserID` 必须与原 session 完全一致，禁止利用 refresh 把挂载重绑定给另一个用户；
- username/role 如发生变化，以新 token 为准重建 `AccessSnapshot` 和 `SessionView`；capabilities 不能因 token refresh 自动扩大；
- JWT 中的 `role` 是签发时快照，不是实时撤权状态；生产 admin mount 还必须通过 UserService 当前状态查询或用户变更事件做短 TTL（默认 30s）复核，用户禁用或 admin 降级后立即失效。该契约落地前默认禁用生产 admin mount；
- session 关闭、用户登出或 refresh 超时后，`MountHandle.Close` 卸载并释放所有 root fd。

“agent 如何取得 token、谁负责每 3 小时刷新、登出是否立即卸载”可以用手动注入先做开发，但属于生产交付前必须确认的控制面，不再标记为纯非阻塞项。

### 8.3 JWKS 不可用时的策略

- 从未成功加载公钥：fail closed；
- `AllowStaleKeys` 默认 **false**；
- 如产品明确选择可用性优先，可配置有限 `MaxStaleKeyAge`，且只在 JWKS 请求失败时使用缓存 key；
- stale key 超时、明确取到新 JWKS、或新 key 验签失败时都不得继续回退旧 key；
- 使用 stale key 的每次成功验签必须输出安全告警和审计字段。

- **开发环境**：`StaticKeyVerifier`（注入测试公钥）与 `NewInsecureMockVerifier`（跳过验签，命名明示不安全），**仅显式注入生效，绝不作为自动 fallback**。

---

## 9. 审计

```go
type AuditLogEntry struct {
    // ...现有字段...
    UserID          string `json:"user_id,omitempty"`
    Username        string `json:"username,omitempty"`
    Role            string `json:"role,omitempty"`
    CallerUID       uint32 `json:"caller_uid,omitempty"`
    CallerPID       uint32 `json:"caller_pid,omitempty"`
    GrantGeneration uint64 `json:"grant_generation,omitempty"`
    Decision        string `json:"decision,omitempty"`  // 命中的授权规则摘要
    Protected       string `json:"protected,omitempty"` // "trash:<entryID>"，恢复凭据
}

func NewFileAuditLogger(dir string) (*FileAuditLogger, error) // JSONL，按日切分
```

`AccessDenied` 判定改为哨兵错误 `errors.Is(err, ErrAccessDenied)`，替换现有脆弱的字符串 `Contains("access denied")`（`toolfs.go:163`）。

审计策略：

- 授权拒绝、token 过期、UID 不匹配、provider 失败、保护失败与成功 mutation 都必须记录；
- 不记录 JWT、文件内容或 JWKS 原文；
- destructive operation 在审计 logger 不可用时默认 fail closed；
- read/list 审计失败可按配置 fail open，但必须输出进程级告警与丢失计数；
- 因授权敏感 FUSE mount 禁用内容缓存，正常文件操作不会被 kernel page cache 完全绕过审计。

---

## 10. 前置条件

| # | 前置条件 | 状态 |
|---|---|---|
| 1 | **descriptor-based P0 路径安全必须先做**（见 §11.0）；不能以 `EvalSymlinks` containment 代替 | ⚠️ **实现阻塞项** |
| 2 | IceWhale-Files 提供修复后的逐用户 Grant 接口；现有 `/share` 不作为授权输入 | ⚠️ **联调阻塞项** |
| 3 | IceWhale-Files public-path 规则可复用或固化为版本化 `PublicRootProvider` 契约 | ⚠️ **安全阻塞项** |
| 4 | IceWhale-Files 服务运行，`/run/casaos/icewhale-files.url` 可读且指向 loopback | 依赖 |
| 5 | ToolFS daemon 以 root 或最小所需 capability 运行；root 进程的可见目录由 root fd 白名单限制 | 依赖 |
| 6 | agent token 获取、refresh 主体、本机 UserID↔UID 映射，以及 admin 降级/用户禁用的实时状态契约 | ❓ **生产阻塞项** |
| 7 | 登录/登出、daemon 重启、token 过期时由谁创建、刷新和关闭 `MountHandle` | ❓ **生产阻塞项** |
| 8 | Linux `/dev/fuse`、目标用户 UID/GID、允许创建 `0700` mountpoint；macOS 仅开发验证 | 依赖 |
| 9 | 每个 backing device 可创建 reserved StateRoot，并同步配置 Files/Samba 排除 | ⚠️ **受保护写入阻塞项** |

第 6、7 项不阻塞使用注入式 token + 手动 mount 完成功能开发，但阻塞生产签收。核心 FUSE MVP 可先交付；mutation 在第 9 项未满足时保持关闭。

---

## 11. 实施计划

### 11.0 P0 安全地基与并发正确性（~3–4d，最高优先级）

已确认的问题：

| 编号 | 问题 | 证据 |
|---|---|---|
| **S1** | 路径穿越绕过访问控制 | `IsPathAllowed("/toolfs/data/../secret.txt") = true`，可读到预期 mount 外文件 |
| **S2** | 符号链接逃逸 | mount 内软链指向外部时，当前 `os.ReadFile` 可直接跟随 |
| **S3** | 只读挂载绕过 | 从可写 mount 经 `../` 可落到只读 mount 的本地目录 |
| **S4** | mounts/sessions/snapshots 竞态 | map 写入与 `resolvePath`/FUSE 遍历并发 |
| **S5** | sandbox 全局 stdout 竞态 | 当前 `go test -race ./...` 在 `sandbox.go:173` 修改 `os.Stdout` 与审计 `fmt.Println` 间报 DATA RACE |
| **S6** | mount 前缀边界错误 | `strings.HasPrefix("/toolfs/database", "/toolfs/data") == true` |

S1–S3 的第一层根因是 `normalizeVirtualPath` 不拒绝 `..`，而 `filepath.Join` 会在执行阶段 clean，形成检查/使用不一致。但即使先 `EvalSymlinks`，路径字符串仍存在 symlink-swap TOCTOU，因此 P0 必须落到 descriptor-based backend。

交付内容：

1. 新增 `sanitizeVirtualPath(path) (string, error)`：拒绝 NUL、任何 `..` segment、绝对路径重置和非法根路径；保留 `normalizeVirtualPath` 仅做字符规范化。
2. 新增 `SecurePathBackend`：
   - Linux：root dirfd + `openat2` / `unlinkat` / `renameat2` / `mkdirat`；
   - macOS：逐段 `openat` 开发 fallback；
   - 单测使用可注入 backend。
3. 所有 virtual/mount/skill prefix 改为 segment-aware match。
4. 引入 `PublicRootProvider`，admin 与普通 Grant 都先受 public root 上界约束。
5. `mounts` / `skillMounts` / `sessions` / `snapshots` 加锁；缓存和 matcher 使用不可变 snapshot + atomic publish。
6. 移除 `InMemorySandbox` 对进程全局 `os.Stdout`/`os.Stderr` 的并发劫持，改为显式 writer 或后续 wazero 原生捕获。
7. 拒绝路径统一使用哨兵错误并记录 `success:false`。

验收：

- S1–S6 回归测试先红后绿；
- 增加并发 symlink swap、新建文件父目录替换、rename 源/目标替换测试；
- `go test ./...`、`go test -race ./...`、`go vet ./...` 全绿；
- Linux 安全 backend 测试证明所有实际 IO 都从受约束 dirfd 发起。

### 11.1 阶段表

| 阶段 | 内容 | 关键文件 | 验收 |
|---|---|---|---|
| **P0**（~3–4d） | descriptor-based path backend、public roots、全部 race 修复 | `toolfs.go`；新增 `securepath_*` | 见 §11.0 |
| **P1**（~2–3d） | IceWhale-Files 逐行 Grant API、`PermissionProvider`、immutable `AccessSnapshot`、alias/冲突规则、`SessionView` | Files 新增 internal route；ToolFS 新增 `rbac.go`、`permission_provider.go`、`namespace.go` | 删除/missing Grant 不返回；零 Grant deny all；admin 不突破 PublicRoots |
| **P2**（~3–4d） | **只读 per-session FUSE 垂直切片**、UID 绑定、`MountHandle`、禁用授权敏感缓存 | 重构 `fuse_adapter.go` | **用户1/用户2分别只见自身目录**；跨 UID 访问拒绝；撤权后缓存不继续泄露 |
| **P3**（~3–4d） | 无 `kid` JWT、admin 当前状态复核、refresh control socket、token/session/mount 生命周期、主动 generation 失效 | 新增 `auth*.go`、`mount_manager.go` | 换钥、过期、admin 降级、同用户续期、异用户重绑定拒绝、登出卸载 |
| **P4**（~4–6d） | 受保护 mutation、per-device trash/version、journal、审计落盘、Samba/Files reserved-path 排除 | 新增 `destructive.go`、`trash.go`、`state_root.go`、`audit_file.go` | 删除→恢复；覆盖→版本；子挂载设备；崩溃恢复；无 StateRoot 时 fail closed |
| 延后 | btrfs 快照 / 闪回 | —— | 单独立项：需把目录转为 subvolume，属侵入式存储改造 |

### 11.2 排期判断

- P0–P2 约 **8–11 人天**，形成可手动注入身份的安全只读挂载 MVP，直接满足核心可见性验收；
- P3 完成后约 **11–15 人天**，达到可持续运行的身份与挂载生命周期；
- P0–P4 约 **15–21 人天**，另需 **3–5 人天**做真实 ZimaOS、Samba、外接盘和故障联调。

因此“16 人天且包含缓冲”不成立。若 8 月第二周是固定节点，建议该节点验收 P0–P2（争取包含 P3）；P4 完成前挂载保持只读，或者仅开放不覆盖已有数据的 create。若节点必须同时包含完整回收站与版本恢复，需要并行投入或缩减功能范围。

---

## 12. 风险与取舍

1. **当前 FUSE 完全绕过 session**：在 P2 落地前，现有 `MountToolFS` 必须明确为 unsafe/development API，不能用于多用户环境。
2. **上游 Grant 契约是新增依赖**：不先修 IceWhale-Files 就无法安全消费授权。好处是把修复放在事实源处，避免每个消费者重复猜测 `/share` UI 模型。
3. **root daemon 爆炸半径大**：descriptor root fd 与 PublicRoots 是核心隔离边界；任何回退到绝对路径 `os.*` 的代码都应被 code review/静态检查禁止。
4. **撤权存在 TTL**：事件落地前最长约 30s；mutation 强制 fresh，read 依靠短 TTL。若产品要求即时撤权，必须由 IceWhale-Files 发布 share generation 事件。
5. **FUSE kernel cache 会绕过应用层**：MVP 牺牲部分性能关闭缓存；之后只能在拥有可靠 generation→inode invalidation 映射后逐步开启。
6. **StateRoot 与 SMB/Files 共用磁盘**：`.toolfs-state` 必须同时从 ToolFS、Samba、Files API 和旧 snapshot walker 排除，否则可能泄露保护副本或递归自吞。
7. **空间与磁盘故障**：trash rename 不释放空间，versions 会增加写放大；retention、配额、低空间阈值和 fail-closed 必须默认开启。
8. **外部写入不受保护**：SMB、SSH 或其他 root 进程直接修改原文件不会经过 ToolFS 版本钩子。本方案只保证经 ToolFS 发起的 agent 操作。
9. **macOS 安全能力较弱**：macFUSE + `openat` fallback 用于开发；生产验收以 Linux `openat2` 为准。
10. **命令执行与文件访问是两条边界**：`DangerousCommandFilter` 仍需从 contains 改为精确命令解析，但不能把它包装成 FUSE 的 `OpExecute`。
11. **依赖与通用性**：`golang-jwt/jwt/v5` 等为纯 Go；`ZimaOS-Common` 会增加耦合。对外通过 `PublicRootProvider`、`PermissionProvider` 和 matcher 接口隔离 ZimaOS 实现。
12. **既有 JWT 不提供即时角色撤权**：3h token 内的 `role` 会过时；普通用户仍由 Grant TTL 收窄，但 admin bypass 必须依赖 §8.2 的当前状态复核，不能只等 token 过期。

---

## 13. 验证方式

1. **安全路径 backend**
   - `../`、重复 slash、反斜杠、NUL、绝对路径重置全部拒绝；
   - 软链指向 Grant 外、父目录并发 symlink swap、目标不存在时替换最近存在祖先均失败；
   - `/toolfs/data` 不命中 `/toolfs/database`；
   - admin 访问 `/etc`、`/root/.ssh` 等 PublicRoots 外路径失败；
   - read/write/create/unlink/rename 的系统调用都以受约束 dirfd 发起。
2. **Grant API 与缓存**
   - 构造同一路径一条 active、一条 deleted 的混合数据，deleted 用户绝不产生 Grant；
   - missing storage、未知用户、空列表、畸形 JSON、超大响应、timeout、redirect 全部 fail closed；
   - `/DATA` 与 `/media/ZimaOS-HD` 归一为同一路径；同路径 RO/RW 冲突按 RO；
   - 并发 cached/fresh/invalidate 不发布旧 generation，不产生 data race。
3. **SessionView**
   - 同名 share、嵌套 Grant、父 RW/子 RO、多个 storage 都得到稳定且无冲突的虚拟名；
   - 零 Grant 用户看到空根目录；
   - admin 只看到 PublicRoots；
   - RBAC MVP 不出现 `memory`、`rag`、skill mounts。
4. **FUSE 端到端**
   - ZimaOS 建两个子用户并授权不同目录，各自 mount 只见自身 entry；
   - 用户2直接访问用户1 mountpoint，因 caller UID 不匹配返回 `EACCES`；
   - 撤销 Grant 后验证 `Readdir`、cached path、已有 inode 和已打开文件均在 TTL/事件后拒绝；
   - JWT 过期、session close、daemon restart 均能安全卸载或恢复，不遗留可访问挂载；
   - `MountHandle.Close` 幂等，无 fd/inode/goroutine 泄漏。
5. **JWT**
   - 覆盖现有“单 key、无 kid”JWKS；
   - 覆盖未来多 key + kid、无 kid 多 key拒绝、正常验签、过期、错 issuer、alg 混淆、非 P-256、签名失败换钥重拉；
   - 新旧 UserID 不同的 refresh 被拒，同 UserID role 变化会重建视图；
   - 有效期内 admin token 对应用户被降级/禁用后，状态事件或短 TTL 到达即拒绝并卸载；
   - JWKS 宕机时默认 fail closed，显式 stale 模式受最大时长限制。
6. **mutation 语义**
   - 使用真实命令覆盖 `touch`、`mkdir`、append、`cp`、`mv`、编辑器 temp+rename、`truncate`、`rm`、`rm -r`；
   - rename 分别覆盖同 Grant、跨 Grant、覆盖目标、跨设备；
   - 并发 write/unlink、两个 rename 互换目标、旧 writable handle 写已删除文件均不会破坏保护副本或形成死锁；
   - 任一权限或 fresh check 不满足时，原文件与保护区均不发生部分修改。
7. **回收站与版本**
   - 内置盘、外接盘、Grant 内子挂载分别落入对应 `st_dev` StateRoot；
   - 重启后 `st_dev` 变化时仍通过稳定 filesystem ID 找回原 journal/trash，不串到其他磁盘；
   - 删除→恢复、覆盖→版本恢复、同名恢复冲突、跨用户猜 entry ID、retention purge；
   - 在 journal 各阶段模拟 kill、断电、磁盘满和只读 remount，重启扫描不丢 payload、不误恢复；
   - `.toolfs-state` 在 ToolFS、Files API、SMB 和旧 snapshot 中均不可见。
8. **一致性不变量**
   - 对同一 `(identity, operation, public path)` 做 ToolFS 与 IceWhale-Files 交叉验证；
   - 要求 `ToolFS allow ⇒ Files 基础授权 allow`，反向不要求成立，因为 ToolFS 的 capability、deny pattern 和保护策略可以更严格。
9. **质量门禁**
   - `go test ./...`、`go test -race ./...`、`go vet ./...`；
   - Linux 真实 FUSE 集成测试纳入 CI 或专用 runner；
   - benchmark 记录禁用 cache 后的 read/list/open 基线，性能优化不得破坏上述安全用例。
