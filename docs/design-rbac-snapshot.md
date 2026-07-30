# ToolFS 技术方案：基于 ZimaOS 用户授权的多用户挂载文件系统（RBAC/ACL）

> 状态：设计稿阶段（v7，最新一版；未评审、未确认，不作为实施依据）
> 日期：2026-07-30（v7，含宿主编排与挂载结构修订）
> 关联系统：[IceWhale-Files](https://github.com/IceWhaleTech/IceWhale-Files)（**权限事实来源**）、[ZimaOS-UserService](https://github.com/IceWhaleTech/ZimaOS-UserService)（JWT 签发）、ZimaOS-LocalStorage（`/DATA` 挂载）
>
> **版本演进**
> - v1：设计了一套 ToolFS 自有的 Policy 配置文件。
> - v2：实证复核发现 v1 有事实错误（误认为 `normalizeVirtualPath` 可防 `../` 逃逸）及多处危险默认值，新增 P0 路径安全阶段。
> - v3：调查 ZimaOS 全栈后推翻 v1/v2 的核心设计——ToolFS 不再自定义授权，而是消费 ZimaOS 既有的权限事实来源；将「按用户的挂载视图」确立为核心交付物。
> - v4：对 v3 做安全与可实施性分析，修正 `/share` 聚合接口不适合作授权来源、`EvalSymlinks` 无法消除 TOCTOU、FUSE 本机调用者与内核缓存绕过、固定 `/var/lib/toolfs` 跨设备失败、现有 JWKS 无 `kid` 等问题；引入逐行 Grant 接口、descriptor-based IO、`InstanceView`、`MountHandle` 和 per-device StateRoot，并把核心 FUSE 验收前移。
> - v5：补齐快照与闪回设计（v3/v4 曾整体延后）。关闭 v4「versions 只写不读」的缺口；确立**快照（系统级只读时间点）与闪回（用户级、受当前 Grant 约束的写回）分权**；引入统一的三层恢复源（versions / trash / snapshot）与 `ListRestorePoints`；btrfs backend 以 dirfd 对齐 descriptor-based 模型，`.toolfs-state` 改为独立 subvolume 从文件系统层面根除快照自吞；**去掉 copy 型快照降级**。
> - v6：确定 agent 为容器内命令行调用（`toolfs run -- codex`），恢复**完整 RBAC** 为当期目标并更正 v5「共享容器无法区分请求归属」的错误结论——身份经 `--token` 显式传入、由挂载点绑定授权范围；新增 **mount namespace 任务隔离**与**排除规则**（否则 `npm install` 会打满磁盘）；建议容器由 privileged 收窄为 `SYS_ADMIN + /dev/fuse`。
> - **v7（本版）**：依据 `IceWhaleTech/codex` **源码调查**重写 §14。查明 agent 实为「常驻 Web 服务 + 常驻 app-server」，codex core 内嵌于 app-server 进程（无 per-task 进程边界，`toolfs run -- codex` 模型不适用）；实测 agent 以 **`seluser` 零 capability** 运行（更正 v6「privileged 容器可绕过」的判断）；确立**每用户一个 app-server 实例**为 per-user RBAC 的落地方式（codex 侧零改动，Web 服务侧 40 处调用点改造）；查明 **codex 账号体系与 ZimaOS 独立**，账号绑定成为前置条件。
> - **v7 修订（2026-07-30）**：编排职责细化为**宿主侧 `agentd` 拉起实例容器 + `toolfsd` 仅负责挂载**（§14.2.2），实例容器降为零特权；实测挂载传播，确立「bind 源取 FUSE 挂载点父目录 + `rslave`」并**推翻此前「无需挂载传播」的判断**（§6.6）；补充**共享目录的跨用户并发语义**，确定 toolfsd 单进程 serve 全部挂载（§6.5）；以最小 FUSE passthrough 实测跨挂载可见性，定位缓存缺口在 negative entry（§2.5⑨）。

---

## 1. 交付目标与验收标准

来自 `workplan/summary.md`：

> ToolFS 下的基于 RBAC 挂载文件系统 —— **"测试可以这样测：用户1能看到 xxx 挂载目录，用户2能看到 xxx 挂载目录"**。本周交技术方案，**8 月第二周交付产物**。

上层业务目的：给 AI agent 一个统一、安全的文件访问工具，**防止误删与越权**。

### 1.1 交付范围：完整 RBAC

**当期目标即完整方案**：用户身份 + 逐用户授权 + 防误删保护，一并交付。

此前曾考虑拆成「先做与身份无关的防误删、后做 RBAC」两期，理由是「共享容器内 ToolFS 无法区分请求归属」。**该结论已被推翻**：

> 那个判断建立在「身份必须从 FUSE 的 caller UID **推断**」这一前提上——同一容器内进程恒为同一 uid，确实推断不出。
> 但**每用户一个实例**后，身份在**挂载点创建时显式绑定**：挂载点绑定谁的授权，被 bind 了该挂载点的实例就只能访问谁的目录。
> **承载身份的是挂载点，不是实例**——实例对身份完全无感，它只是看到一个目录，因而无需推断 caller。

隔离由**容器边界**保证（§14.2.2）：每用户一个实例容器，宿主侧的 `toolfsd` 建立挂载后仅 bind 给对应实例；真实数据与保护区不进任何容器。不同用户的会话、记忆与数据因此互相隔离。

### 1.2 能力边界（诚实声明）

| 范围 | 保证 |
|---|---|
| **挂载点内的操作** | **完整 RBAC**：只见授权目录、删除权限默认不授予、审计记录真实用户身份 |
| **实例之间** | 独立容器，挂载 / 进程 / 网络视图由 runtime 隔离；对方的挂载路径在本容器内**根本不存在** |
| **真实数据与保护区** | **不 bind 进任何容器**——回收站、版本、审计位于实例触及范围之外（§14.4） |
| **蓄意绕过** | 实例容器**零内核特权**：无法挂载、无法访问块设备。当前设计下无已知可行绕过路径 |

> 该结论以 **`toolfsd` 与 `agentd` 运行在宿主**为前提。若回退到「同容器内多进程」形态（§14.2.3 已排除），隔离将退化为依赖「独立 uid + PID namespace + `hidepid`」的配置拼装，且 `/proc` 穿透风险重现（§14.2.1）。

**不提供**：主数据的时间点快照（ext4 能力上界，§2.5①）；对经 SMB / 其他通路的修改的保护；对「未授权者驱使 agent」的防护（需 agent 自身鉴权，§14.13）。

### 1.3 术语说明

ZimaOS 当前模型实质上是：

- `role=admin/user` 的粗粒度角色；
- `(username, path, read_only)` 的逐用户路径 ACL；
- ToolFS 在其上增加 per-agent capability 收窄。

因此本文沿用项目中的 “RBAC” 名称，但实现上不是纯角色继承模型，而是 **RBAC + 用户路径 ACL + per-instance capability**。

> **「Session」在本文有两个不同含义，勿混淆：**
> - **ToolFS 的 `*Session`**（Go API 参数）：一个挂载点的**授权上下文句柄**，持有 `ZimaIdentity` + `AccessSnapshot` + `AgentCapabilities`。由 `MountForIdentity` 创建，**粒度是 per-user（每挂载点一个）**，与 agent 的会话数无关。
> - **agent 侧会话**：codex 的对话线程。同一用户的多个会话**共享**同一个 ToolFS 挂载与同一个 `*Session`。`ChangeRecord.SessionID`/`TaskID` 指的是这一种（§7.8）。

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
| **ZimaOS 自身对 admin 的处理是分裂的** | **SMB 是数据驱动**：`service/smb.go` 全文无 `admin`/`role`，配置纯按 share 表逐用户生成，admin 看到的就是他在表里的行。**Web UI 是特权驱动**：`service/shares.go:618` `CheckPermission` 第一行 `if role=="admin" { return true }`，压根不查表。两个视图对 admin 会给出不同答案 |
| **admin 的 share 行是自动写入的，不是特权换来的** | `samba:create-user-end` 事件 → `service/message.go:139` `StartAllStorageSMBShareWhenSambaUserBeCreate` → 遍历 storage，给 `getUsername()` 取到的第一个用户（= 主用户 = admin）建 **rw** share。**注意仅在 `len(shares)==0` 时创建**，已有 share 的存储不会再自动加 admin |
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

1. **授权事实**：share 表；
2. **宿主路径范围**：`NormalizePublicPath` / `ValidatePublicPath` 对 `/media`、`/DATA` 等受管根目录的约束。

ToolFS 必须同时复用或等价实现这两个约束。

**v5 决策：admin 走数据驱动，与 SMB 视图对齐，不做特权放行。**

ZimaOS 自身在 admin 语义上是分裂的（SMB 查表 / Web UI 特权放行）。ToolFS 选择 SMB 那一侧，即 **admin 与普通用户共用同一套 Grant 构建逻辑，ToolFS 内不存在 `if role=="admin"` 的旁路**。理由：

- **实际可见范围几乎不变**：自动授权机制已经把各存储根的 rw share 写进表里，admin 照常能看到它们；
- **权限变成显式、可查、可审计、可收窄**：想限制某个 admin agent 的范围，删几行 share 即可，走既有流程；
- **特权旁路换来的唯一额外能力是「share 行被删光时仍能访问」**——这恰恰是不应该要的行为；
- 少一条绕过全部检查的代码路径，root daemon 的爆炸半径显著变小。

`PublicRoots` 因此退化为**纯粹的硬上界**（任何 Grant、StateRoot、恢复路径都不得越过），而不再是 admin 的可访问集合。

> `role` 字段在 v5 中仍有用，但只用于**管理操作的准入**（`PurgeTrash`、`CreateSnapshot`、`RollbackVolume`），不参与任何文件路径的可见性与读写判定。

### 2.5 真机实测结论（ZimaCube Pro，ZimaOS v1.7.0，10.0.0.75）

以下均为在真实设备上只读采集的结果，**推翻了本方案此前的一项核心假设**。

#### ① 主存储 `/DATA` 是 ext4，不是 btrfs —— 快照能力不可用

```
/DATA              /dev/nvme0n1p8   ext4     223G 已用 87%，剩 31G
/media/HDD-Storage /dev/sda         btrfs    932G 已用 5%
/media/sdb_1       /dev/sdb         exfat
```

LocalStorage 的 `mkfs.btrfs` 只作用于**数据盘**（新单盘、RAID/JBOD），系统盘分区由 OS 镜像层格式化为 ext4。而 `/DATA` 恰恰是用户主数据所在（LocalStorage 创建的 Documents / Downloads / Media 等 8 个默认目录都在这里）。

**结论：快照能力按存储而异，不是全局能力。** `/DATA` 上只有 versions + trash；只有 btrfs 数据盘能做快照。这反向验证了 per-device StateRoot 设计的必要性——不同存储的保护能力本就不同。

#### ② ext4 不支持 reflink —— versions 的代价按存储而异

实测 `cp --reflink=always`：`/DATA`(ext4) 失败，`/media/HDD-Storage`(btrfs) 成功。

| 存储 | trash（rename） | versions（保护副本） |
|---|---|---|
| `/DATA` ext4 | O(1) | **O(size) 全量拷贝** |
| btrfs 数据盘 | O(1) | O(1) reflink |

`/DATA` 仅剩 31G（已用 87%），全量拷贝的版本保护在此处空间压力很现实。`MaxVersionFileSize` 的默认值需按存储剩余空间动态评估，不能一刀切 64MB。

#### ③ btrfs 盘挂在顶层子卷，无嵌套子卷

`btrfs subvolume show /media/HDD-Storage` → `<FS_TREE>`，Subvolume ID 5；`subvolume list` 为空。顶层子卷可被快照，`.toolfs-state` 可按 §7.5.3 建成独立子卷，方案可行。

#### ④ admin 的「数据驱动」视图实证成立

用户表：`casaos` id=1 role=**admin**；`subuser` id=10000 role=**user**。

share 表中 `casaos` 的有效行（`is_deleted=0`）恰好 3 条：`/media/ZimaOS-HD/`、`/media/HDD-Storage`、`/media/sdb_1`，全部 rw。而 `/etc/samba/casa.casaos.conf` 里正好是 `[ZimaOS-HD]` `[HDD-Storage]` `[sdb_1]` 三个 section。

**admin 在 SMB 上看到的就是他 share 表里的行，没有任何特权全盘。** §2.4 的决策得到实证：改为数据驱动后 admin 可见范围不变。

#### ⑤ 真实存在「父目录 RW、子目录 RO」的 Grant

`subuser` 的三条有效行：

```
/media/HDD-Storage            read_only=0   (rw)
/media/HDD-Storage/rauc/temp  read_only=1   (ro)   ← 父 rw、子 ro
/media/sdb_1                  read_only=0   (rw)
```

这验证了 §4.4 规则 4（最长路径命中、子目录保持 RO）不是假想场景。

**并且暴露了 SMB 侧的一个缺陷**：SMB 把每条 share 行渲染成独立顶层共享（`casa.subuser.conf` 里 `[temp]` `[HDD-Storage]` `[sdb_1]`），因此 `temp` 的只读限制**可以通过 `[HDD-Storage]` 这个 rw 共享走 `rauc/temp` 路径绕过**。HTTP API 侧因为用 PathMatcher 最长前缀匹配，只读生效。

ToolFS 与 HTTP API 侧一致（最长前缀），即**比 SMB 更严格**。这符合 `ToolFS allow ⇒ Files allow` 不变量，但与 SMB 行为存在可见差异，需在交付说明中写清。

#### ⑥ `is_deleted` 权限放大风险实证

share 表共 14 行，其中 **8 行 `is_deleted=1`** 全部属于 `casaos`。若按 §5.1 所述误用 `/share/permission` 端点（不过滤 `is_deleted`），admin 会额外拿到这 8 条已撤销授权。**§5.1 改用 `/share` 并过滤 `Status` 的决策由此得到实证支持。**

#### ⑦ `/media/ZimaOS-HD` 是指向 `/DATA` 的符号链接

```
lrwxrwxrwx /media/ZimaOS-HD -> /DATA
```

且 share 表里存的是带尾斜杠的 `/media/ZimaOS-HD/`。因此路径规范化必须同时处理：尾斜杠、`/DATA` ↔ `/media/ZimaOS-HD` 别名、以及**该别名本身就是一个符号链接**。Grant root 的 dirfd 在打开时会解析到 `/DATA`，canonical 形式需固定选一侧（建议 `/DATA`，即 `readlink -f` 的结果），避免同一目录产生两个 NamespaceEntry。

#### ⑧ 挂载传播：内容无关传播，但挂载点结构决定撤权是否失效

Docker 28.3.2，dockerd 以 `MountFlags=shared` 运行且与宿主同 mount namespace；新建挂载默认继承 `shared`，故 `rslave` 无需额外 `--make-rshared`。

| 实测项 | `rprivate`（默认） | `rslave` |
|---|---|---|
| 宿主↔容器文件内容互见 | ✅ | ✅ |
| 容器启动后新增挂载可见 | ❌ | ✅ |

关键发现：**bind 源若是 FUSE 挂载点本身，重挂后容器静默读到旧数据**（非报错），使 toolfsd 重启后的撤权失效。改为 bind 挂载点的父级普通目录后，重挂立即生效、卸载立即 `ENOENT`。详见 §6.6，该结论推翻了早先「无需挂载传播」的判断。

#### ⑨ 跨挂载可见性实测（最小 FUSE passthrough）

用 go-fuse v2.9.0 写了约 50 行 loopback passthrough，在同一后端目录上建立**两个独立 FUSE 挂载**（模拟用户 A、B 的挂载视图），验证 §6.5 的结论。两个挂载 `st_dev` 分别为 89 与 179，**确为不同 superblock**，非 bind 共享。

| 配置 | 观测 |
|---|---|
| **TTL=0**（MVP 配置） | A 写 → B 即时读到；B 写 → A 即时读到；A 创建/删除/mkdir → B 即时反映；`readdir` 即时包含对方新建项。**双向、无延迟** |
| **TTL=30s + `KEEP_CACHE`** | 内容**未**陈旧（含等长覆盖写）；正向 entry 与删除**未**陈旧；**negative entry 陈旧恰好 30s**——A 创建的文件，B 持续 `ENOENT` 至第 30 秒才可见 |

两项配套确认：`KEEP_CACHE` 确实下发（`OPEN reply flags=0x2, keep_cache=true, direct_io=false`），排除了标志未生效导致的假阴性；内核为 6.18.9。

**结论**：①跨挂载可见性由**后端文件**保证，与挂载传播（§6.6）完全无关；②MVP 的 TTL=0 配置下无需任何额外机制；③放开缓存后的真实破绽是 negative entry；④内容未陈旧属内核 6.18.9 的自动失效行为，非协议保证，**不可作为设计依据**。

> 实验用挂载、进程与临时文件已全部清理，未触碰生产数据。

---

## 3. 已确认的决策

| 议题 | 决策 |
|---|---|
| agent 权限与用户权限的关系 | **继承用户权限 + 操作收窄**，永不放大 |
| admin 语义 | **数据驱动，与 SMB 视图对齐**：admin 也只从 share 表构建视图，ToolFS 内无 `if role=="admin"` 旁路；`role` 仅用于管理操作准入 |
| ToolFS 的 IO 路径 | **鉴权用 IceWhale-Files 的逐行 Grant，IO 通过受 Grant 根 dirfd 约束的 descriptor-based backend 直连文件系统** |
| 权限数据获取方式 | **新增/修复 IceWhale-Files 内部逐用户 Grant HTTP API；不使用 `/share` UI 聚合模型做授权** |
| 模块定位 | 快照/闪回是 **ToolFS 的独立模块，不依赖也不对接 ZimaOS backup**；服务对象是 **AI agent 的操作安全**，不是灾备 |
| 身份传递 | **挂载点承载身份**——每个挂载点绑定某用户的 Grant；app-server 实例通过运行在该挂载视图内而受约束，**实例本身对身份无感** |
| 实例编排 | **宿主侧分工**：`agentd` 拉起并回收实例容器，`toolfsd` 只负责按用户建立挂载；实例与挂载点一一对应（§14.2.2） |
| 实例隔离 | **已选定独立容器**。`/proc/<pid>/root` 可穿透 mount namespace（实测），故「同容器多进程 + mount namespace」不成立；独立容器不共享 PID namespace，从源头消除（§14.2.1/14.2.3） |
| 挂载位置 | **宿主挂载、bind 进容器**——FUSE 由宿主 `toolfsd` 挂载（实例容器零特权，无 `/dev/fuse`）；agent 运行在容器内是因为 codex 工具链只存在于镜像 |
| 挂载点结构 | **bind 源必须是 FUSE 挂载点的父级普通目录** + `rslave` 传播；否则 toolfsd 重挂后容器静默读到旧视图、撤权失效（实测，§6.6） |
| 共享目录并发 | 多用户可对同一目录都有 rw 授权，其挂载点指向同一份后端数据；**可见性天然成立**（实测，§2.5⑨），需处理的是保护链协调与撤销冲突（§6.5） |
| 实例启动 | **预热池**：预热容器空转仅 2.4 MB，分配延迟 136 ms（实测）。slot **一次性使用**，绝不跨用户复用（§14.10.7） |
| 实例通信 | **unix socket**（`--listen unix://PATH`）——app-server 拒绝无鉴权的非 loopback TCP 监听，socket 经 bind mount 暴露给 Web 服务（§14.10.8） |
| toolfsd 进程模型 | **单进程 serve 全部挂载**——版本去抖与计数需跨挂载互斥，进程内 mutex 最可靠；崩溃可由 systemd 拉起并重挂（§6.5） |
| 集成方式 | **改 `WEB_STREAMING_BUSINESS_ROOT` 指向挂载点**，codex 侧零改动 |
| 账号绑定 | agent 与 ZimaOS **两套独立账号体系**，请求不带 ZimaOS token；须经用户自助绑定或管理员映射（§14.6）。ToolFS 侧以 `IdentityResolver` 抽象隔离，**不阻塞开工** |
| 排除规则 | **必备项**：`.git/`、`node_modules/` 等不存版本（但删除仍进回收站），否则一次 `npm install` 即打满仅剩 31G 的 `/DATA` |
| 恢复能力 | **三层恢复源统一为一条时间线**：versions（覆盖前旧内容）/ trash（删除）/ snapshot（整卷时间点） |
| agent 核心能力 | **会话/任务级撤销**（§7.8）——建立在 versions+trash 之上，不依赖 btrfs，在 ext4 主存储上同样可用 |
| 快照与闪回分权 | **快照 = 系统级只读时间点（admin）；闪回 = 用户级写回（受当前 Grant 约束）**；整卷回滚仅 admin + offline |
| 快照后端 | **仅 btrfs**；非 btrfs 存储明确 `ErrSnapshotUnsupported`，**不提供 copy 型降级** |
| 保护能力的粒度 | **按存储而异，不是全局能力**（§2.5①）。实测主存储 `/DATA` 是 ext4 → 只有 versions + trash；btrfs 数据盘才有快照 |
| 跨文件系统的架构 | **分层：语义统一 + 能力显式 + 实现适配**（§7.2.1）。统一的是「恢复点」抽象，不是「快照」实现；能力挂在 per-device `StateRoot` 上并**驱动策略**，不只用于展示 |
| 消费形态 | Go 库 + FUSE（**每用户一个挂载点**；同一用户的多个 agent 会话共享之。仅当需要不同 capabilities 的并行实例时才追加实例 ID） |
| 身份来源 | **不经 JWT 验签**——agent 与 ZimaOS 账号体系独立，请求不携带 ZimaOS token；改由 `IdentityResolver` 解析绑定关系（§14.6） |
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
不可变 InstanceView{NamespaceEntries, AccessMatcher}
        ↓ secure path backend（dirfd + openat2/*at）
allow / deny + 实际 IO
```

### 4.1 操作粒度——ToolFS 的增量价值

ZimaOS 只有 `read_only` 一个维度，「可写」即「可删」，这正是 agent 误删风险的根源。ToolFS 将其拆开：

| ZimaOS share | ToolFS 授予 |
|---|---|
| `read_only = true` | `OpRead`, `OpList` |
| `read_only = false` | `OpRead`, `OpList`, `OpCreate`, `OpModify` |
| `read_only = false` 且 capability 显式开启删除 | `OpDelete`；只读 Grant 永远不能被 capability 提升为可删 |

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

最终权限始终是「ZimaOS 允许的最大集合」与该实例 `AgentCapabilities` 的交集。`AllowDelete` 只能恢复 ToolFS 默认收窄掉的 RW 删除能力，不能提升只读 Grant。`DenyPatterns` 在挂载建立时针对 canonical 相对路径编译；非法 pattern 直接拒绝建立挂载，匹配失败不能按 allow 处理。

`OpExecute` 不属于文件系统权限：FUSE 无法阻止进程先读取脚本、再执行 `bash script.sh`。命令/代码执行能力只在 ToolFS 自己的 command/skill API 中通过 `AllowCommandExecution` 控制，不能宣称由挂载文件系统强制执行。

`role == "admin"`：**在文件访问层没有任何特殊待遇**（见 §2.4）。admin 与普通用户走同一套 Grant 构建与判定逻辑——他能看到什么，取决于 share 表里以他为 `samba_user` 的行，而那些行通常由系统自动创建。

`role` 只用于**管理操作准入**：`PurgeTrash`、`CreateSnapshot`/`DeleteSnapshot`、`RollbackVolume`。这些操作不针对具体文件路径，无法由 Grant 表达。

其余约束对所有身份一律生效：

- 任何路径都不得越过 `PublicRootProvider` 的硬上界；
- `AgentCapabilities` 与 deny patterns 照常生效；
- `OpDelete` 默认关闭；
- storage root 本身不可删除、覆盖或重命名，只能作为 `WriteInto` 容器。

### 4.2 核心修正：descriptor-based 安全 IO

仅执行 `filepath.EvalSymlinks` 后再调用 `os.Open`/`os.Rename` **不能消除 TOCTOU**：检查和使用之间，父目录或软链仍可能被 SMB、其他本机进程或并发 agent 替换；`O_NOFOLLOW` 也只保护最终分量。v4 不再把“解析后的路径字符串”当作安全句柄。

统一流水线改为：

```
虚拟路径
  → sanitizeVirtualPath（拒绝 NUL、绝对重置、任何 .. path segment）
  → InstanceView.Resolve（得到 NamespaceEntry + 相对路径）
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
- 响应 generation/ETag，供 ToolFS 判断是否需要重建 InstanceView；
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
- snapshot 更新后通知对应 `MountHandle` 重建 InstanceView，并失效 FUSE inode/dentry/content cache。

失败与新鲜度规则：

| 操作 | 缓存策略 |
|---|---|
| 已开始的只读请求 | 允许使用请求开始时持有的 TTL 内 snapshot |
| 新 open / list / stat | snapshot 过期且刷新失败时 fail closed |
| create / modify / truncate / setattr shrink | `RequireFresh`；刷新失败即拒绝 |
| delete / rename / 覆盖已有目标 / restore | `RequireFresh`；刷新失败即拒绝 |

`Invalidate` 与强制刷新必须通过 singleflight + generation compare 原子完成，不能采用“先删 cache、再普通读取”的竞态流程。任何上游错误、解析错误或空响应都不能自动回退到“无限期使用旧授权”。

---

## 6. per-user 挂载视图（核心交付物）


`MountToolFSWithSession`（`fuse_adapter.go:326`）当前是**空壳**——注释写明 "placeholder for future implementation"，直接转调无 session 的 `MountToolFS`，各 FUSE handler 全部调用无 session 版本，即当前 FUSE 路径完全绕过访问控制。

v4 起不把授权规则直接塞进现有全局 `fs.mounts`，而是为每个挂载实例（per-user）构建独立、不可变的命名空间：

```go
type NamespaceEntry struct {
    GrantID     int
    VirtualName string
    PublicPath  string
    LocalRoot   string
    ReadOnly    bool
    RootFD      int // Linux；由 SecurePathBackend 管理
}

type InstanceView struct {
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
- **admin 与普通用户一样，只从自己的有效 Grant 构建 entry**（§2.4）——不存在「admin 看到全部 storage」的特殊分支；他通常能看到各存储根，是因为自动授权已把那些行写进 share 表，而非因为角色特权；
- **挂载粒度为 per-user**：一个用户一个挂载点、一个实例容器，该用户的多个 agent 会话共享之；
- **挂载点必须位于 bind 源的下一层**，不能让 bind 源本身就是 FUSE 挂载点（实测依据见 §6.6）：

  ```
  /mnt/toolfs/<user-id>/                 ← 稳定普通目录，作为 bind 源，永不 umount
  /mnt/toolfs/<user-id>/workspace/       ← FUSE 挂载点，toolfsd 可自由 umount / 重挂
  ```

- 若同一用户需要不同 capabilities 的并行实例，可在 workspace 层追加实例 ID（`<user-id>/<instance-id>/`），但**默认不必**——capabilities 是按用户配置的；
- mount path 不直接使用未经校验的 username；目录名使用数字 user ID 与不可预测的实例 ID。

当前根节点固定暴露 `memory`、`rag` 和 skill mounts，它们没有 ZimaOS share 授权来源。核心 RBAC FUSE MVP **不暴露这些虚拟后端**；后续如需合入，统一放到 `/tools` 并使用独立 `ToolCapabilities`，不能因用户拥有某个文件 Grant 就自动开放。

**唯一例外是 `.toolfs/`**——per-instance 的**只读控制视图**（§14.9）。它不来自 Grant，但也不泄露 Grant 之外的任何数据：内容仅为当前实例自身的恢复点、变更记录与回收站条目，且逐条目按**当前** Grant 过滤。它与 `.toolfs-state` 同属**全局 reserved name**：真实数据中出现同名目录时以虚拟视图优先并记录告警，禁止用户数据遮蔽控制视图。

### 6.2 MountHandle 与生命周期

```go
type NamespaceMode uint8
const (
    NamespaceHide NamespaceMode = iota // 默认：无权路径不可见
    NamespaceEACCES                    // 仅诊断场景
)

type MountConfig struct {
    Mode            NamespaceMode
    // 保留字段：仅在回退到同容器多进程形态时使用（§6.3）
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

// 为某个 ZimaOS 授权主体建立挂载。调用方为 toolfsd，
// 挂载建立后由 agentd bind 给对应的实例容器（§14.2）。
func MountForIdentity(
    ctx context.Context,
    toolfs *ToolFS,
    mountPoint string,
    identity *ZimaIdentity,     // 由 IdentityResolver 解析所得（§14.6.4）
    caps AgentCapabilities,
    config MountConfig,
) (MountHandle, error)
```

`Options` 只接受性能类安全白名单；cache TTL、direct IO 等安全相关选项由挂载实现统一生成，调用方传入冲突配置时直接报错。宿主编排形态下不启用 `AllowOther`（§6.3）。

不能继续丢弃 `*fuse.Server`：`MountHandle` 持有 server、当前 `InstanceView`、inode 索引和取消函数，用于主动撤权、cache invalidation、卸载和健康检查。**其生命周期由 `toolfsd` 管理，与 `agentd` 管理的实例容器一一对应**（§14.2.4）。

原 `MountToolFS` 不带身份即完全放行。为避免安全误用：

- 重命名为 `MountToolFSUnsafe` 并标记 deprecated；
- README 明示仅允许受信任的单用户开发环境；
- 新 CLI 默认只暴露带身份的挂载；
- 不允许带身份的挂载在 permission provider 失败时 fallback 到 unsafe 挂载。

### 6.3 挂载的访问边界

**在 §14.2 选定的宿主编排形态下，实例间的隔离由 bind 的选择性与容器边界达成，而非依靠 FUSE 层的调用者校验。**

宿主上的挂载点归属：

- 每个用户的挂载点由 `toolfsd` 以 root 创建，模式 `0700`，**仅 bind 给该用户的实例容器**；
- 其他实例容器的挂载视图中**根本不存在**该路径，无从访问；
- 真实数据与 `.toolfs-state/` 保护区**不 bind 进任何容器**（§14.4）。

因此 FUSE 挂载**不需要 `AllowOther`**：唯一的消费者是被 bind 进去的那一个容器，其内部进程通过 bind 访问，不涉及跨用户的本机调用。

> **此前设计的 caller UID 校验（`fuse.FromContext` + `ExpectedUID`）在本形态下不再必要。** 它是为「同一宿主上多个本地用户共用挂载」的场景设计的；改为每实例独立容器后，边界前移到了容器与 bind 层面。
>
> 若将来回退到「同容器内多进程」形态（§14.2.3 已排除），则该机制**连同独立 uid、PID namespace、`hidepid`** 一并成为必需——但那时 caller UID 校验本身也不足以防住 `/proc` 穿透（§14.2.1）。

**仍然保留的约束**：挂载点不使用未经校验的用户名构造路径；目录名使用数字 user ID 与不可预测的实例 ID。

### 6.4 动态可见性与 FUSE 缓存

- `Readdir`、`Lookup`、`Open`、`Read`、`Write`、`Setattr` 和所有 mutation handler 都校验同一 session；
- 根目录动态基于当前 `InstanceView` 响应，不在 `OnAdd` 中永久固化 Grant 列表；
- `NamespaceHide` 下，无权路径 `Lookup` 返回 `ENOENT`；已知只读路径的 mutation 返回 `EROFS`；身份或 capability 拒绝返回 `EACCES`；
- 授权敏感文件不返回 `FOPEN_KEEP_CACHE`，默认使用 direct IO/禁用内容缓存；
- entry、attr、negative TTL 在 MVP 设为 0；后续如提升性能放开缓存，失效触发条件有**两个独立来源**，缺一不可：
  - **授权变更** → generation 变化时调用 `NotifyEntry`；
  - **数据变更** → 另一挂载点上的写入需对本挂载点调用 `NotifyEntry`/`NotifyContent`。**此项不由 generation 覆盖**——A 写文件不改变 B 的 Grant generation。实测已暴露的具体破绽是 **negative entry**：A 创建的文件在 B 上持续 `ENOENT` 达整个 TTL（§2.5⑨）。因此放开缓存时 `NegativeTimeout` 须单独保持 0，或由创建方主动对其他挂载点 `NotifyEntry`；
- Grant 撤销、JWT 过期或 session 关闭后，即使 inode/handle 已存在，后续操作也必须重新拒绝；
- open handle 不能固定保存“已授权”布尔值，只保存 session、entry 和受约束 fd。

#### 权限判定缓存（与内核缓存无关）

`git status` 在大仓库上会 stat 数千文件，每次都过 FUSE 与权限判定。需在**应用层**缓存判定结果：

```go
// key: (generation, canonical path, op) → 决策
// 随 AccessSnapshot.Generation 变化整体失效
```

这与上文「禁用 FUSE 内核缓存」**不冲突**：内核缓存的是文件内容与目录项，撤权后可能泄露陈旧数据；此处缓存的是权限决策，且与 generation 绑定，Grant 一变即全部失效。两者性质不同，不可混为一谈。

**核心验收**：用户1 的挂载点只见 A、B，用户2 只见 C；两个挂载点分属不同实例容器，用户2 的容器内不存在通往用户1 挂载点的路径（隔离由 §14.2 的容器边界提供，**不再依赖 caller UID 判定**）；撤销 Grant 后，已有路径和已打开文件在规定 TTL 后均不可继续读取。

### 6.5 共享目录的并发语义

多个用户可能对**同一目录**都持有 rw Grant（share 表允许这种授权）。此时两个挂载点指向同一份真实数据：

```
/mnt/toolfs/<A>/workspace/HDD-Storage/  ┐
                                        ├─► 同一份真实数据 /media/HDD-Storage/
/mnt/toolfs/<B>/workspace/HDD-Storage/  ┘
```

**需要澄清的概念**：挂载点隔离的是**视图与权限**，不是**数据**。ToolFS 是 passthrough，后端就是真实文件系统，因此 A 的写入对 B 天然立即可见——不需要、也不应该让两个挂载点互相可见。

**可见性的来源是后端文件，不是挂载**。A 与 B 是两个独立的 FUSE 挂载、两条独立的 FUSE 连接、不同的 superblock；它们都只是视图，数据在后端文件系统上只有一份，读写在那里汇合。挂载传播（§6.6）管的是挂载事件的跨命名空间传递，不参与数据通路——**无论 `rprivate` 还是 `rslave`，A 与 B 都互相可见**。

因此真正可能破坏跨用户可见性的只有**缓存**。已用最小 FUSE passthrough 在真机实测（详见 §2.5⑨）：

| 配置 | 跨挂载可见性 |
|---|---|
| TTL=0（§6.4 的 MVP 配置） | 写入、创建、删除、mkdir、readdir **全部即时可见，双向** |
| TTL=30s + `FOPEN_KEEP_CACHE` | **negative entry 陈旧恰好 30s**；内容与正向 entry 未陈旧 |

**结论：MVP 配置下跨用户可见性经实测成立，无需额外机制。** 而放开缓存后已实测暴露的破绽是 **negative entry 缓存**——A 创建的文件，B 在整个 TTL 窗口内持续返回 `ENOENT`。

约束因此为：

| 层 | 约束 |
|---|---|
| FUSE **内核**缓存 | MVP 保持 TTL=0 + direct IO。放开缓存的前提是实现**跨挂载点失效**，且 `NegativeTimeout` 必须单独保持 0（见 §6.4） |
| **toolfsd 内部**缓存 | 只允许缓存**权限决策**（与 generation 绑定），**禁止缓存文件内容与 stat 结果** |

> **不得依赖内核的自动失效。** 实测中内容未陈旧，是因为内核 6.18.9 在属性刷新时察觉 mtime 变化而主动作废页缓存（已确认 `KEEP_CACHE` 确实下发：`flags=0x2, direct_io=false`）。这属于内核实现行为而非 FUSE 协议保证，跨内核版本不可移植，不能作为设计依据。

并发的三个层面分别处理：

| 层面 | 处理方式 |
|---|---|
| **数据一致性** | 不干预。底层同一文件系统，POSIX 语义照旧，"谁后写谁赢"是正确行为 |
| **保护链协调** | 需串行化。`versions` 的去抖判断（60s）、`MaxVersionsPerFile` 计数、回收站条目写入，在并发写同一文件时必须互斥 |
| **撤销冲突** | 已由 §7.8 覆盖。A 撤销时若发现 mtime/size 与记录不符（B 期间改过），标记 `UndoSkipped` 并报告，不盲目回退 |

> §7.8 的冲突检测原本给出的理由是「SMB / Docker 等旁路通道可能改过」。**多用户并发访问共享目录是更直接的触发场景**，设计无需改动，但理由应补全。

**版本历史属于文件，不属于用户**：A 写入产生的旧版本，B 同样可以据以恢复；`MaxVersionsPerFile` 的名额由所有写入方共同消耗。这是有意的——按用户切分版本链会导致恢复时看不到完整历史。

**对 toolfsd 进程模型的影响**：保护链协调要求跨挂载点互斥。

| | 单进程 serve 全部挂载 | 每挂载一个 serve 进程 |
|---|---|---|
| 去抖 / 计数协调 | 进程内 mutex，简单可靠 | 需文件锁或 IPC |
| 故障影响面 | 崩溃影响所有挂载 | 相互隔离 |

**取舍：采用单进程 serve 全部挂载。** 保护链的正确性优先于故障隔离；且 toolfsd 崩溃是可恢复的——systemd 拉起后重新挂载，实例容器凭 §6.6 的结构自动跟上新挂载。（若后续压测显示单进程成为瓶颈，可改为多进程 + 基于版本目录 `O_CREAT|O_EXCL` 的文件锁协调，接口不变。）

### 6.6 挂载传播与挂载点结构

agentd 将宿主挂载 bind 进实例容器时，传播模式与挂载点结构共同决定了**挂载生命周期能否与容器生命周期解耦**。

#### 先澄清一个常见误解

**挂载传播传递的是「挂载事件」，不是「文件内容」。** bind mount 创建的是新的 mount 对象，但引用**同一个 superblock**——对 FUSE 而言即同一条 toolfsd 连接。因此容器内外的读写本就作用于同一份数据。

实测（10.0.0.75，Docker 28.3.2）：

| | 宿主↔容器内容互见 | 容器启动后新增的挂载可见 |
|---|---|---|
| `rprivate`（Docker 默认） | ✅ | ❌ |
| `rslave` | ✅ | ✅ |

即：**`rslave` 不是内容可见性的前提**；它解决的是 toolfsd 重挂载后容器能否跟上。

#### 关键实测：挂载点结构决定能否恢复

对比两种结构，容器启动后由宿主执行 `umount` + 重新挂载（模拟 toolfsd 重启）：

| 结构 | bind 源 | 重挂后容器读到 | 卸载后容器 |
|---|---|---|---|
| **A** | FUSE 挂载点**本身** | ❌ 仍是**旧挂载**的数据，且不报错 | 静默陈旧 |
| **B** | 挂载点的**父级普通目录** | ✅ 新挂载的数据 | ✅ 立即 `ENOENT` |

**结构 A 是安全缺陷，不只是可用性问题**：toolfsd 重启会按最新 share 表重建视图（例如管理员刚撤销了某用户对某目录的授权），但容器仍持有旧 FUSE 连接，继续按**旧权限**读写——撤权静默失效且无任何错误信号。`rslave` 在此结构下无法补救，因为被 umount 的是 bind 源自身。

#### 因此规定

```
宿主：
  /mnt/toolfs/<user-id>/              ← 稳定普通目录（bind 源，永不 umount）
  /mnt/toolfs/<user-id>/workspace/    ← FUSE 挂载点（toolfsd 可自由重挂）

agentd 启动实例容器：
  docker run -v /mnt/toolfs/<user-id>:/mnt/toolfs:rslave ...
容器内：
  /mnt/toolfs/workspace/              ← 跟随 FUSE 挂载生命周期
```

- **必须 `rslave`**：单向传播，宿主的重挂载可达容器；
- **禁止 `rshared`**：双向传播会让容器内的 mount 出现在宿主命名空间，等于把挂载能力交还给容器，与 §14.2 的隔离前提直接冲突；
- **无需 `--make-rshared`**：实测 ZimaOS 上新建挂载默认继承 `shared`（`/` 为 shared，dockerd 以 `MountFlags=shared` 运行且与宿主同 mount namespace），`rslave` 开箱可用。

#### 传播不能替代生命周期管理

即便结构 B + `rslave` 让**新的**路径解析正确，容器内**已打开的 fd** 仍绑定在旧 FUSE 连接上，会得到 `ENOTCONN`。因此仍需：

- toolfsd 重启后，agentd 主动通知受影响实例，或直接回收重建；
- app-server 侧对文件操作的 `ENOTCONN` / `ESTALE` 做重试而非崩溃退出。

**传播保证的是「新操作看到正确视图」，不是「进行中的操作无感知」**，两者都要做。

---

## 7. 防误删与数据恢复

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
  → InstanceView + SecurePathBackend 打开受约束 parent/target fd
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
    MaxVersionFileSize         int64          // 见 §7.2.1：按存储能力动态定，不是固定值
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

> **为什么保护副本必须是 reflink/copy 而不能是硬链接**：硬链接共享 inode，agent 原地 `open(O_WRONLY)` 写下去会同时改掉「旧版本」，保护形同虚设。reflink 创建独立 inode、写时才分裂数据块，旧版本安全——这正是 reflink 相对硬链接的关键价值，也是 ext4 给不了的（其块分配元数据没有引用计数结构）。

### 7.2.0 排除规则（不可省略）

agent 由 codex 驱动，会执行 `npm install`、`git` 操作、生成 `__pycache__`。若每个文件写入都触发版本保护：

```
npm install 一个中等项目  →  node_modules 数万文件
每个都存一份版本          →  ext4 上是全量拷贝
/DATA 仅剩 31G           →  一次任务即打满
```

`git` 尤其麻烦：它的工作模式是「写临时文件 + rename 覆盖」（`.git/index.lock` → `.git/index`），每次 `git add` 都走 rename 覆盖分支；而 `.git/` 里的内容 **git 自身已有完整历史**，ToolFS 再存一份既浪费又干扰。

```go
type ExcludePolicy struct {
    NoVersion []string // 命中则不保存版本，但删除仍进回收站
    NoProtect []string // 命中则完全不保护（谨慎使用）
}

// 默认 NoVersion：
//   .git/**            git 自有历史
//   node_modules/**    可重新安装
//   __pycache__/**  *.pyc  *.pyo
//   .venv/**  venv/**
//   target/**  dist/**  build/**  .next/**
//   *.tmp  *.swp  *~  .DS_Store
```

**两件事必须分开配置**：

| | 含义 |
|---|---|
| `NoVersion` | 不为写入保存旧版本；**但 `rm -rf node_modules` 仍进回收站可撤销** |
| `NoProtect` | 完全不保护，删除即真删。默认为空 |

这条规则同时缓解性能：否则 `npm install` 的每个文件写入都要同步等一次全量拷贝完成。

规则按**相对 Grant root 的路径**匹配，在挂载建立时编译；非法 pattern 拒绝建立挂载，匹配失败不得按「不排除」静默处理。

### 7.2.1 分层设计：语义统一、实现适配、能力显式

文件系统的保护能力差异是**物理事实**，不是实现选择。设计上分三层应对，三者缺一不可。

**第一层：语义契约——必须统一。** agent 与用户只面对「这个路径能回到哪些时间点」（§7.4 的 `ListRestorePoints` / `FlashbackPath`），不需要知道底层是 subvolume 快照、reflink 版本还是全量拷贝。授权规则（当前 Grant 判定、写回走保护链、覆盖前存版本）在所有存储上完全一致。

**第二层：能力声明——必须显式暴露，不能假装统一。**

```go
type CostClass uint8
const (
    CostConstant CostClass = iota // reflink / rename，O(1)
    CostLinear                     // 全量拷贝，O(size)
)

type StorageCapabilities struct {
    Trash          bool      // 恒 true：同设备 rename 在所有 fs 上都可用
    Versions       bool      // 恒 true，但代价见 VersionCost
    VersionCost    CostClass // btrfs/XFS(reflink)=Constant；ext4=Linear
    VolumeSnapshot bool      // 仅 btrfs 子卷
    FreeBytes      int64
    FSType         string
}

func (sr *StateRoot) Capabilities() StorageCapabilities
```

能力由 backing filesystem 决定，因此**挂在 per-device 的 `StateRoot` 上**，不引入新的承载单元。探测在 StateRoot 初始化时完成（`statfs` 判 fstype + 实探一次 `FICLONE`），结果缓存。

**第三层：实现适配——同一接口、多个后端。**

```go
// ProtectionRef 是可持久化的恢复凭据，形如 "version:<id>" / "trash:<id>"，
// 同时写入审计（§9 的 Protected 字段）与 change log（§7.8 的 ChangeRecord.Protected）
type ProtectionRef string

type ProtectionBackend interface {
    SaveVersion(ctx context.Context, srcFD int, sr *StateRoot) (ProtectionRef, error)
    MoveToTrash(ctx context.Context, srcFD int, sr *StateRoot) (ProtectionRef, error)
    Open(ctx context.Context, ref ProtectionRef) (fd int, release func(), err error)
}
// reflinkBackend：ioctl(FICLONE)，O(1)
// copyBackend   ：io.Copy，O(size)
// 选择由 StateRoot.Capabilities().VersionCost 决定，调用方无感
```

返回 `ProtectionRef` 而非裸 ID，是为了让**同一个凭据**贯穿审计、闪回（§7.4）与会话级撤销（§7.8）三条路径；`Open` 以 fd 形式交出恢复源，与 descriptor-based 模型一致。

`SnapshotBackend`（§7.5.2）同理：btrfs 有实现，其余返回 `ErrSnapshotUnsupported`。

> **核心原则：统一的是「恢复点」这个抽象，不是「快照」这个实现。**
> `RestorePoint` 与文件系统无关——ext4 上由 versions/trash 两源构成，btrfs 上多一个 snapshot 源，但返回类型与恢复语义完全一致。

**为什么不追求「能力对齐」**：不在 ext4 上用硬链接硬造快照。多访问通路（SMB / Docker / 其他服务）下的原地写入会污染快照，得到的是一个**会静默失效的假快照**。宁可如实声明「这块盘没有时间点快照」。

### 7.2.2 能力差异必须驱动策略，而不只是展示

能力字段若只用于 UI 展示，等于没做。它必须真正改变运行时行为：

| 维度 | btrfs（`CostConstant`） | ext4（`CostLinear`） |
|---|---|---|
| `MaxVersionFileSize` | 可放宽甚至不限（reflink 恒 O(1)） | **按 `FreeBytes` 动态收紧**；实测 `/DATA` 已用 87%、剩 31G，固定 64MB 偏激进 |
| 低剩余空间 | prune 旧快照与旧版本 | **降级为「只保 trash、不保 versions」并告警**，而不是把盘写满 |
| 周期性快照 | 可默认开启 | 不适用（`VolumeSnapshot=false`） |
| 覆盖写保护 | 可对每次写入都保护 | 大文件走 `AllowUnprotectedLargeWrite` 显式决策，默认拒绝而非静默裸写 |

**UI/产品侧必须按存储如实呈现能力**：不能用统一的「文件历史」措辞掩盖 `/DATA` 上只有少量文件版本、而 btrfs 盘上才有完整时间点这一差异，否则用户会基于错误预期丢数据。

### 7.3 元数据、归属与崩溃恢复

`TrashEntry` 至少记录：

- entry ID、原 public path、稳定 filesystem ID、运行时设备号、trash 相对路径；
- UserID、username、session ID；
- 原 mode、mtime、size、类型；
- create/commit 时间、保护原因和源 Grant generation。

保护流程使用 write-ahead journal：

1. 写临时 metadata，`fsync` 文件与 journal 目录；
2. 执行同设备 rename/copy；
3. 原子标记 committed，**并在同一次提交中追加 change log 条目**（§7.8 的 `ChangeRecord`，含 `Protected` 凭据）；
4. 启动时扫描 pending journal 和孤儿 payload，恢复为可见 trash entry 或回滚；change log 与保护副本必须一致恢复，不允许出现「有变更记录但凭据指向的副本不存在」。

change log 与 trash/versions 同处一个 StateRoot（`.toolfs-state/<mount-id>/{trash,versions,journal,changelog}`），因此天然继承 per-device 路由与跨重启的 filesystem ID 稳定性。

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

### 7.4 三层恢复源与闪回

v4 会把旧版本写进 `versions/`（`MaxVersionsPerFile` 默认 10），却没有定义任何列举或取回的 API——版本只写不读，配置项形同虚设。v5 关闭这个缺口，并把三个恢复源统一成用户视角的一条时间线。

| 源 | 覆盖的事件 | 粒度 | 保留策略 | 写入代价 |
|---|---|---|---|---|
| `versions/` | 覆盖写、truncate、rename 覆盖目标 | 单文件 | 10 版本/文件、60s 去抖、64MB 上限 | reflink 或 copy |
| `trash/` | unlink、rmdir、被覆盖的 rename 目标 | 文件或目录 | 时长 + 容量 + 每设备最低剩余空间 | 同设备 rename，O(1) |
| `snapshots/` | 周期或手动的整卷时间点 | 整卷（读取时按路径切片） | 分层 hourly/daily/weekly | btrfs CoW，O(1) |

用户只需要表达「把这个路径恢复到某个时间点」，由 ToolFS 决定从哪一层取。

```go
type RestoreSource uint8
const (
    RestoreFromVersion  RestoreSource = iota // versions/
    RestoreFromTrash                          // trash/
    RestoreFromSnapshot                       // snapshots/
)

type RestorePoint struct {
    Source     RestoreSource
    ID         string    // version ID / trash entry ID / snapshot ID
    Timestamp  time.Time // 该版本对应的数据时间点
    Size       int64
    IsDir      bool
    Truncated  bool      // 因超过 MaxVersionFileSize 而未受保护
}

// 用户级：全部受调用者【当前】Grant 约束
func (fs *ToolFS) ListRestorePoints(ctx context.Context, path string, s *Session) ([]RestorePoint, error)
func (fs *ToolFS) PreviewRestorePoint(ctx context.Context, rp RestorePoint, s *Session) (io.ReadCloser, error)
func (fs *ToolFS) FlashbackPath(ctx context.Context, path string, rp RestorePoint, s *Session) error
```

`ListRestorePoints` 合并三源、按时间倒序返回；`FlashbackPath` 把指定恢复点写回 `path`。

> 本节是**按路径**的恢复。agent 更高频的需求是**按任务整体撤销一串操作**，见 §7.8——它复用同样的 versions/trash 凭据，但入口是会话/任务而非路径。

#### 四条不可妥协的安全规则

**① 授权判定必须用当前 Grant，不能用快照/版本创建时的 Grant。**

历史快照包含所有用户的数据。若按「创建时有权限」判定，就会出现：管理员授权用户 A 访问 `/Documents` → 创建快照 → 管理员撤销授权 → 用户 A 通过旧快照把数据取回来。**撤权将形同虚设**。因此从任何恢复源读取，都要先用当前 `AccessSnapshot` 校验目标路径的 `OpRead`；写回时按 §7.1 矩阵校验 `OpCreate`/`OpModify`/`OpDelete`。

**② 恢复源的浏览同样要过滤。**

`ListRestorePoints` 与快照内的目录列举只返回调用者当前拥有 `OpList`/`OpRead` 的路径。否则「列出快照里有哪些文件」本身就是信息泄露。

**③ 闪回是 mutation，走完整保护链。**

恢复动作本身会覆盖现有数据，必须可再恢复。因此 `FlashbackPath` 与普通写入走同一条流水线：`RequireFresh` 授权复核 → 覆盖前把当前内容存入 `versions/` → write-ahead journal → descriptor-based 写入 → 提交 → 审计。**恢复错了还能再恢复回来。**

**④ 读取快照内容同样是 descriptor-based。**

只读快照是普通目录树，里面可能存在指向外部的软链（快照忠实保留了当时的软链）。不能因为「快照是只读的」就放松路径约束：必须以 snapshot root dirfd 为锚，用 `openat2(RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS)` 读取。

### 7.5 快照后端（btrfs）

> **适用范围警告（§2.5① 实测）**：真机上主存储 `/DATA` 是 **ext4**，快照能力在那里**完全不可用**；只有 btrfs 数据盘（如 `/media/HDD-Storage`）支持。而 `/DATA` 恰恰承载用户主数据（Documents/Downloads/Media 等）。
>
> 因此本节描述的是**按存储可选启用**的增强能力，不是全局保底能力。**误删/误改的保底防线是 versions + trash（§7.2、§7.4），它们在所有存储上都可用**。
>
> 能力差异通过 `StorageCapabilities.VolumeSnapshot`（§7.2.1）显式暴露；**产品说明与 UI 必须按存储如实呈现**，不能用统一措辞掩盖「`/DATA` 无时间点快照」这一事实。

#### 7.5.1 为什么快照与闪回必须分权

btrfs 快照的粒度是 **subvolume**（整个 `/DATA` 或整个存储），而 RBAC 的粒度是 **per-user 路径前缀 Grant**。两者不匹配。

若直接把「整卷回滚」开放给普通用户，用户 A 的一次回滚会覆盖用户 B、C 的全部数据——这是比误删严重得多的越权。v2 曾把 `Flashback(snapshotID)` 设计成「需 admin / OpWrite+OpDelete」，在多用户模型下**不足够**。

v5 的解法是把两者拆成不同权限级别的操作：

| | 快照 Snapshot | 闪回 Flashback |
|---|---|---|
| 性质 | 系统级、整卷、只读时间点 | 用户级、按路径、写回当前树 |
| 谁能做 | 仅 admin + 显式管理能力 | 普通用户，受当前 Grant 约束 |
| 数据流 | live subvolume → 只读快照 | 恢复源 → live（经保护链） |
| 对 live 数据的影响 | 无（只读） | 只改调用者被授权的路径 |

即：**快照只是一个只读的历史数据源；闪回是受 RBAC 约束的「读历史 + 写回当前」**。整卷回滚降级为 admin-only 的灾难恢复操作（§7.5.4）。

#### 7.5.2 接口

```go
type SnapshotID string

type SnapshotTrigger uint8
const (
    TriggerManual SnapshotTrigger = iota
    TriggerPeriodic
    TriggerPreRollback // 整卷回滚前自动创建，保证回滚本身可逆
)

type SnapshotInfo struct {
    ID           SnapshotID
    FilesystemID string    // 跨重启稳定，与 StateRoot 同源
    CreatedAt    time.Time
    Trigger      SnapshotTrigger
    SourcePath   string    // 被快照的 subvolume
    ExclusiveB   int64     // 该快照独占的字节数（btrfs qgroup，尽力而为）
}

type SnapshotBackend interface {
    Name() string
    Probe(ctx context.Context, targetFD int) error       // 不可用时返回具体原因
    Create(ctx context.Context, sr *StateRoot, trigger SnapshotTrigger) (*SnapshotInfo, error)
    OpenRoot(ctx context.Context, id SnapshotID) (rootFD int, release func(), err error)
    List(ctx context.Context, sr *StateRoot) ([]SnapshotInfo, error)
    Delete(ctx context.Context, id SnapshotID) error
}
```

`OpenRoot` 返回 **dirfd 而非路径**，与 v4 的 descriptor-based 模型对齐；`release` 用于引用计数，避免 retention 在有活跃读取时删除快照。

#### 7.5.3 btrfs 落地细节

**探测（`Probe`）** 三种结果，失败时必须给出可操作的原因：

1. `statfs` 判 `BTRFS_SUPER_MAGIC` → 再 `btrfs subvolume show <path>` 判是否 subvolume → 是则可用；
2. 是 btrfs 但目标不是 subvolume → 不可用，提示需管理员转换；
3. 非 btrfs → 不可用，`ErrSnapshotUnsupported`。

**不自动把目录转换为 subvolume。** 自动搬移用户数据风险过高。提供独立运维命令（要求停机 + 已有备份 + 显式确认），不在服务启动路径里做。

**`.toolfs-state` 必须是独立 subvolume——这是根除快照自吞的关键。**

btrfs 快照**不递归进嵌套 subvolume**（嵌套点在快照里表现为空目录）。因此只要 `.toolfs-state` 自己是一个 subvolume，快照就天然不包含 trash、versions 和历史快照。

这比 v2「walker 显式跳过」的方案可靠得多：前者是**文件系统层面的保证**，后者依赖每一处遍历代码都记得排除。StateRoot 初始化时：

- 目标是 btrfs 且 `.toolfs-state` 不存在 → `btrfs subvolume create`；
- 已存在且是普通目录 → **告警并拒绝启用快照能力**，要求先迁移（否则第一次快照就会把历史数据整个吞进去）；
- 非 btrfs → `.toolfs-state` 保持普通目录，快照能力不可用，versions/trash 照常工作。

**存放与命令**：

```
<StateRoot>/snapshots/<snapshot-id>        # 与 trash/ versions/ journal/ 同级
btrfs subvolume snapshot -r <source-subvol> <StateRoot>/snapshots/<id>
```

**删除**：`btrfs subvolume delete`。retention 清理前必须确认没有活跃 `OpenRoot` 引用；采用引用计数 + 延迟删除，不依赖「已打开 fd 仍可读」的隐式语义。

#### 7.5.4 整卷回滚（admin-only，离线）

```go
type RollbackConfirmation struct {
    SnapshotID SnapshotID // 必须与目标一致
    CreatedAt  time.Time  // 二次确认，防误操作
}

func (fs *ToolFS) RollbackVolume(ctx context.Context, id SnapshotID,
    admin *Session, confirm RollbackConfirmation) error
```

前置条件全部满足才执行，任一不满足即拒绝：

- admin session **且**显式开启 `AllowVolumeRollback` capability（默认关闭）；
- **所有 session mount 已卸载**——否则各 session 的视图与已打开的 fd 会指向被换走的旧 inode，产生不一致；
- 回滚前**自动创建一个 `TriggerPreRollback` 快照**，否则回滚本身不可逆；
- 二次确认（快照 ID + 创建时间戳同时匹配）；
- FUSE 服务运行期间一律拒绝。

实现采用子卷换名：`mv live live.rollback-<ts>` → `btrfs subvolume snapshot <snap> live`（可写快照落位）→ 旧卷进入 retention 保留一份。

#### 7.5.5 为什么不做 copy 型降级

v2 曾设计 `CopyBackend` 作为非 btrfs 环境的降级。**v5 去掉它**，理由：

- copy 型「快照」是 O(数据量) 的全量拷贝。在 TB 级 `/DATA` 上既慢又占空间，且没有原子性保证——拷贝过程中数据仍在变化，得到的不是一致时间点；
- 它想解决的核心场景（误删、误改恢复）**已被 versions + trash 完全覆盖**，而那两者是 O(1) 的；
- 保留一个「看起来能用、实际会拖垮系统」的降级实现，比明确不支持更危险。

非 btrfs 环境（含 macOS 开发机）：快照能力返回 `ErrSnapshotUnsupported`，**versions 与 trash 不受影响**（它们只依赖同设备 rename 与 reflink/copy，不依赖 btrfs）。这也把测试矩阵从「两个后端 × 全部用例」简化为「一个后端 + 一个明确的不支持分支」。

### 7.6 自动快照与保留策略

```go
type AutoSnapshotConfig struct {
    Enabled   bool          // 默认 false
    Interval  time.Duration // 建议 1h
    Retention SnapshotRetention
}

type SnapshotRetention struct {
    KeepHourly int   // 默认 24
    KeepDaily  int   // 默认 7
    KeepWeekly int   // 默认 4
    MinFreeBytes int64 // 低于此值：停止创建、触发 prune、发出告警
}
```

- 分层保留（hourly/daily/weekly）比单纯 `MaxCount` 更贴合恢复需求：近期细粒度、远期粗粒度；
- **手动快照与 `TriggerPreRollback` 快照不参与自动 prune**；
- **低空间保护是必须项，不是优化项**：btrfs 快照创建虽是 O(1)，但随着 live 数据变化，快照会持续持有旧 extent，**空间占用随时间单调增长**。必须监控剩余空间，低于阈值时停止创建并主动 prune，否则会把存储写满——这是 btrfs 快照最常见的运维事故。

### 7.7 快照不在 FUSE 中暴露

快照**不**作为挂载点内的可见目录（例如不提供 `/.toolfs-history/`）。理由：需要处理只读子树、跨快照路径映射、逐条目权限过滤等复杂性，而快照浏览是低频操作。MVP 阶段快照只通过 API 访问，保持 FUSE 层简单。

若后续确有需求，再评估以独立虚拟目录形式暴露，并复用 §7.4 的四条安全规则。

### 7.8 会话级撤销（agent 场景的核心恢复能力）

**定位**：ToolFS 的快照/闪回是**独立模块**，不依赖也不对接 ZimaOS 的 backup，它服务的是 **AI agent 的操作安全**。按这个定位重新审视 agent 的恢复需求：

| 优先级 | 场景 | 由谁覆盖 |
|---|---|---|
| 高频 | 改错一个文件 → 回退 | versions（§7.2） |
| 高频 | 删错 → 恢复 | trash（§7.2） |
| **高频** | **一个任务做了一串操作 → 整体撤销** | **本节** |
| 低频 | 回到某个时刻 | 快照（§7.5，仅 btrfs 存储） |

backup 类产品提供的是最后一项，粒度还粗——**恰恰是 agent 最不需要的**。agent 真正的高频需求是「把刚才那个任务做的事整体撤掉」。

典型场景：agent 执行「重构这个目录的配置文件」动了 15 个文件，用户看完说撤销。让 agent 自己逐个 `FlashbackPath` 意味着它得自行记住动过哪些、每个的上一版是什么——状态放在 agent 侧，进程一崩就全丢；整卷回滚粒度又太粗，会波及其他用户和其他任务。

**关键价值：本能力建立在 versions + trash 之上，不依赖 btrfs，因此在 ext4 的 `/DATA` 上完全可用**——正好补上「主存储无快照能力」（§2.5①）的缺口。

> 现有代码已有种子但不可用：`ChangeRecord{Path, Operation, Timestamp, SessionID}`（`toolfs.go:200`）与 `TrackChange`（`toolfs.go:1759`）。三个问题使其无法直接采用——依赖 `currentSnapshot != ""`（无活跃快照时直接 return）、变更挂在 snapshot 上（只能 `GetSnapshotChanges(快照名)`，无法按 session 查询）、纯内存态（重启即丢）。

#### 7.8.1 数据模型与 API

```go
type ChangeRecord struct {
    Seq       uint64    // 会话内单调序号，保证逆序撤销
    // SessionID / TaskID 是【agent 侧】的会话与任务标识，由调用方经 MCP 传入。
    // 它们与 ToolFS 的挂载粒度（per-user）无关——一个用户的实例内会有多个 agent 会话。
    SessionID string
    TaskID    string    // 可选：agent 的任务分组，用于按任务撤销
    Op        Operation
    Path      string    // canonical public path
    Protected string    // "version:<id>" / "trash:<id>" —— 撤销凭据
    Timestamp time.Time
}

type ChangeFilter struct {
    SessionID string     // 必填：agent 侧会话标识；只能过滤本实例内的变更
    TaskID    string     // 可选
    Since     time.Time  // 可选
    MaxSeq    uint64     // 可选：撤销到某个序号为止
}

type UndoOutcome uint8
const (
    UndoDone     UndoOutcome = iota
    UndoSkipped              // 冲突：该路径在此之后被他人修改
    UndoFailed               // 权限、IO 或凭据缺失
)

type UndoReport struct {
    Attempted int
    Results   []struct {
        Change  ChangeRecord
        Outcome UndoOutcome
        Reason  string
    }
}

func (fs *ToolFS) ListChanges(ctx context.Context, f ChangeFilter, s *Session) ([]ChangeRecord, error)
func (fs *ToolFS) UndoChanges(ctx context.Context, f ChangeFilter, s *Session) (UndoReport, error)
```

#### 7.8.2 六条设计约束

**① 保护凭据在 mutation 落盘时同步记录。** 每次 create/modify/delete/rename 走完 §7.1 流水线后，把 `ProtectionBackend` 返回的 `VersionID`/`TrashID` 写进 `ChangeRecord.Protected`。撤销时直接按凭据定位恢复源，**撤销代价是 O(变更数) 而非 O(全盘扫描)**。

**② 必须按 `Seq` 逆序撤销。** 正序会踩坏中间态：若依次发生 `A→B`、`B→C`，正序撤销第一条会把 A 恢复到还被第二条占用的位置。逆序是唯一安全顺序。

**③ 撤销本身也是 mutation，走完整保护链。** 撤销时被覆盖的当前内容同样进 `versions/` 并产生新的 `ChangeRecord`，因此**「撤销的撤销」天然可用**，无需特殊实现。

**④ 持久化到 journal，与 trash/versions 同处一个 StateRoot。** change log 随 §7.3 的 write-ahead journal 一起落盘并参与崩溃恢复。这是相对现有内存态实现的关键改进——agent 进程重启、ToolFS 重启后仍可撤销。

**⑤ 授权：只能撤销自己 session 的变更，且按【当前】Grant 重新判定。** `ChangeFilter.SessionID` 强制为调用者自身；每条撤销在执行前按 §7.1 矩阵重新校验目标路径权限（`RequireFresh`）。这防止「撤权后借历史变更记录写回」——与 §7.4 规则① 同源。

**⑥ 冲突默认跳过并报告，不做全有全无、不强制覆盖。** 撤销前比对目标当前状态与 `ChangeRecord` 记录的变更后状态（mtime + size，必要时内容摘要）：不一致说明此后有他人修改，标记 `UndoSkipped` 并在报告中说明。

> 第 ⑥ 条对 ZimaOS 尤其必要，有两个独立的触发来源：**（a）多通路共享**——数据同时经 SMB / Web UI / Docker / agent 访问；**（b）多用户并发**——多个用户可对同一目录都持有 rw Grant，其挂载点指向同一份数据（§6.5），B 的写入会直接使 A 的撤销记录失效。后者是更高频的场景。agent 变更之后很可能已被其他通路或其他用户改动。静默覆盖会造成他人数据丢失；全有全无则会让一次冲突废掉整个撤销。**部分成功 + 明确报告**是唯一合理的语义。

#### 7.8.3 保留与清理

change log 的保留策略与 `versions`/`trash` 联动：**凭据指向的恢复源被 retention 清理后，对应 `ChangeRecord` 同时失效**，`ListChanges` 需将其标记为不可撤销而非直接隐藏（否则 agent 会以为没做过这些操作）。change log 本身按会话结束后 N 天清理，默认与 `TrashRetention` 对齐。

---

## 8. ZimaOS 凭据的获取与维护

> **适用范围（v7 修订）**：本章原设定为「每个请求验签 JWT 获取身份」。因 agent 与 ZimaOS 账号体系独立（§14.6.1），**该模型不成立**——请求中不携带 ZimaOS token。
>
> 本章内容改为适用于**账号绑定流程**：当采用 §14.6.3 的方案 A 或 B 时，需要获取并维护该 ZimaOS 用户的凭据（用于代表其拉取 Grant、以及凭据续期）。下述关于无 `kid` 的 JWKS、过期与续期、失效降级策略的分析**仍然适用于那个场景**。
>
> 若采用方案 C（管理员映射表），则不涉及凭据获取，本章可跳过。



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
- username/role 如发生变化，以新 token 为准重建 `AccessSnapshot` 和 `InstanceView`；capabilities 不能因 token refresh 自动扩大；
- JWT 中的 `role` 是签发时快照，不是实时撤权状态。因 v5 的文件访问层不再依赖 `role`（§2.4），**文件可见性与读写不受这一问题影响**——它们由 Grant TTL 收窄。但 `role` 仍决定管理操作准入（`PurgeTrash`/快照/回滚），故执行这些操作前必须通过 UserService 当前状态查询做短 TTL（默认 30s）复核，用户禁用或 admin 降级后立即拒绝；
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
    InstanceID      string `json:"instance_id,omitempty"`  // 哪个实例发起
    CallerUID       uint32 `json:"caller_uid,omitempty"`    // 容器内 uid，诊断用
    CallerPID       uint32 `json:"caller_pid,omitempty"`    // 容器内 pid，诊断用
    GrantGeneration uint64 `json:"grant_generation,omitempty"`
    Decision        string `json:"decision,omitempty"`  // 命中的授权规则摘要
    Protected       string `json:"protected,omitempty"` // "trash:<entryID>"，恢复凭据
}

func NewFileAuditLogger(dir string) (*FileAuditLogger, error) // JSONL，按日切分
```

`AccessDenied` 判定改为哨兵错误 `errors.Is(err, ErrAccessDenied)`，替换现有脆弱的字符串 `Contains("access denied")`（`toolfs.go:163`）。

审计策略：

- 授权拒绝、身份解析失败、provider 失败、保护失败与成功 mutation 都必须记录；
- `CallerUID`/`CallerPID` 是**容器内**的值，仅作诊断用途——实例归属由 `InstanceID` 与挂载点确定，不依赖这两个字段做判定（§6.3）；
- 不记录 JWT、文件内容或 JWKS 原文；
- destructive operation 在审计 logger 不可用时默认 fail closed；
- read/list 审计失败可按配置 fail open，但必须输出进程级告警与丢失计数；
- 因授权敏感 FUSE mount 禁用内容缓存，正常文件操作不会被 kernel page cache 完全绕过审计。

---

## 10. 前置条件

| # | 前置条件 | 状态 |
|---|---|---|
| 1 | **descriptor-based P0 路径安全必须先做**（§11.0）；不能以 `EvalSymlinks` containment 代替 | ⚠️ **实现阻塞项** |
| 2 | **宿主可部署 `toolfsd` 与 `agentd`**——宿主 rootfs 为只读 squashfs（§14.1），需借助可写分区或系统扩展 | ⚠️ **落地阻塞项** |
| 3 | **`agentd`（实例容器编排）由谁实现、何时排期**（§14.2.2） | ⚠️ **落地阻塞项**（agent 团队） |
| 4 | IceWhale-Files 提供修复后的逐用户 Grant 接口；现有 `/share` 不作为授权输入 | ⚠️ **联调阻塞项** |
| 5 | IceWhale-Files public-path 规则可复用或固化为版本化 `PublicRootProvider` 契约 | ⚠️ **安全阻塞项** |
| 6 | IceWhale-Files 服务运行，`/run/casaos/icewhale-files.url` 在**宿主**可达 | 依赖 |
| 7 | **agent 账号 → ZimaOS 授权主体的绑定方式**（§14.6）。**注意：不能靠请求携带 ZimaOS token，两套账号体系独立** | ⚠️ **集成阻塞项**（因 `IdentityResolver` 抽象，不阻塞开工） |
| 8 | 若绑定采用方案 A/B，需明确 ZimaOS 凭据的续期主体（§8） | ❓ **待确认** |
| 9 | 每个 backing device 可创建 reserved StateRoot，并同步配置 Files/Samba 排除 | ⚠️ **受保护写入阻塞项** |
| 10 | ~~真机确认 `/DATA` 是否为 btrfs subvolume~~ → **已确认为 ext4**，快照能力在主存储不可用（§2.5①）。需产品确认可接受 | ✅ 已查明 → ❓ **产品决策** |
| 11 | `.toolfs-state` 独立 subvolume → btrfs 数据盘已验证可行；ext4 上退化为普通目录 | ✅ 已查明 |

**已消除的前置条件**（宿主编排形态带来的简化）：

- ~~容器需具备 `CAP_SYS_ADMIN` 与 `/dev/fuse`~~ → **实例容器零特权**，挂载在宿主完成后 bind 进去（§14.2.2）；
- ~~Codex 在无 Landlock 环境下的沙箱行为~~ → codex 的沙箱策略本就是「委托外部」（§14.1），边界由容器提供，不依赖内核 LSM；
- 挂载传播（`rslave`）配置与时序 → **仍需处理**，但已收敛为固定规则：bind 源取挂载点父目录 + `rslave`（§6.6）。早先「普通 bind 即可」的判断已被实测推翻。

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
4. 引入 `PublicRootProvider` 作为**所有身份共同的**硬上界；不为任何角色开设绕过分支。
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
| **P1**（~2–3d） | 逐行 Grant 接入、`PermissionProvider`、不可变 `AccessSnapshot`、别名/冲突规则、`InstanceView` | 新增 `rbac.go`、`permission_provider.go`、`namespace.go` | 已删除/missing Grant 不返回；零 Grant deny all（含 admin）；无角色旁路 |
| **P2**（~4–5d） | **挂载编排接口**：`toolfsd` 按用户建挂载并返回路径供 `agentd` bind；`IdentityResolver`（过渡期为固定实现）；挂载生命周期与回收 | 新增 `cmd/toolfsd/`、`mountset.go`、`identity.go` | 用户 A/B 各自实例只见自身授权目录；实例间挂载与 `CODEX_HOME` 互不可见；实例退出即卸载；崩溃不留悬挂 |
| **P3**（~4–6d） | 受保护 mutation：trash/versions/journal、**排除规则**、审计落盘、reserved-path 排除 | 新增 `destructive.go`、`trash.go`、`state_root.go`、`exclude.go`、`audit_file.go` | 删除→恢复；覆盖→版本；`npm install` 不产生版本洪水；崩溃恢复；无 StateRoot 时 fail closed |
| **P4**（~3–4d） | 闪回 + 会话级撤销、`.toolfs/` 只读视图、`toolfs-mcp` | 新增 `restore.go`、`changelog.go`、`mcp/` | 按 task 逆序撤销、冲突跳过并报告、重启后仍可撤销 |

**合计约 16–22 人天**，另需 **3–4 人天**真机联调（宿主挂载 + bind 进实例容器、跨实例隔离验证、git/npm 压力、故障注入）。

> **本表仅为 ToolFS 侧工期。** `agentd` 的实例编排与 Web 服务的调度改造（§14.5）在 agent 团队一侧，需并行排期。

### 11.2 btrfs 快照（可选增强，可独立排期）

| 阶段 | 内容 | 说明 |
|---|---|---|
| **S1**（~2–3d） | btrfs backend、`.toolfs-state` 独立 subvolume、`OpenRoot` 引用计数 | **仅 btrfs 数据盘生效**；`/DATA` 是 ext4，主数据无此能力 |
| **S2**（~1–2d） | 自动快照、分层 retention、低空间保护 | 依赖 S1 |
| **S3**（~1–2d） | 整卷回滚（admin + 离线 + 二次确认 + 前置快照） | 依赖 S1 |

### 11.3 排期判断

- **P0 是硬前置**：路径穿越、软链逃逸、只读绕过、并发竞态（§11.0 的 S1–S6）不修，其上的一切保护都建立在可绕过的基础上。
- **P2 是本方案的核心**：per-user 挂载点 + 独立实例容器同时解决了身份传递与隔离，是「完整 RBAC 成立」的关键。注意 Web 服务侧的实例池改造（40 处调用点）**不在 ToolFS 工期内**，需 agent 团队并行推进（§14.5）。
- **P3 的排除规则不可省略**：codex 会驱动 `npm install`、`git`，无排除规则时版本保护会在一次任务内打满仅剩 31G 的 `/DATA`。
- **btrfs 快照优先级最低**：只覆盖数据盘、对应 agent 最低频的需求，可暂缓。
- **交付范围是 P0–P4 一次做完**（§1.1）：完整 RBAC + 防误删保护同批交付，不再拆期。btrfs 快照（§11.2）是**范围之外的可选增强**，不属于「二期」。

> **关于 8 月第二周节点的风险提示（非交付计划）**：P0–P4 合计 16–22 人天，若该节点不可移动，则存在缺口。此时**取舍是产品决策，不是本方案的既定安排**——可选项包括推迟节点、增加人力，或临时只验收 P0–P2 的「用户1/用户2 看到不同挂载目录」（此时挂载须保持只读或仅允许不覆盖已有数据的创建，因为防误删保护尚未就位）。**方案本身不预设降级路径。**

---

## 12. 风险与取舍

1. **当前 FUSE 完全绕过 session**：在 P2 落地前，现有 `MountToolFS` 必须明确为 unsafe/development API，不能用于多用户环境。
2. **上游 Grant 契约是新增依赖**：不先修 IceWhale-Files 就无法安全消费授权。好处是把修复放在事实源处，避免每个消费者重复猜测 `/share` UI 模型。
3. **root daemon 爆炸半径大**：descriptor root fd 与 PublicRoots 是核心隔离边界；任何回退到绝对路径 `os.*` 的代码都应被 code review/静态检查禁止。
4. **撤权存在 TTL**：事件落地前最长约 30s；mutation 强制 fresh，read 依靠短 TTL。若产品要求即时撤权，必须由 IceWhale-Files 发布 share generation 事件。
5. **FUSE kernel cache 会绕过应用层**：MVP 牺牲部分性能关闭缓存。之后开启需同时满足两个条件——授权侧的 generation→inode invalidation 映射，**以及跨挂载点的数据失效**。后者不由 generation 覆盖：实测已暴露 negative entry 缓存会让 A 新建的文件在 B 上持续 `ENOENT` 达一整个 TTL（§2.5⑨）。
6. **StateRoot 与 SMB/Files 共用磁盘**：`.toolfs-state` 必须同时从 ToolFS、Samba、Files API 和旧 snapshot walker 排除，否则可能泄露保护副本或递归自吞。
7. **空间与磁盘故障**：trash rename 不释放空间，versions 会增加写放大；retention、配额、低空间阈值和 fail-closed 必须默认开启。
8. **外部写入不受保护**：SMB、SSH 或其他 root 进程直接修改原文件不会经过 ToolFS 版本钩子。本方案只保证经 ToolFS 发起的 agent 操作。
9. **macOS 安全能力较弱**：macFUSE + `openat` fallback 用于开发；生产验收以 Linux `openat2` 为准。
10. **命令执行与文件访问是两条边界**：`DangerousCommandFilter` 仍需从 contains 改为精确命令解析，但不能把它包装成 FUSE 的 `OpExecute`。
11. **依赖与通用性**：`golang-jwt/jwt/v5` 等为纯 Go；`ZimaOS-Common` 会增加耦合。对外通过 `PublicRootProvider`、`PermissionProvider` 和 matcher 接口隔离 ZimaOS 实现。
12. **角色信息可能过时**：若绑定流程保存了 ZimaOS 凭据，其中的 `role` 是签发时快照。因文件访问层不依赖 `role`（§2.4），可见性与读写由 Grant TTL 收窄，**影响面有限**；但管理操作（PurgeTrash/快照/回滚）仍需按 §8 做当前状态复核。
13. **快照会随时间吃掉磁盘**：btrfs 快照创建是 O(1)，但会持续持有 live 数据变化前的旧 extent，占用**单调增长**。分层 retention、`MinFreeBytes` 阈值、低空间自动 prune 与告警必须默认开启，否则会把存储写满。这是 btrfs 快照最常见的运维事故。
14. **`.toolfs-state` 必须是独立 subvolume**：否则第一次快照就会把 trash、versions 和历史快照整个吞进去。已按普通目录部署的环境需要迁移步骤，且在迁移完成前不能启用快照能力。
15. **主存储没有快照能力（实测确认，§2.5①）**：`/DATA` 是 ext4，btrfs 只在数据盘上。用户主数据（Documents/Downloads/Media）**没有时间点快照**，只有 versions + trash 兜底。这是能力上界，不是实现选择——除非重装系统换文件系统，否则无法改变。产品与 UI 必须如实呈现，不能让用户误以为全部数据可回到任意时间点。
16. **整卷回滚在多用户环境是核弹**：一次回滚覆盖所有用户的数据。故限定 admin + 显式 capability + 全部 mount 卸载 + 二次确认 + 自动 pre-rollback 快照，五个条件缺一不可。
17. **快照包含全部用户数据**：读取必须过**当前** Grant 而非快照时的 Grant，否则撤权后仍可通过旧快照取回数据，撤权形同虚设（§7.4 规则①）。
18. **ext4 上 versions 是全量拷贝（实测确认，§2.5②）**：`/DATA` 不支持 reflink，每次覆盖写保护都要完整复制一份旧内容，而该盘已用 87%、仅剩 31G。`MaxVersionFileSize` 必须按存储剩余空间动态决定，固定 64MB 在此处偏激进；低空间时应降级为「只保 trash、不保 versions」并告警，而不是把盘写满。
19. **实例隔离不能依赖 mount namespace 单独达成**：`/proc/<pid>/root` 可穿透 mount namespace，实测同 uid 进程间可完整读取对方的根目录、cwd 与 fd。**选定的宿主编排 + 独立容器方案已从源头消除该问题**（不同容器不共享 PID namespace）；此处保留记录，是为了防止后续因启动延迟而回退到「同容器多进程」形态时重蹈覆辙——那时「独立 uid + PID namespace + `hidepid`」缺一不可，且失效时无任何迹象（§14.2.1、§14.2.3）。
20. **`toolfsd` 是单点（§6.5 的取舍代价）**：单进程 serve 全部挂载换来了保护链协调的简单可靠，代价是它崩溃会**同时**中断所有用户的挂载——已打开的 fd 得到 `ENOTCONN`。缓解：systemd 自动拉起 + `rslave` 让重挂对容器可见（§6.6）+ agentd 感知后回收实例。若运维上无法接受该影响面，可改为每挂载一个 serve 进程 + 基于版本目录 `O_CREAT|O_EXCL` 的文件锁协调，接口不变。
21. **挂载点结构是安全约束，不是部署细节**：若误将 bind 源设为 FUSE 挂载点本身，toolfsd 重启后容器会**静默**沿用旧视图，撤销的授权继续生效且无任何错误信号（实测，§6.6）。这是一个部署配置就能引入的安全回归，须在 agentd 的启动参数中固化并纳入验收（§13.16）。
22. **ToolFS 在子目录只读上比 SMB 严格（实测确认，§2.5⑤）**：SMB 把每条 share 渲染成独立顶层共享，子目录的 `read_only` 可被父目录的 rw 共享绕过；ToolFS 走最长前缀匹配，只读生效。方向安全，但同一份授权数据下两条通路行为不同，需在交付说明中写清，避免被当作 bug。

---

## 13. 验证方式

1. **安全路径 backend**
   - `../`、重复 slash、反斜杠、NUL、绝对路径重置全部拒绝；
   - 软链指向 Grant 外、父目录并发 symlink swap、目标不存在时替换最近存在祖先均失败；
   - `/toolfs/data` 不命中 `/toolfs/database`；
   - 任何身份（含 admin）访问 `/etc`、`/root/.ssh` 等 PublicRoots 外路径均失败；
   - read/write/create/unlink/rename 的系统调用都以受约束 dirfd 发起。
2. **Grant API 与缓存**
   - 构造同一路径一条 active、一条 deleted 的混合数据，deleted 用户绝不产生 Grant；
   - missing storage、未知用户、空列表、畸形 JSON、超大响应、timeout、redirect 全部 fail closed；
   - `/DATA` 与 `/media/ZimaOS-HD` 归一为同一路径；同路径 RO/RW 冲突按 RO；
   - 并发 cached/fresh/invalidate 不发布旧 generation，不产生 data race。
3. **实例间隔离（必测）**
   - 用户 B 的实例容器内**不存在**用户 A 的挂载路径（未被 bind），访问返回 `ENOENT`；
   - 实例容器内**不存在**真实数据路径与 `.toolfs-state/` 保护区（§14.4）；
   - 实例容器**无任何内核特权**：`mount` 被拒、块设备不可见；
   - 跨实例无法枚举彼此进程、无法互发信号（容器边界）；
   - **若回退到同容器多进程形态**（§14.2.3 已排除），须额外断言：跨实例 `/proc/<pid>/root`、`cwd`、`fd/` 访问全部失败；A 与 B 的 uid 不同；`hidepid` 已生效。**该组用例失效时无任何报错迹象，必须显式断言**。
4. **InstanceView**
   - 同名 share、嵌套 Grant、父 RW/子 RO、多个 storage 都得到稳定且无冲突的虚拟名；
   - 零 Grant 用户看到空根目录；
   - **admin 视图 = 他在 share 表里的行**，与同数据下的 SMB `casa.<admin>.conf` 完全一致；把 admin 的 share 行删光后，其挂载点为空目录（证明无特权旁路）；
   - RBAC MVP 不出现 `memory`、`rag`、skill mounts；`.toolfs/` 作为唯一虚拟条目出现且只读（§14.9）。
   - `.toolfs/` 视图：写入返回 `EROFS`；条目按当前 Grant 过滤，撤权后对应恢复点即刻消失；真实数据中存在同名 `.toolfs` 目录时虚拟视图优先并告警；`restore/` 的 `Readdir` 只列本 session 变更过的路径，不枚举全部 Grant 内容；内容不被 kernel 缓存（撤权后不返回陈旧结果）。
5. **FUSE 端到端**
   - ZimaOS 建两个子用户并授权不同目录，各自 mount 只见自身 entry；
   - 用户2的实例容器内**不存在**用户1的挂载路径（未被 bind），访问返回 `ENOENT`；
   - 撤销 Grant 后验证 `Readdir`、cached path、已有 inode 和已打开文件均在 TTL/事件后拒绝；
   - JWT 过期、session close、daemon restart 均能安全卸载或恢复，不遗留可访问挂载；
   - `MountHandle.Close` 幂等，无 fd/inode/goroutine 泄漏。
6. **ZimaOS 凭据（仅绑定方案 A/B 适用，见 §8）**
   - 覆盖现有“单 key、无 kid”JWKS；
   - 覆盖未来多 key + kid、无 kid 多 key拒绝、正常验签、过期、错 issuer、alg 混淆、非 P-256、签名失败换钥重拉；
   - 新旧 UserID 不同的 refresh 被拒，同 UserID role 变化会重建视图；
   - 有效期内 admin token 对应用户被降级/禁用后，**管理操作**（PurgeTrash/快照/回滚）在状态事件或短 TTL 到达即拒绝；文件读写不受影响（本就不看 `role`）；
   - JWKS 宕机时默认 fail closed，显式 stale 模式受最大时长限制。
7. **mutation 语义**
   - 使用真实命令覆盖 `touch`、`mkdir`、append、`cp`、`mv`、编辑器 temp+rename、`truncate`、`rm`、`rm -r`；
   - rename 分别覆盖同 Grant、跨 Grant、覆盖目标、跨设备；
   - 并发 write/unlink、两个 rename 互换目标、旧 writable handle 写已删除文件均不会破坏保护副本或形成死锁；
   - 任一权限或 fresh check 不满足时，原文件与保护区均不发生部分修改。
8. **回收站与版本**
   - 内置盘、外接盘、Grant 内子挂载分别落入对应 `st_dev` StateRoot；
   - 重启后 `st_dev` 变化时仍通过稳定 filesystem ID 找回原 journal/trash，不串到其他磁盘；
   - 删除→恢复、覆盖→版本恢复、同名恢复冲突、跨用户猜 entry ID、retention purge；
   - 在 journal 各阶段模拟 kill、断电、磁盘满和只读 remount，重启扫描不丢 payload、不误恢复；
   - `.toolfs-state` 在 ToolFS、Files API、SMB 和旧 snapshot 中均不可见。
9. **存储能力分层（§7.2.1/7.2.2）**
   - 同一套 `ListRestorePoints`/`FlashbackPath` 在 ext4 与 btrfs 上返回同构结果，调用方无需分支；
   - `Capabilities()` 在 ext4 上报 `VersionCost=CostLinear`、`VolumeSnapshot=false`，btrfs 上报 `CostConstant`、`true`；探测结果与实际 `FICLONE` 行为一致；
   - **能力真正驱动策略**：ext4 + 低剩余空间时降级为「只保 trash 不保 versions」并告警，而非写满磁盘；btrfs 上 `MaxVersionFileSize` 放宽后仍为 O(1)；
   - `ProtectionBackend` 在两种后端下产出的 version/trash 元数据格式一致，恢复路径不分叉；
   - 注入 mock backend 可在 macOS 上覆盖 `CostLinear` 分支的全部逻辑。
10. **闪回与恢复源**
   - `ListRestorePoints` 正确合并 versions/trash/snapshot 三源并按时间倒序；同一时间点存在多源时选择规则确定；
   - **撤权后不能通过旧快照或旧版本读回数据**（用当前 Grant 判定，§7.4 规则①）；
   - 用户 A 无法 `ListRestorePoints` 或闪回用户 B 的路径；快照内目录列举按当前 `OpList` 过滤；
   - 闪回覆盖已有文件时，被覆盖的当前内容进入 `versions/`，可再次恢复回来；
   - 闪回过程中断（kill、磁盘满、只读 remount）后，原文件与恢复源均不出现部分写入；
   - 快照内的软链指向 Grant 外时，读取失败（snapshot root dirfd + `RESOLVE_BENEATH`）。
11. **会话级撤销（§7.8）**
   - 一个 task 内 15 个混合操作（create/modify/delete/rename）能被完整逆序撤销，最终状态与任务开始前一致；
   - **正序撤销必须被证明会出错**（构造 `A→B`、`B→C` 的 rename 链），逆序正确；
   - 撤销后再撤销（撤销的撤销）能回到撤销前状态；
   - 撤销中途 kill / 磁盘满 / 只读 remount，重启后 change log 与保护副本一致，可继续撤销，不出现「有记录无副本」；
   - 目标文件在 agent 变更后被 SMB 改过 → 该项 `UndoSkipped` 并在报告中说明，其余项照常完成（**部分成功**，非全有全无）；
   - 用户 A 无法用 `ChangeFilter` 撤销用户 B 或其他 session 的变更；
   - 撤权后即使 change log 仍在，撤销因当前 Grant 判定失败而被拒；
   - retention 清理掉凭据指向的副本后，`ListChanges` 将该条标记为不可撤销而非隐藏。
12. **快照后端**
   - `.toolfs-state` 为独立 subvolume 时，快照内该路径为空目录，不含 trash/versions/历史快照；
   - `.toolfs-state` 为普通目录时，启用快照被拒绝并给出迁移提示；
   - 目标非 subvolume、非 btrfs 时分别返回可区分的错误；两种情况下 versions/trash 均正常工作；
   - retention 分层（hourly/daily/weekly）生效；手动与 pre-rollback 快照不被自动 prune；
   - 低于 `MinFreeBytes` 时停止创建、触发 prune、发出告警；
   - `OpenRoot` 引用计数期间 retention 不删除该快照；`release` 后可正常删除。
13. **整卷回滚**
   - 存在活跃 session mount 时拒绝；未开启 `AllowVolumeRollback` 时拒绝；确认信息不匹配时拒绝；
   - 回滚前自动生成 `TriggerPreRollback` 快照，且该快照可用于把系统再回滚回去；
   - 非 admin session 一律拒绝。
14. **一致性不变量**
   - 对同一 `(identity, operation, public path)` 做 ToolFS 与 IceWhale-Files 交叉验证；
   - 要求 `ToolFS allow ⇒ Files 基础授权 allow`，反向不要求成立，因为 ToolFS 的 capability、deny pattern 和保护策略可以更严格。
15. **跨挂载可见性与挂载传播（§6.5/§6.6）**
   - 两个用户对同一目录均有 rw Grant 时，A 的写入/创建/删除在 B 的挂载点上即时可见，反向亦然；
   - 并发写同一文件时，versions 去抖与 `MaxVersionsPerFile` 计数不超发、不漏记（跨挂载互斥生效）；
   - A 撤销变更时若 B 已改动该文件，该项 `UndoSkipped` 且其余项照常完成；
   - **挂载点结构**：断言 bind 源为普通目录、FUSE 挂在其下一层；`toolfsd` 重启并重挂后，容器内读到的是**新视图**（构造「重启前后授权不同」的用例，验证撤权确实生效）；
   - **传播模式**：断言容器 bind 为 `rslave`；显式断言**不是** `rshared`（容器内 mount 不得出现在宿主）；
   - toolfsd 重启期间容器内已打开 fd 得到 `ENOTCONN` 而非静默陈旧读；agentd 能感知并回收实例。
16. **质量门禁**
   - `go test ./...`、`go test -race ./...`、`go vet ./...`；
   - Linux 真实 FUSE 集成测试纳入 CI 或专用 runner；
   - benchmark 记录禁用 cache 后的 read/list/open 基线，性能优化不得破坏上述安全用例。

---

## 14. 部署与集成

**目标 agent**：Zima Agent —— 基于 Codex 二开，镜像 `icewhaletech/codex:latest`。本章依据对 `IceWhaleTech/codex` 源码与运行态（10.0.0.75）的实际调查编写。

### 14.1 Agent 的真实运行形态（源码 + 实测）

它是**常驻 Web 服务 + 常驻 app-server**，不是命令行按任务调用：

```
容器 entrypoint（root）
  ├─ setpriv --reuid=seluser              ← 降权，后续全部以 seluser 运行
  ├─ codex app-server --listen ws://127.0.0.1:4222  &   ← Rust 核心，常驻单实例
  ├─ feedback_server.ts                              &
  └─ exec server.ts                                       ← Web 服务，端口 8787
```

Web 服务经 JSON-RPC over WebSocket 调用 app-server：

```ts
// sdk/typescript/samples/web-streaming/server/sessions.ts:680
appServer.request("thread/start", {
  cwd: sessionProjectCwd(session) ?? vaultRegistry.sessionRootPath(session.id, session.userId),
  permissions: permissionsProfileForConversationMode(session.conversationMode),
})
```

四点关键事实：

1. **app-server 内嵌 codex core**（`app-server/src/in_process.rs`），thread 是**进程内对象**而非子进程——因此没有可被 `toolfs run` 包装的 per-task 进程边界。
2. **实际执行身份是 `seluser`，非 root**。实测 `CapEff: 0000000000000000`、`mount` 被拒、块设备读取 `Permission denied`。容器虽为 privileged，但该配置**未传导到 agent 进程**。
3. **codex 的沙箱策略是 `externalSandbox`**（`WEB_STREAMING_EXTERNAL_SANDBOX`）——它明确把沙箱责任委托给外部，自身不做 Landlock。这与 ZimaOS 内核未编译 Landlock 恰好相容，也正是 ToolFS 应当填入的位置。
4. **agent 与 ZimaOS 是两套完全独立的账号体系**（§14.6.1）。agent web 有自己的登录，请求中**不携带 ZimaOS token**；`DEFAULT_USER_ID = process.env.WEB_STREAMING_USER_ID ?? "local-user"`，与 `casaos`/`subuser` 无任何映射。**接入 ZimaOS 网关也不解决此问题**——网关（Caddy）仅做反向代理，鉴权由各应用自行实现。

5. **agent 具备无头浏览器能力**：Chromium + chromedriver 已装，按需启动，由 `openrt`（独立进程，监听 9731）管理，经 `open_browser_instance` / `close_browser_instance` 两个动态工具暴露给 agent。**每 thread 一个实例，profile 与登录态持久化**。镜像本身是 Selenium Node 底子（`seluser`、4442/4443/4444 端口、`SE_*` 变量、`/opt/selenium/selenium-server.jar`）。资源实测见 §14.10。

> **修正此前判断**：曾因「容器 privileged」推断 agent 可挂载宿主磁盘绕过 ToolFS——**该结论不成立**。agent 以 `seluser` 运行且零 capability（实测 `CapEff: 0`、`mount` 被拒、块设备读取被拒）。
>
> 在 §14.2 选定的宿主编排形态下，这一点进一步强化：**实例容器无需任何特权**，真实数据与保护区**根本不 bind 进容器**，因此不存在可绕过的路径。

### 14.2 集成方案：app-server 多实例，每用户一个

单实例服务全部用户时，ToolFS 在 FUSE 层只见同一个进程，无法区分请求归属。**改为每用户一个 app-server 实例后，每个实例运行在一个绑定了特定用户授权的挂载视图内**——这是 per-user RBAC 成立的关键。

> **准确表述**：承载身份的是**挂载点**（它绑定了某用户的 Grant，决定可见与可写范围）与其所在的 **mount namespace**（限定可见范围）。app-server 实例**对身份完全无感**，它只是看到一个文件系统；约束来自它被放置于哪个视图，而非它自身知道什么。这一点对实现很重要：**不要在 app-server 侧做任何身份相关的处理**。

#### 14.2.1 关键前提：mount namespace 单独使用**不足以**隔离

**实例之间必须做到互不可见，否则 per-user RBAC 名存实亡。** 而仅靠 mount namespace 达不到这一点——`/proc` 提供了一条绕过 namespace 边界的旁路：

```
实例 A 执行：  ls  /proc/<B的pid>/root/mnt/toolfs/Documents/
              cat /proc/<B的pid>/root/mnt/toolfs/Documents/secret.txt
```

`/proc/<pid>/root` 是指向该进程根目录的符号链接，**跟随它会穿入对方的 mount namespace 视图**。访问条件是对目标进程具备 `PTRACE_MODE_READ` 权限——**同 uid 即满足**。`/proc/<pid>/cwd` 与 `/proc/<pid>/fd/` 同理。

**实测确认**（10.0.0.75，容器内）：同为 `seluser` 的两个进程之间，可完整读取对方的 `/proc/<pid>/root/`、`cwd`、`fd/`。

因此若所有实例都以 `seluser` 运行且共享 PID namespace，**隔离效果退化为统一挂载**，只是需要绕一下而已。

**结论：独立 uid 与 PID namespace 不是可选增强，而是隔离成立的必要条件。**

#### 14.2.2 实例形态：宿主编排的独立容器（选定方案）

**每用户一个独立容器，由宿主侧组件编排；挂载在宿主完成后 bind 进容器。**

```
agentd（宿主，root）                    ← 实例编排
   │  「用户 A 需要一个实例」
   ▼
toolfsd（宿主，root）                   ← 只管文件系统
   ① IdentityResolver：agent 账号 → ZimaOS 授权主体（§14.6）
   ② 拉取该主体的逐行 Grant
   ③ 在宿主建立该用户的 ToolFS 挂载
   ④ 返回挂载点路径
   │
   ▼
agentd 拉起实例容器（复用 icewhaletech/codex 镜像，工具链自带）
   -v <该用户的挂载点>:/workspace
   -e CODEX_HOME=/data/homes/<uid>
   codex app-server --listen 0.0.0.0:<port>
   │
   ▼
Web 服务（agent）                       ← 只做调度
   维护 user → 实例地址 的映射，按用户转发 RPC
```

##### 挂载传播：仍然需要，但原因与直觉不同

> **本节结论已被实测修正。** 早先版本断言「挂载先于容器建立，故普通 bind 即可，不需要 `rslave`」——该判断只考虑了**启动时序**，忽略了 **toolfsd 重启**这一常态事件。

挂载确实先于容器建立，因此**首次**可见性没有问题。但实例容器是长期运行的（懒启动、空闲回收），其生命周期会跨越 toolfsd 的重启。实测表明，若 bind 源就是 FUSE 挂载点本身，toolfsd 重挂后容器会**静默读到旧挂载的数据**，导致撤权失效（详见 §6.6）。

因此规定：

- bind 源为 `/mnt/toolfs/<user-id>/`（稳定普通目录），FUSE 挂在其下的 `workspace/`；
- bind 传播模式为 **`rslave`**，禁止 `rshared`；
- 宿主无需 `--make-rshared`（ZimaOS 上新建挂载默认继承 `shared`，已实测）。

`rslave` 保证「新操作看到正确视图」；已打开 fd 的 `ENOTCONN` 仍需 agentd 侧的实例回收与 app-server 侧的重试兜底。

##### 职责边界

建议 **`agentd` 与 `toolfsd` 分离**，而非由 toolfsd 兼管容器编排：

| 组件 | 职责 | 不需要知道 |
|---|---|---|
| **`toolfsd`** | 身份解析、拉取 Grant、建立/销毁挂载、保护链、审计 | Docker |
| **`agentd`** | 实例容器的拉起、回收、健康检查、资源限额 | FUSE |
| **Web 服务** | 按用户调度 RPC 至对应实例 | 挂载与权限 |
| **app-server 实例** | 执行任务 | **身份**（对其完全无感） |

两个特权互不耦合，且该边界正好对应团队边界：`toolfsd` 属 ToolFS，`agentd` 属 agent 侧。**ToolFS 的交付物因此收敛为一个清晰接口——「给定用户身份，返回一个受该用户授权约束的挂载点」。**

##### 安全收益：容器不再需要任何特权

| | 当前 | 本方案 |
|---|---|---|
| agent 主容器 | `privileged: true` | **无需任何特权**（纯调度服务） |
| 实例容器 | —— | **无需任何特权**（只拿到一个 bind 进来的目录） |
| Docker API | —— | 仅宿主的 `agentd` 使用，**不暴露给任何容器** |
| `/proc` 穿透（§14.2.1） | 存在 | **天然消除**——不同容器不共享 PID namespace |

隔离由 runtime 默认保证，而非依靠多项配置拼装。

##### 代价

- **启动约 7 s**（vs 同容器进程形态的 422 ms）。若交互体验不容许，可对活跃用户预热，或让实例常驻至空闲回收；
- **宿主 rootfs 为只读 squashfs**（§14.1），部署 `toolfsd` / `agentd` 需借助可写分区或系统扩展；
- 每实例一个容器，需配置资源限额与并发上限（§14.10）。

#### 14.2.3 被排除的两条路径

| 路径 | 排除理由 |
|---|---|
| **容器内启动子容器** | 需把 `/var/run/docker.sock` 挂进 agent 容器，等于赋予其创建任意特权容器、挂载宿主任意路径的能力——**比本方案要解决的问题严重得多** |
| **同容器内多进程 + 加固** | 启动快（422 ms），但隔离需自行拼齐「独立 uid + PID namespace + `hidepid`」，**任一项遗漏即静默退化为统一挂载**（§14.2.1 实测）。隔离靠实现纪律而非 runtime 保证，失效时无任何迹象 |

> 若后续因启动延迟而重新考虑进程形态，§14.2.1 的三项加固**缺一不可**，且必须把「跨实例 `/proc` 穿透失败」列为必测项（§13.3）。

#### 14.2.4 实例创建流程

```
① agentd 收到「用户 A 需要实例」
② agentd → toolfsd：请求该用户的挂载点
     toolfsd: IdentityResolver → 拉 Grant → 建挂载 → 返回路径
③ agentd 拉起实例容器，bind 挂载点，设置独立 CODEX_HOME
④ agentd 返回实例地址给 Web 服务
⑤ Web 服务按用户转发后续 RPC
⑥ 空闲超时 → agentd 停止容器 → 通知 toolfsd 卸载挂载
```

每个实例拥有：**独立授权范围 · 独立挂载点 · 独立 `CODEX_HOME` · 独立容器（含 mount / PID / network namespace）**。

#### 14.2.5 为什么挂载必须由宿主的 toolfsd 建立

一个自然的替代是让 `agentd` 直接挂载、或由实例容器自行挂载。**均不成立**：

1. **实例容器不应具备挂载能力。** 让它自行挂载 FUSE 需要 `CAP_SYS_ADMIN` 与 `/dev/fuse`——那正是本方案要消除的特权。挂载在宿主完成后 bind 进去，容器**零特权**即可工作。
2. **保护数据必须在实例触及范围之外。** `.toolfs-state/` 为宿主上的 root-owned `0700`（§7.2），且**不 bind 进容器**。否则链条是：agent 误删 → 进回收站 → agent 接着清空回收站 → 保护失效。prompt injection 场景下更直接。
3. **审计不能被被审计方终止。** `toolfsd` 在宿主以 root 运行，容器内进程无法触及。
4. **并发实例的协调。** `versions` 的去抖判断与数量上限需跨实例串行化（§7.2），由单一的 `toolfsd` 承担最为简单可靠。

**`toolfsd` 不负责容器编排**——那是 `agentd` 的职责（§14.2.2）。两者通过「请求挂载点 / 返回路径」这一接口协作，特权互不耦合。

### 14.3 codex 侧改动：零

app-server 原生支持 unix socket transport（`AppServerTransport::UnixSocket`）与独立 `CODEX_HOME`，多实例**不需要修改任何 Rust 代码**。

#### 14.3.1 状态隔离清单

`CODEX_HOME` 中的状态必须按实例隔离，否则实例间会争抢 SQLite 与启动锁（`app_server_startup_lock_path(&codex_home)` 即由 `CODEX_HOME` 派生）：

| 必须隔离 | 应当共享 |
|---|---|
| `logs_*.sqlite`（会话历史） | `config.toml`（只读挂载） |
| `memories_*.sqlite`（记忆） | `skills/` |
| `goals_*.sqlite` | API key（现经环境变量传入，天然共享） |
| `mcp-oauth-locks/`、启动锁 | |
| `shell_snapshots/` | |

> **顺带收益**：当前单实例意味着全部用户共享 codex 的记忆库，这本身即隐私问题。多实例一并解决。

### 14.4 实例为何触及不到真实数据与保护区

宿主编排形态下，隔离由 **bind 的选择性**直接达成，无需在容器内做路径遮蔽：

| 宿主上的路径 | 是否 bind 进实例容器 |
|---|---|
| 该用户的 ToolFS 挂载点 | ✅ 挂到 `/workspace` |
| 真实数据（`/DATA`、`/media/*`） | ❌ **不 bind**——容器内根本不存在这些路径 |
| 保护区 `.toolfs-state/` | ❌ **不 bind**——回收站、版本、审计均在实例触及范围之外 |

**只 bind 需要的那一个目录**，其余在容器的挂载视图里根本不存在。这比「挂进去再遮蔽」更彻底，也更不容易出错。

> **descriptor-based IO（§4.2）在此仍然必要**，但作用不同：它保证 `toolfsd` 自身在解析路径时不受符号链接与并发替换影响（消除 TOCTOU），而非用于实现遮蔽。

### 14.5 Web 服务侧改造量

实测调用面：**40 处 `appServer.*` 调用，分布于 12 个文件**（`sessions.ts`、`turns.ts`、`skills.ts`、`mcp-oauth.ts`、`connectors/`、`platforms/` 等）。

```ts
export let appServer: AppServerRpcClient;       // 现状：模块级单例
→  getAppServer(userId): AppServerRpcClient     // 改为：按 userId 的实例池
```

多数调用点已处于具备会话上下文之处（`userId` 现成）；`connectors/`、`platforms/` 中的部分调用需补充上下文传递。

**Web 服务只负责「按用户把 RPC 调度到对应实例」**；实例的拉起、回收、健康检查由宿主侧的 `agentd` 承担（§14.2.2），Web 服务向其请求实例地址即可，不直接管理容器。

**属中等规模重构，但显著小于改用 `codex exec` 的代价**——后者需重写事件层，并丢失审批交互（`item/commandExecution/requestApproval`）、MCP OAuth、`ThreadFork`、动态工具等能力。**该工作量在 agent 团队，不计入 ToolFS 工期（§11.1）。**

#### 备选方案对比

| | 包装现有单实例 | `codex exec` 每次调用 | **多实例 app-server** |
|---|---|---|---|
| codex 改动 | 无 | 无 | **无** |
| Web 服务改动 | 无 | 重写事件层 | 40 处补 userId |
| 审批 / MCP OAuth | ✅ | ❌ 丢失 | ✅ |
| 每轮开销 | ✅ 无 | ❌ 进程冷启动 | ✅ 无 |
| per-user RBAC | ❌ | ✅ | ✅ |
| 记忆隔离 | ❌ | ✅ | ✅ |
| 前置条件 | 无 | 账号绑定（§14.6） | 账号绑定（§14.6，过渡期可用固定账号） |

**多实例是三者中唯一无明显短板者。** 若账号绑定短期无法确定，可先按「包装现有单实例」落地（entrypoint 改一行，即得完整误删保护），代码中预留实例池抽象——两者的 toolfsd 侧逻辑一致，仅从「固定身份」变为「按 userId 动态建实例」。

### 14.6 账号绑定（前置条件）

#### 14.6.1 不可再漂移的前提

> **Zima Agent 与 ZimaOS 是两套完全独立的账号体系。**
>
> - agent web 有自己的登录，用户以 **agent 账号**登录，不是 ZimaOS 账号；
> - **请求中不携带 ZimaOS token**，因此**无法通过 JWT 验签获取 ZimaOS 身份**；
> - codex 源码中 `DEFAULT_USER_ID = process.env.WEB_STREAMING_USER_ID ?? "local-user"`，除云盘连接器外无任何 ZimaOS 身份集成；
> - 接入 ZimaOS 网关也**不解决**此问题——网关（Caddy）仅做反向代理，鉴权由各应用自行实现，而 agent 实现的是自己那一套。

因此 ToolFS 无法「读出」当前操作对应哪个 ZimaOS 用户，**必须先建立显式的账号对应关系**：

```
agent 账号  alice@agent   ←── 需建立对应关系 ──→   ZimaOS 账号  subuser
```

建立途径只有两类：**用户自助绑定**（证明拥有该 ZimaOS 账号）或**管理员配置映射**（代为指定）。

#### 14.6.2 卡点：ZimaOS 无授权流程

UserService 仅提供：`/v1/users/login`（用户名密码 → access + refresh token）、`/v1/users/refresh`、`/v1/users/parse-token`、JWKS 端点。**没有 authorize / callback / client 注册**，因此拿不到标准的第三方授权流程。

#### 14.6.3 三个可行方案

| | 做法 | ZimaOS 改动 | 评价 |
|---|---|---|---|
| **A. 输凭据绑定** | 用户在 agent 内输入 ZimaOS 用户名密码 → 调 `/v1/users/login` → 保存 refresh token 并定期续期 | 无 | 立即可做。但**应用收集平台密码属反模式**，且长期保管 refresh token 风险与收益不成比例 |
| **B. 轻量授权流程** | agent 跳转至 ZimaOS 确认页 → 用户在 ZimaOS 界面确认 → 回调携带一次性 code → 换取 token | 需新增端点 | **正确终态**。因属同设备内单一可信客户端，无需实现完整 OAuth，一个确认页 + 一次性 code 即可 |
| **C. 管理员配置映射表** | 管理员配置 `agent 用户 → ZimaOS 授权主体` | 无 | 零协议设计，立即可用。但需手工维护，用户变动时易失配 |

**建议：短期 C，目标 B。** A 不建议采用。

#### 14.6.4 ToolFS 侧的抽象：`IdentityResolver`

**三种方案对 ToolFS 的接口是同一个**，因此绑定方案的选型**不阻塞 ToolFS 开工**：

```go
// 把「agent 用户 → ZimaOS 授权主体」的解析隔离在一个接口后
type IdentityResolver interface {
    // 未绑定时返回 ErrIdentityUnbound，调用方据此提示用户完成绑定
    Resolve(ctx context.Context, agentUserID string) (*ZimaIdentity, error)
}

type ZimaIdentity struct {
    Username string   // ZimaOS 授权主体，用于拉取 Grant
    UserID   int
    Role     string
}
```

- **C 方案**：实现为读取配置文件的 `StaticMapResolver`；
- **B 方案**：实现为查询绑定表的 `BindingStoreResolver`；
- **过渡期**：实现为恒返回同一专用账号的 `FixedResolver`（见下）。

`toolfsd` 在创建实例前调用它取得 ZimaOS 授权主体，再据此拉取 Grant 并建立挂载。

#### 14.6.5 过渡方案：单一专用账号

绑定方案定稿前，可令全部实例绑定同一个 ZimaOS 专用账号：

- 建议建立 `zima-agent` 账号，**而非复用 admin**——admin 的授权由系统自动写入全部存储根 rw（§2.5④），等同不设限；专用账号可由管理员经 ZimaOS 既有界面精确授权，且审计归属清晰；
- 此时具备**完整的误删保护**与 **agent 整体的权限约束**，仅缺用户间隔离；
- 代码结构保持 `IdentityResolver` 抽象，绑定方案就绪后替换实现即可，无需重构。

### 14.7 三档通道划分

```
app-server 实例（运行于 toolfsd 建立的 namespace 内）
  ├─ 数据面 ── cwd = 挂载点下的工作目录        ← FUSE
  ├─ 查询面 ── cat .toolfs/*.json              ← FUSE（只读虚拟视图）
  └─ 变更面 ── MCP server (toolfs-mcp)         ← 结构化操作
```

| 操作 | 通道 | 依据 |
|---|---|---|
| 读写真实文件 | FUSE | 本来就是文件操作，agent 零改造 |
| **查询**恢复点 / 变更 / 回收站 | **FUSE · `.toolfs/`** | 只读、无副作用、结果为 JSON，可用「读文件」表达 |
| **执行**闪回 / 撤销 | **MCP** | 需结构化返回：「15 项中 3 项因冲突跳过」用 errno 表达不了 |

> **不要在 MCP 中暴露 `read_file`/`write_file`/`list_dir`**——那些走 FUSE。重复暴露会让 agent 困惑该用哪条路径，更危险的是可能绕过保护链。需同步修正 `ToolFS-Plan.md` 中「把注册技能自动暴露为 MCP tool（read/write/list/skill 映射）」的表述。

### 14.8 数据根目录的接入

codex 的数据根由单一环境变量决定，指向 ToolFS 挂载点即可：

```yaml
WEB_STREAMING_BUSINESS_ROOT: /mnt/toolfs/workspace/business   # 原为 /data/codex/life-recorder（容器内视角）
```

`vaultRegistry` 与 `projectTreeRootPath(userId)` 均基于此根构建，因此 app-server 实例的全部 thread cwd、以及其派生的 python / git / npm 子进程，都落在挂载点内。

codex 侧已有的 `assertProjectPathInsideRoot` 只约束 HTTP API 的路径参数；**agent 经 shell 执行的文件操作由 ToolFS 在 VFS 层拦截**——两者互补，不重叠。

### 14.9 `.toolfs/` 只读控制视图

per-instance 虚拟目录，随挂载出现，**只读**（写入返回 `EROFS`）：

```
/mnt/toolfs/workspace/                  ← 容器内视角（宿主为 /mnt/toolfs/<uid>/workspace/）
├── business/                           ← codex 的数据根
└── .toolfs/
    ├── trash.json                      回收站条目
    ├── changes.json                    本实例全部变更
    ├── changes/<task-id>.json          按任务分组
    ├── restore/<url-encoded>.json      指定路径的恢复点
    └── capabilities.json               各存储能力 + 虚拟路径→真实路径映射
```

约束：逐条目按**当前** Grant 过滤；`.toolfs` 为全局 reserved name；不返回 `FOPEN_KEEP_CACHE`；`restore/` 的 `Readdir` 仅列本实例变更过的路径。

### 14.10 资源与生命周期（实测数据）

#### 14.10.1 启动耗时

| 场景 | 耗时 | 说明 |
|---|---|---|
| **实例容器冷启动**（仅 app-server） | **0.75–5.4 s**（12 次采样） | 方差大；耗时几乎全在**进程自身启动**（447 MB 二进制加载），非 Docker 开销。方差成因未能隔离 |
| **预热池分配**（§14.10.7） | **136 ms** | 容器已预热并预读二进制，分配时仅需挂载 + exec |
| 同容器内起 app-server 进程 | 422 ms | 该形态已因 `/proc` 穿透被排除（§14.2.3），仅作参照 |
| 完整 agent 容器冷启动 | 约 7.0 s | daemon 0.8s → openrt 1.8s → app-server 3.2s → feedback 5.4s → Web 7.0s。**实例容器不需要这整套栈** |

> 就绪信号：**socket 文件出现即可建连**（实测 connect 成功），可作为 `agentd` 的健康判据。

**早先「独立容器 = 7 s」的判断偏悲观**：7.0 s 是**完整 agent 栈**（含 openrt、feedback、Web 服务）的数字，而实例容器只跑 app-server，实测 0.75–5.4 s。叠加预热池后，用户可感知的分配延迟降至 **136 ms**（§14.10.7），启动体验不再是选择独立容器的代价。

> **实现细节**：实例的 `CODEX_HOME` 必须位于容器的可写数据卷内（如 `/data/codex/homes/<uid>`）。实测放在 `/tmp` 下启动失败（`Operation not permitted`）。

#### 14.10.2 内存占用

| | 内存（RSS） | 进程数 |
|---|---|---|
| **app-server 实例（新起）** | **152 MB** | 1 |
| app-server（带会话运行中） | 204 MB | 1 |
| **无头 Chromium 实例** | **645 MB** | **8** |
| openrt（浏览器管理器） | 6.4 MB | 1 |
| 容器总占用（当前状态） | 573 MB | — |

**浏览器比 agent 实例贵 4 倍多**，是容量规划的主要矛盾，而非 app-server 本身。

#### 14.10.3 容量测算

设备总内存 15.37 GB，另有 23 个其他容器在运行。按「每用户一路会话」估算：

```
152 MB (app-server) + 645 MB (浏览器) ≈ 800 MB / 用户
15.37 GB ÷ 800 MB ≈ 18 用户理论上限
```

**风险在浏览器侧**：当前为「每 thread 一个浏览器实例且无自动回收」，一个用户开多个 thread 即会迅速耗尽内存。

#### 14.10.4 建议：浏览器实例独立化 + per-user 复用

**openrt 已具备独立服务的全部特征**，拆分成本极低：

| 特征 | 现状 |
|---|---|
| 独立进程 | ✅ 已是 |
| 绑定所有接口 | ✅ `OPENRT_LISTEN=0.0.0.0:9731` |
| 完整 REST API | ✅ `/v1/browser/instances`（增删查停）+ `/healthz` + noVNC 代理 |
| 客户端解耦 | ✅ Web 服务经 `browser-proxy.ts` 转发 |
| **服务地址可配置** | ✅ **`OPENCLI_RUNTIME_URL` 为环境变量** |

因此拆分只需新增一个仅运行 openrt 的容器，并把 agent 侧的 `OPENCLI_RUNTIME_URL` 指过去：

```yaml
browser-service:
  image: icewhaletech/codex:latest
  command: openrt --root /data/browser serve --listen 0.0.0.0:9731
  shm_size: "2gb"

agent:
  environment:
    OPENCLI_RUNTIME_URL: http://browser-service:9731    # 原为 http://127.0.0.1:9731
```

**复用粒度建议：per-user + 空闲回收。**

`close_browser_instance` 的语义已支持复用——「停止进程但**保留 profile 与登录态供下次打开**」，因此机制现成，缺的只是策略：

| 方案 | 内存 | 问题 |
|---|---|---|
| 现状：per-thread、不回收 | N_threads × 645MB | 不可控 |
| per-user 共享 | N_users × 645MB | 同用户并发 thread 争用同一浏览器，页面状态互相干扰 |
| 全局池 + 归还 | K × 645MB 固定 | **登录态会串**：归还不清 profile 则泄露，清了又失去保留登录态的价值 |
| **per-user + 空闲回收**（推荐） | 活跃用户 × 645MB | 需空闲判定策略；可按用户允许至多 2 个实例以缓解并发争用 |

按 3 个并发活跃用户估算：`3 × 645 + 3 × 152 ≈ 2.4 GB`，在 15 GB 上宽裕。

#### 14.10.5 实例生命周期策略

| 项 | 建议 |
|---|---|
| 实例容器空闲回收 | N 分钟无活动即由 `agentd` 停止容器，并请求 `toolfsd` 卸载对应挂载（挂载不会随容器销毁自动清理，§14.11）；下次访问从**预热池**分配，延迟约 136 ms（§14.10.7） |
| 浏览器空闲回收 | 调 `/v1/browser/instances/{id}/stop`，profile 保留 |
| 并发上限 | 配置最大实例数，超限时按 LRU 回收 |
| 实例崩溃 | `agentd` 检测并回收容器，请求 `toolfsd` 卸载挂载；Web 服务感知后重建 |

#### 14.10.6 浏览器数据位置对 ToolFS 的影响

**浏览器 profile 不落在 ToolFS 路径上。** 它位于 `OPENRT_ROOT=/data/codex/opencli-runtime/`，属于 `CODEX_HOME` 一侧而非业务数据根。因此 Chromium 极高频的 cache / IndexedDB / Local Storage 写入**不会经过 ToolFS 挂载点**，无需为其设排除规则（§7.2.0）。浏览器拆为独立服务后隔离更彻底。

**但托管下载需留意**：`SE_NODE_ENABLE_MANAGED_DOWNLOADS=true` 表明存在托管下载能力。若下载目标位于业务数据根内，将走 ToolFS 保护链——这本身合理（下载文件理应受保护），但大文件下载时 `MaxVersionFileSize` 阈值需相应考虑。**下载目录的具体配置位置尚未查明，实施前需确认。**

#### 14.10.7 实例预热池（实测验证）

**问题**：挂载是用户专属的，而 `docker run -v` 在容器创建时就固定了 bind——通用预热容器如何在分配时拿到某个用户的挂载？

**答案正是 §6.6 确立的挂载点结构**：bind 源是稳定普通目录，FUSE 挂在其下一层。预热时 bind 一个空的 slot 目录，分配时由 `toolfsd` 在该 slot 内建立挂载，经 `rslave` 传播进**已在运行**的容器。这是同一个结构决策的第二个收益。

```
宿主：
  /mnt/toolfs/pool/slot-N/              ← 预热时 bind 进容器（空目录）
  /mnt/toolfs/pool/slot-N/workspace/    ← 分配给用户后，toolfsd 在此挂 FUSE 视图

预热容器 entrypoint：
  cat /usr/local/bin/codex > /dev/null              # 预读 447 MB 二进制进页缓存
  until [ -d /mnt/toolfs/workspace ]; do sleep 0.02; done   # 等待被分配
  exec codex app-server --listen unix:///sock/app.sock
```

**实测结果**（10.0.0.75，端到端验证）：

| 阶段 | 实测 | 说明 |
|---|---|---|
| 预热 | 4819 ms | 后台完成，用户无感 |
| **空转内存** | **2.4 MB** | 仅一个 shell 在等待，池化成本极低 |
| **分配 → 可服务** | **136 ms** | 用户可感知的全部延迟 |
| 服役内存 | 40 MB | app-server 已 exec |

容器正确读到了**分配后才出现**的挂载内容，验证了 `rslave` 传播路径成立。

**强制约束**：

1. **slot 一次性使用，绝不跨用户复用。** 服务过用户 A 的容器必须销毁后补充新的——`CODEX_HOME`、`/tmp`、进程内记忆与各类缓存都会残留，复用即数据泄露。**预热的含义是「提前付出启动成本」，不是「跨用户复用容器」。**
2. 预热容器**不持有任何用户身份**，身份完全由分配时注入的挂载承载——与 §1.1「承载身份的是挂载点」一致。
3. 池容量 N 可配置，分配即触发补充；空闲 slot 每个仅 2.4 MB，N 取值不受内存约束。
4. slot 目录本身按 §6.6 规则 bind（`rslave`，禁 `rshared`）。

**未验证项**：app-server exec 后是否会因数据根（`WEB_STREAMING_BUSINESS_ROOT`）此前不存在而有异常行为，需在 P2 联调时确认。本实测只验证到「socket 就绪且可建连」。

#### 14.10.8 跨容器通信必须用 unix socket，不能用 TCP

实测发现：**app-server 拒绝非 loopback 的 ws 监听**——

```
Error: refusing to start non-loopback websocket listener 0.0.0.0:4222 without auth;
       configure `--ws-auth capability-token` or `--ws-auth signed-bearer-token`
```

实例容器与 Web 服务分属不同容器，若走 TCP 即触发该限制，需额外配置 `--ws-auth` 并管理令牌。

`codex app-server --listen` 原生支持 `unix://PATH`（`--help` 实测确认，取值域为 `stdio:// | unix:// | unix://PATH | ws://IP:PORT | off`）。因此**实例容器监听 unix socket，socket 文件经 bind mount 暴露给 Web 服务**：

```
-v /run/toolfs/instances/<uid>:/sock
codex app-server --listen unix:///sock/app.sock
```

无需 TCP、无需令牌管理，且 socket 文件的目录权限天然构成访问控制。Web 服务侧 `DEFAULT_APP_SERVER_URL = "ws://127.0.0.1:4222"`（`config.ts:43`）改为按用户解析 socket 路径即可，属实例池改造的一部分（§14.5）。

### 14.11 故障与边界

| 情况 | 行为 |
|---|---|
| `toolfsd` 未运行 | 实例无法创建，**不降级为直接访问真实数据路径** |
| 授权拉取失败 | fail closed，拒绝创建实例 |
| `toolfsd` 崩溃 | **所有挂载同时中断**——toolfsd 是 FUSE server 本身（单进程 serve 全部挂载，§6.5），容器内已打开 fd 得到 `ENOTCONN`。systemd 拉起后重挂，凭 `rslave` 对容器可见（§6.6）；`agentd` 应感知并回收受影响实例。**早先「已有实例继续运行」的说法错误**，已更正 |
| 实例被终止 | 容器销毁，但**宿主侧的 FUSE 挂载不会自动清理**——须由 `agentd` 在回收容器后显式请求 `toolfsd` 卸载，否则遗留悬挂挂载 |
| Grant 被撤销 | 按 §5.2 的 TTL 与事件失效；已打开 handle 复核后同样拒绝 |
| agent 尝试绕过 | 以 `seluser` 零 capability 运行，无法 mount、无法读块设备、无法触碰 `0700` 保护区 |
| 蓄意绕过 | 理论上可经 `/proc/<toolfsd-pid>/fd/` 迂回，但 `seluser` 无权读取 root 进程的 fd 目录。**当前配置下无已知可行绕过路径** |

### 14.12 待确认问题

| # | 问题 | 影响 |
|---|---|---|
| 1 | **账号绑定采用哪种方案？** 建议短期 C（管理员映射表）、目标 B（ZimaOS 轻量授权流程）；A（收集平台密码）不建议 | per-user 隔离的前提。**因有 `IdentityResolver` 抽象，不阻塞 ToolFS 开工** |
| 2 | 若采用 B，ZimaOS 侧新增授权端点由谁排期？ | 决定终态落地时间 |
| 3 | ~~实例形态选型：进程 + 三重加固还是独立容器？~~ → **已决策：独立容器**（§14.2.3）。`/proc/<pid>/root` 穿透使「进程 + 加固」不可靠，且失效无迹象。早先认为代价是「7 s vs 422 ms」，实测后该代价基本消失：实例容器仅 0.75–5.4 s，叠加预热池后分配延迟 136 ms（§14.10.7） | ✅ 已决策，且代价小于预期 |
| 4 | Web 服务的实例池改造（40 处调用点 / 12 文件）由谁承担、何时排期？ | 决定 per-user 隔离的落地时间 |
| 5 | 实例数上限与空闲回收策略？实测 app-server 152 MB / 浏览器 645 MB，设备 15.37 GB（§14.10） | 决定资源占用上限 |
| 6 | **是否将浏览器拆为独立服务并改为 per-user 复用？** openrt 已具备独立服务全部特征，改动仅为新增容器 + 改一个环境变量（§14.10.4） | 决定内存是否可控——浏览器占比是 agent 实例的 4 倍 |
| 7 | 浏览器托管下载的目标目录配置在何处？若在业务数据根内需调整版本保护阈值 | 影响大文件下载体验 |
| 8 | **过渡期是否接受「单一专用账号 + 完整误删保护」先行落地？**（§14.6.5） | 决定能否立即开工并交付价值 |
| 9 | 「主数据只有版本与回收站、无时间点快照」是否可接受？ | 若否需评估迁移至 btrfs 存储（§2.5①） |

### 14.13 独立于本方案的安全发现

以下问题**与 ToolFS 无关、独立存在**，建议同步告知 agent 团队：

**agent web 服务在 `0.0.0.0:8787` 无鉴权暴露。** 实测无任何凭据即可 `GET /api/sessions` 返回 200 与会话数据。同网段任何人都能打开 agent、创建会话、驱使其读写 `/DATA`——而 agent 当前对数据有完整访问权。

ToolFS 落地后可收窄「agent 能碰哪些目录」，但**无法阻止未授权者驱使 agent**——那需要 agent 自身的鉴权。建议优先修复。

---

## 15. 实现进展（P0–P4 已落地）

> 本章记录**代码实际长成的样子**，与前文的设计意图对照。分支 `feat/toolfsd-rbac-mount`。
> 真机验证于 10.0.0.75（Linux 6.18.9）。
>
> 提交序列：P0 安全地基 → P1 授权接入 → P2 挂载视图 → 注释统一 → toolfsd 守护进程
> → 文档同步 → P3 保护链 → 保护链接入 FUSE → P4 恢复能力 → 控制视图。

### 15.1 已交付

| 阶段 | 状态 | 关键文件 |
|---|---|---|
| **P0** 安全地基 | ✅ S1–S6 全修，先红后绿 | `pathsec.go`、`securepath.go`、`toolfs.go`、`sandbox.go` |
| **P1** 授权接入 | ✅ 逐行 Grant + 不可变快照 | `rbac.go`、`permission_provider.go` |
| **P2** per-user 挂载 | ✅ **原验收标准真机通过** | `identity.go`、`namespace.go`、`fuse_rbac.go`、`mountset.go` |
| **toolfsd 服务** | ✅ 守护进程 + 控制接口 | `control.go`、`cmd/toolfsd/main.go` |
| **P3** 保护链 | ✅ 回收站 + 版本，已接入 FUSE | `protection.go`、`protection_bound.go`、`fuse_mutate.go` |
| **P4** 恢复能力 | ✅ 三层恢复源 + 闪回 + 任务级撤销 | `recovery.go` |
| **控制视图** | ✅ `.toolfs/` 只读 | `fuse_control_view.go` |

**挂载已可写，且写入受保护**：删除默认不授予（`EPERM`）；授予后 `rm` 进回收站而非
真删；覆盖写、`Create` 覆盖既有文件、`truncate` 三条路径都先留存旧内容。

### 15.2 实现与设计稿的差异（均为落地时的修正）

| # | 设计稿写的 | 实际实现 | 原因 |
|---|---|---|---|
| 1 | `Operation` / `OpRead…` | **`AccessOp`** / `OpRead…` | `skills.go:690` 已有 `type Operation struct`，名字冲突 |
| 2 | 手写 `openat2(RESOLVE_BENEATH)` | **`os.Root`**（Go 1.24） | 其 Linux 实现内部就是 `openat2(RESOLVE_BENEATH)`，由 Go 团队维护，比自写 syscall 更可靠。代价：`go.mod` 从 1.21 升到 **1.25**（`os.Root.Rename` 需要 1.25），抬高了本库消费者的最低 Go 版本 |
| 3 | 扩展 `Session` 加 `View` 字段 | **另起一套 FUSE 节点**（`instanceRoot/instanceDir/instanceFile`） | legacy `ToolFSRoot` 会暴露 `memory`/`rag`/skill mounts；分开两棵树使 RBAC 路径**不可能**回归既有行为，也避免「持有文件 Grant 即开放虚拟后端」 |
| 4 | 挂载点 `/mnt/toolfs/<user-id>/workspace/` | **`<root>/u<uid>/workspace`** | 目录名取数字 uid 并加 `u` 前缀；用户名属可被影响的数据，不应未经校验插进路径 |
| 5 | `MountForIdentity(ctx, toolfs, mountPoint, identity, caps, config)` | 参数改为 `(ctx, resolver, provider, agentUserID, mountPoint, caps, config)` | 身份解析与授权拉取下沉进函数内部，调用方不必自己拼装 identity |
| 6 | —— | 新增 **`FUSEAvailable()`** 前置探测 | go-fuse 在无 FUSE 实现时**挂起而非报错**；且 macOS 常残留 `osxfuse.fs` 目录（本机即 2022 年残留、kext 未加载），故按**设备节点**判定而非按目录判定 |
| 7 | **保护区在挂载点【外】的独立目录**（§7.3 per-device StateRoot） | **改到每个 Grant 根【之内】** `<grantRoot>/.toolfs-state/` | `os.Root` 无法表达跨 root 的 rename，而回收站正是一次 rename。改到 Grant 根内后全部操作经同一受约束句柄发起：**TOCTOU 消失**（rename 与包含性检查同一描述符）、**EXDEV 在构造上不可能**（必然同文件系统）。代价：保护区落在用户数据里，已对 FUSE 隐藏，但**仍须从 SMB 与 Files API 排除** |
| 8 | `ChangeRecord` | **`ProtectedChange`** | `toolfs.go:275` 已有同名类型（属快照模块） |
| 9 | —— | 跨 Grant rename 返回 **`EXDEV`** | 两个 Grant 权限可能不同、甚至不同文件系统；返回 EXDEV 后内核退化为 copy-then-delete，从而让删除重新走回受保护路径 |
| 10 | —— | 控制视图必须**显式**拒绝变更 | 实测：不实现 handler 并不等于拒绝，go-fuse 默认行为下未实现的 `Unlink` 会**成功** |
| 11 | 分段前缀匹配只认 `/` | 边界另认 **`?`** | 虚拟后端把查询串编码在路径里（`/toolfs/rag?text=x`，`readRAG` 按 `?` 切分）。放宽不引入逃逸风险：字符串匹配只决定选中哪个挂载点，真正的边界由 `RootHandle` 在内核层守住 |

### 15.3 保护与恢复的实际语义

**删除**：默认不授予，返回 `EPERM`（语义是「本实例不得删除」而非「路径不可达」）。
授予 `AllowDelete` 后仍可恢复——条目 rename 进保护区而非 unlink。`rename` 移除了
源名字，因此要求与删除同一权限。

**版本护栏**三道，缺一不可（`/DATA` 是 ext4、无 reflink，每次留版本都是全量拷贝）：

| 护栏 | 缺了会怎样 |
|---|---|
| 排除规则（`.git`/`node_modules`/`__pycache__` 等，按**路径分量**匹配） | 一次 `npm install` 即可打满仅剩 31G 的盘 |
| 去抖 + 单文件版本数上限 | agent 是突发写入，每次保存都会产生一个版本 |
| 低空间降级为「只保回收站」 | 把盘写满 |

> 排除规则**只跳过版本留存，删除仍进回收站**——`.git` 本就有完整历史，但误删 `.git`
> 依然要能恢复。

**撤销**按逆序执行、冲突跳过并报告：

- 逆序是必须的：任务先 `rename A→B` 再 `B→C`，正序撤销会在 B 上撞车
- 冲突判定比对变更后记录的 `size`/`mtime`；不一致说明此后有他人改动（另一会话、
  共享目录的另一用户、或 SMB），覆盖会毁掉那份工作
- **部分成功**：一处冲突不废掉整次撤销，跳过项在报告中给出原因

**恢复按【当前】授权判定**，而非内容被留存时的授权。否则撤权后仍能经旧版本把数据
取回，撤权形同虚设。

#### 落地时暴露并修复的三个 bug

这三个都只有真机跑起来才会显形，值得记录：

| 现象 | 根因 |
|---|---|
| 新建被记成「修改」，撤销时变成恢复而非移除 | `Create` 已经建了空文件，随后写句柄的 `ReadFile` 成功 → 误判为已存在。改为在 `Create` 处显式标记 `isNew` |
| 修改撤销失败「无留存内容」 | `os.WriteFile` 以 `O_TRUNC` 打开会**先触发 `Setattr`**，它取走版本并**消耗了去抖窗口**，使写句柄自己的 `CaptureVersion` 返回 nil。改为回退引用该路径最近一次版本——内容确实已留存，只是不是这次留的 |
| 闪回未留存「闪回前的内容」，误闪回不可撤销 | 闪回也被去抖挡住。闪回是一次**刻意的破坏性操作**，与突发写不同，改为强制取版本 |

### 15.4 `.toolfs/` 只读控制视图

让 agent 用一次普通读回答「我刚改了什么」「这个文件能回到哪些状态」，不必依赖带外
API——agent 面前本来就是一个文件系统。

```
.toolfs/README                 用法说明
.toolfs/changes.jsonl          本实例的受保护变更，最旧在前
.toolfs/trash.jsonl            仍可恢复的删除条目
.toolfs/restore-points.jsonl   各路径可回到的状态，最新在前
```

三条承重性质：

1. **只读**，所有变更操作显式返回 `EROFS`。见 §15.2 第 10 条——不实现 handler 不等于拒绝。
2. **按当前授权过滤，读取时求值**。撤权后条目立即消失，否则控制视图会成为绕过授权
   取回内容的旁路。
3. **每次读现生成 + direct IO**。缓存会让陈旧列表活过一次撤权。

`restore-points` 只枚举本实例**实际动过**的路径，不遍历整个 Grant——遍历会把控制视图
变成一份目录清单，泄露调用方本不会去枚举的内容。

保留名优先于用户数据：名为 `.toolfs` 的 Grant 不能遮蔽控制视图（`sanitizeEntryName`
拒绝产出该名字，`Lookup` 侧是这个保证的另一半）。

### 15.5 控制接口（供 `agentd` 对接）

unix socket 上的 HTTP，**权限 0600**——一次挂载请求等同于把某人的授权投射到文件系统上，
不能对本机所有用户开放，也不应暴露端口。

```
GET    /healthz                      → {"status":"ok","mounts":N}
GET    /v1/mounts                    → {"mounts":[…]}
POST   /v1/mounts                    → 201 建挂载；200 已存在（幂等）；403 账号未绑定
       {"agent_user_id":"…", "allow_delete":false, "deny_patterns":[…]}
DELETE /v1/mounts/{agentUserID}      → 204 / 404
```

`POST` 的响应同时给出 **`bind_source`** 与 **`mount_point`**：

```json
{"username":"alice","user_id":1001,
 "bind_source":"/mnt/toolfs/u1001",
 "mount_point":"/mnt/toolfs/u1001/workspace"}
```

**`agentd` 要 bind 的是 `bind_source`（父目录），不是 `mount_point`。** 这正是 §6.6 的结论：
bind 源若取挂载点本身，toolfsd 重挂后容器会静默沿用旧视图，使撤权失效。

### 15.6 真机验证记录（10.0.0.75）

**隔离与编排**（拉起真实 daemon 走完整链路）：

```
alice 挂载点 → Documents      bob 挂载点 → Media
alice 访问 Media      → No such file or directory
bob   访问 Documents  → No such file or directory
未绑定 agent 账号     → HTTP 403
SIGTERM               → 2 个挂载全部卸载，socket 已删除，无残留
```

**保护与恢复**：

```
默认配置       rm file.txt        → 失败（删除默认不授予）
授予 AllowDelete rm file.txt      → 成功，内容进 .toolfs-state/trash/
覆盖写         echo x > file.txt  → 成功，覆盖前内容进 versions/
只读 Grant     写 / 删            → 全部拒绝，且不创建保护区
保护区         ls / stat / 读     → 挂载点内完全不可见

一个任务改了 A、建了 B、删了 C  → UndoChanges({TaskID})
  undo delete C -> applied     undo create B -> applied     undo modify A -> applied
第三方绕过挂载点改动后再撤销      → skipped，并给出原因；其余项照常完成
```

测试规模：安全 S1–S6、授权、命名空间、挂载、控制接口、变更、恢复、控制视图
各组用例。本地 `go test` / `-race` / `vet` 全绿，既有 101 个测试无回归；
Linux 真机全量 PASS。

> **本地跑绿不等于真的绿。** macOS 上没有 macFUSE，全部挂载类用例都会被跳过——
> 加控制视图时把 `.toolfs` 计入了根目录集合断言，就是只有 Linux 真机才暴露的。
> 涉及 FUSE 行为的改动必须在 Linux 上复验。

### 15.7 尚未实现的设计内容

以下在前文有设计、但代码里还没有：

- **btrfs 快照**（§7.5–7.7）：范围外的可选增强，`/DATA` 是 ext4 用不上
- **审计落盘**：`AuditLogger` 接口在，但受保护变更目前只写变更日志，未落独立审计文件（§9）
- **权限判定缓存**：§6.4 的 `(generation, path, op)` 应用层缓存尚未加，当前每次判定都走 Trie
- **撤权的主动失效**：现为 TTL 到期后重建视图；`NotifyEntry`/`NotifyContent` 尚未接入
- **回收站与版本的保留策略**：`MinFreeBytes` 已用于降级判断，但定时清理（`PurgeTrash`）尚未实现——回收站的 rename 不释放空间，长期运行必须补上
- **`FilesServiceProvider` 的真实联调**：上游逐行 Grant 接口未就绪，当前以
  `StaticPermissionProvider` 与 httptest 桩覆盖；端点路径 `/v2_1/files/users/{u}/grants`
  是**假定值**，需与 IceWhale-Files 对齐
- **保护区的多通路排除**：`.toolfs-state` 已对 FUSE 隐藏，但**尚未从 SMB 与 Files API
  排除**。不做这一步，被删内容可经其他通路读回——这是 §15.2 第 7 条那个设计修正
  带来的连带要求，必须在联调清单里

