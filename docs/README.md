# ToolFS 设计文档索引

## 当前版本

| 文档 | 用途 | 篇幅 |
|---|---|---|
| **[`review-brief.md`](review-brief.md)** | **团队评审用**。决策、取舍、范围、风险与待拍板问题，不含实现细节 | ~15 分钟读完 |
| [`design-rbac-snapshot.md`](design-rbac-snapshot.md) | 完整设计稿 v7 + **§15 实现进展** | 约 2280 行 |

**评审请从 [`review-brief.md`](review-brief.md) 开始。**

> ⚠️ **设计部分（§1–§14）仍处于设计稿阶段**，尚未评审、未确认。
> **但代码已大体落地**：P0 安全地基、P1 授权接入、P2 per-user 挂载、P3 保护链、
> P4 恢复能力、toolfsd 守护进程与 `.toolfs/` 只读控制视图均已在分支
> `feat/toolfsd-rbac-mount` 完成，**原验收标准与防误删语义都已在真机（10.0.0.75）验证**。
> 实现现状、与设计稿的差异（含一处保护区位置的设计修正）、以及尚未实现的部分见
> **§15 实现进展**。
> 若要讨论或实施，以 v7 为准；历史版本归档在 [`history/`](history/)，仅供追溯决策来由。

### 交付方式：每用户一个实例容器，宿主侧编排（v7）

依据对 `IceWhaleTech/codex` 的源码调查与真机实测确定：

> Agent 是**常驻 Web 服务 + 常驻 app-server**，codex core 内嵌于 app-server 进程内——**没有可被包装的 per-task 进程边界**。
>
> 改为 **每用户一个实例容器**：宿主的 `toolfsd` 按身份建挂载并返回路径，`agentd` 拉起实例容器并 bind 该挂载点，Web 服务按用户调度请求。**承载身份的是挂载点**，实例对身份无感。
>
> **codex 侧零改动**；Web 服务侧需把单例改为按用户的实例池（实测 40 处调用点 / 12 文件）。

五点关键：

- **跨用户共享目录的可见性经实测成立** —— 两个独立 FUSE 挂载（不同 superblock）指向同一后端时，TTL=0 配置下双向即时可见，与挂载传播无关；放开缓存后的实测破绽是 negative entry（陈旧恰好一个 TTL）；
- **挂载传播已收敛为固定规则** —— bind 源取 FUSE 挂载点的父级普通目录 + `rslave`；早先「普通 bind 即可」的判断被实测推翻（源本身是挂载点时，toolfsd 重挂会导致撤权静默失效）；
- **启动代价经实测消解** —— 7 秒是完整 agent 栈的数字；实例容器仅 0.75–5.4 秒，叠加预热池后分配延迟 **136 ms**（预热容器空转仅 2.4 MB）；
- **只 bind 需要的目录** —— 真实数据与保护区**不进容器**，比「挂进去再遮蔽」更彻底；
- **隔离由 runtime 保证** —— 同容器多进程方案下，`/proc/<pid>/root` 可穿透 mount namespace（实测同用户进程间可完整读取对方根目录），独立容器天然消除该问题。

**关键约束（不可再漂移）**：

> **agent 与 ZimaOS 是两套完全独立的账号体系。** agent web 有自己的登录，请求中**不携带 ZimaOS 凭据**；接入 ZimaOS 网关也不解决——网关仅做反向代理，鉴权由各应用自行实现。
>
> 必须显式建立 `agent 账号 → ZimaOS 授权主体` 的对应关系。ToolFS 侧以 `IdentityResolver` 抽象隔离，**绑定方案选型不阻塞开工**。

---

## 1. 版本演进一览

| 版本 | 日期 | 文件 | 核心主张 | 为何被取代 |
|---|---|---|---|---|
| v1 | 07-23 | [`history/v1-toolfs-own-policy-initial.md`](history/v1-toolfs-own-policy-initial.md) | ToolFS 自建一套 Policy 配置文件（JSON/YAML）定义「角色→路径→操作」 | 实证复核发现事实错误与危险默认值 |
| v2 | 07-25 | [`history/v2-toolfs-own-policy.md`](history/v2-toolfs-own-policy.md) | v1 + 新增 P0 路径安全阶段 + state 目录移出 mount + 修正快照默认值 | 全栈调查发现自建授权体系方向错误 |
| v3 | 07-25 | [`history/v3-consume-zimaos-grants.md`](history/v3-consume-zimaos-grants.md) | 改为消费 ZimaOS 既有授权（share 表）；快照延后；per-user 挂载视图确立为核心交付物 | 安全评审发现接口选型与 IO 模型不足以支撑安全承诺 |
| v4 | 07-26 | [`history/v4-descriptor-based-grants.md`](history/v4-descriptor-based-grants.md) | 逐行 Grant 契约 + descriptor-based IO + `SessionView`（v7 更名 `InstanceView`）/MountHandle + per-device StateRoot | 快照/闪回整章缺失，且 versions 只写不读 |
| v5 | 07-29 | （已并入 v6） | 补齐快照与闪回：分权、三层恢复源、会话级撤销、能力分层 | 曾误判「共享容器无法区分归属」而提议拆期 |
| v6 | 07-29 | （已并入 v7） | 容器内 `toolfs run` + mount namespace；恢复完整 RBAC；排除规则 | 源码调查证明 `toolfs run -- codex` 模型不适用 |
| **v7** | **07-29**<br>（07-30 修订） | **[`design-rbac-snapshot.md`](design-rbac-snapshot.md)** | **每用户一个实例容器，宿主侧 `agentd`/`toolfsd` 分工编排**；codex 侧零改动；账号绑定为前置条件。07-30 修订：挂载结构 + `rslave`、共享目录并发语义 | **现行** |

> **v1/v2 文件说明**：两者都从未提交进 git，磁盘副本在后续写入时被覆盖。现存文件是依据会话记录重建的（v1 为原始写入内容，v2 为 v1 + 全部 v2 编辑），内容应当一致，但未做字节级校验。
>
> ⚠️ **v1 的 §3.2 含一处已知事实错误**——它写「复用 `normalizeVirtualPath`（防 `../` 逃逸）」，该函数实际不清理 `..`。这个错误正是 v2 新增 P0 阶段的起因，重建时予以保留以还原历史原貌，**阅读时请勿采信**。

---

## 2. 关键决策的演进

这张表是本索引最有价值的部分——它记录了每个决策**为什么变成现在这样**，避免重新讨论已经settled 的问题。

| 议题 | v1–v4 | v5 | v6 | v7（现行） |
|---|---|---|---|---|
| **授权来源** | 自有 Policy → ZimaOS share 表 | 同 v4 | 同 v5 | 同 v5 |
| **路径安全** | 误判 → descriptor-based | 覆盖快照内读取 | 同 v5 | 同 v5 |
| **身份传递** | 从 FUSE caller UID 推断 | 同 v4 | `toolfs run --token` | **挂载点承载身份**，每用户一个实例容器 bind 之 |
| **实例隔离** | UID 校验（容器内失效） | 同 v4 | mount namespace/任务 | **独立容器，runtime 保证**（同容器多进程会被 `/proc` 穿透） |
| **编排归属** | — | — | — | **`toolfsd` 管挂载 / `agentd` 管容器**，特权互不耦合 |
| **挂载点结构** | 挂载点即 bind 源 | 同左 | 同左 | **bind 源取挂载点父目录 + `rslave`**（否则 toolfsd 重挂后撤权静默失效） |
| **集成方式** | 宿主挂载 | 同 v4 | 包装 codex 命令行 | **改一个环境变量指向挂载点** |
| **codex 侧改动** | — | — | 假设可包装 | **零**（原生支持 unix socket） |
| **快照/闪回** | 延后 | 分权 + 三层恢复源 | 同 v5 | 同 v5 |
| **admin 语义** | 特权放行 | **数据驱动，无旁路** | 同 v5 | 同 v5 |
| **工期估算** | 15–21 + 联调 | 拆两期 | 16–22 + 3–4 | **ToolFS 16–22 + Web 服务改造另计** |

---

## 3. 已被否决的设计（不要重新提出）

| # | 设计 | 出现于 | 否决原因 |
|---|---|---|---|
| 1 | ToolFS 自建 Policy 配置文件定义权限 | v1, v2 | 造成**第二套权限真相**：管理员在 ZimaOS UI 改的权限 ToolFS 不知道，反之亦然。分歧即安全事故 |
| 2 | 用 `/v2_1/files/share` 聚合接口作授权输入 | v3 | 面向 UI 的路径聚合模型，无法表达「同一路径下某用户记录已删除、其他用户仍有效」，不适合作安全契约 |
| 3 | 用 `/share/permission?username=` 作授权输入 | （曾评估） | handler 未过滤 `is_deleted`，会把已禁用共享当有效权限返回，**造成权限放大** |
| 4 | `EvalSymlinks` + 路径字符串作为安全句柄 | v2, v3 | **不能消除 TOCTOU**：检查与使用之间，父目录或软链仍可被 SMB/其他进程替换；`O_NOFOLLOW` 只保护最终分量 |
| 5 | 固定 `/var/lib/toolfs` 作 StateDir | v2, v3 | 通常与 `/DATA`、外接盘不同文件系统，`os.Rename` 返回 `EXDEV`，保护链直接失效 |
| 6 | state 目录放在 mount 内部 | v1 | copy 型快照第 N 次会把第 N-1 次整个拷进去（指数膨胀）；`filepath.Walk` 会把回收站读进内存 |
| 7 | `MemoryBackend` 作为生产降级路径 | v1 | `FileSnapshot.Content []byte` 把每个文件完整内容常驻内存，真实数据量下必然 OOM |
| 8 | `SnapshotOnDeleteDirs` 默认 true | v1 | btrfs 不可用时，agent 删一个目录就触发整挂载（可能 TB 级）全量拷贝 |
| 9 | 由 FUSE 强制 `OpExecute` | v1–v3 | FUSE 无法阻止进程先读脚本再 `bash script.sh`。执行能力只能在 ToolFS 自己的 command/skill API 里控制 |
| 10 | 「两种路径别名任一命中即放行」 | v3 | 会把冲突数据合并成更高权限。v4 改为规范化后单次匹配 + 只读优先 |
| 11 | copy 型快照作为非 btrfs 降级 | v1, v2 | O(数据量) 全量拷贝，TB 级 `/DATA` 上又慢又占空间，且拷贝期间数据仍在变化、拿不到一致时间点。它要解决的误删场景已被 O(1) 的 versions + trash 覆盖。v5 改为明确 `ErrSnapshotUnsupported` |
| 12 | 普通用户可执行整卷回滚 | v2 | 快照粒度是 subvolume，RBAC 粒度是 per-user 路径前缀。一个用户的回滚会覆盖所有人的数据。v5 拆成「快照=系统级只读」+「闪回=用户级受 Grant 约束写回」 |
| 13 | 按快照创建时的授权判定恢复权限 | （曾评估） | 撤权后仍能通过旧快照取回数据，撤权形同虚设。必须用**当前** Grant 判定 |
| 14 | 靠遍历代码「显式跳过」`.toolfs-state` | v4 | 依赖每一处 walker 都记得排除。v5 改为把 `.toolfs-state` 建成独立 subvolume——btrfs 快照不递归嵌套 subvolume，是文件系统层面的保证 |
| 15 | 用单一通用机制覆盖所有文件系统（最低公共分母） | v1, v2 | btrfs 上会把 O(1) 的 reflink 降级成 O(size) 拷贝，并永远拿不到卷级时间点快照，只为省一个 backend。v5 改为分层：语义统一、能力显式、实现适配 |
| 16 | 在 ext4 上用硬链接硬造快照来「对齐能力」 | （曾评估） | 硬链接共享 inode，多访问通路（SMB/Docker/其他服务）下的原地写入会污染快照，得到一个**会静默失效的假快照**。宁可如实声明该存储无时间点快照 |
| 17 | 用 overlayfs 在 ext4 上提供 CoW | （曾评估） | 改变数据可见位置：SMB/Web/Docker 看到 lower（旧数据）、agent 看到合并视图，**视图分裂**，违背 ZimaOS 多通路共享同一份数据的核心场景 |
| 18 | `toolfs run -- codex "任务"` 包装命令行 | v6 | 源码显示 codex core 内嵌于常驻 app-server 进程，thread 为进程内对象——**不存在可包装的 per-task 进程边界** |
| 19 | 「容器 privileged，agent 可挂载宿主磁盘绕过」 | v5, v6 | 实测 entrypoint 用 `setpriv --reuid=seluser` 降权，agent 进程 `CapEff: 0` —— **privileged 未传导到 agent**，只要 toolfsd 以 root 运行，保护本就成立 |
| 20 | 用 `codex exec` 每次调用替代 app-server | （曾评估） | 会丢失审批交互、MCP OAuth、`ThreadFork`、动态工具等能力，且每轮付进程冷启动成本。多实例 app-server 无此短板 |
| 21 | 「共享 privileged 容器内无法区分请求归属，故须拆期」 | v5 | 该判断假设身份须从 FUSE caller UID **推断**。`toolfs run --token` 显式传入身份后，挂载点即绑定了授权范围，无需推断——v6 据此恢复完整 RBAC；v7 改为由 per-user 实例承接同一机制 |
| 22 | 版本保护不设排除规则 | v1–v5 | codex 会驱动 `npm install`、`git`：`node_modules` 数万文件各存一份版本，在仅剩 31G 的 ext4 `/DATA` 上一次任务即打满。`.git/` 更是本就有完整历史 |
| 23 | admin 在文件访问层享有特权放行 | v1–v4 | ZimaOS 自身分裂：**SMB 数据驱动**（`smb.go` 无 `admin`/`role`，纯按 share 表生成 per-user conf）、**Web UI 特权驱动**（`shares.go:618` 第一行 `if role=="admin" return true`）。v5 选 SMB 侧：admin 的 share 行本就由系统自动写入（`message.go:139`），按表构建实际可见范围几乎不变，却让权限变成显式可审计、可收窄，且 root daemon 少一条绕过全部检查的代码路径 |
| 24 | 「挂载先于容器建立，故无需挂载传播」 | v6, v7 | 只考虑了启动时序，忽略 toolfsd 重启。实测：bind 源若是 FUSE 挂载点本身，重挂后容器**静默读到旧挂载数据**，撤权失效且无错误信号。改为 bind 挂载点父级普通目录 + `rslave`（§6.6）|
| 25 | 让不同用户的挂载点互相可见以支持共享目录协作 | （曾评估） | 概念混淆：挂载传播传的是挂载事件而非文件内容。passthrough 下同一份数据本就双向可见（实测 `rprivate` 亦成立），互相可见只会破坏视图隔离而毫无收益（§6.5）|

---

## 4. 跨版本稳定的实证结论

以下为在真实代码上写测试**实际复现**的问题（非推测），是 P0 阶段存在的依据。它们从 v2 起贯穿至今：

| 编号 | 问题 | 证据 |
|---|---|---|
| S1 | 路径穿越绕过访问控制 | `IsPathAllowed("/toolfs/data/../secret.txt") = true`，读到 mount 外文件，审计还记 `success:true` |
| S2 | 符号链接逃逸 | mount 内软链指向外部文件，`os.ReadFile` 直接跟随 |
| S3 | 只读挂载被绕过写入 | 从可写 mount 经 `../` 写入 `ReadOnly=true` mount 的本地目录 |
| S4 | 并发数据竞态 | `go test -race` 实测：`fs.mounts` 写（`toolfs.go:430`）vs 读（`toolfs.go:705`） |
| S5 | sandbox 全局 stdout 竞态 | `sandbox.go:173` 改 `os.Stdout` 与审计 `fmt.Println` 冲突（v4 补充） |
| S6 | mount 前缀边界错误 | `strings.HasPrefix("/toolfs/database", "/toolfs/data") == true`（v4 补充） |

**根因**：`normalizeVirtualPath`（`toolfs.go:346`）只做字符规范化、不清理 `..`，而 `resolvePath` 用 `filepath.Join`（会 clean）——**判定与执行用了两个不同的路径值**。

---

## 5. 贯穿各版本的不变结论

无论方案怎么改，以下事实来自对 ZimaOS 全栈的调查，不随版本变化：

- **ZimaOS 文件权限的唯一事实来源**是 `/var/lib/casaos/files.db` 的 `shares_model_dbs` 表，每行 `(samba_user, path, read_only, is_deleted)`。
- **UserService 里没有权限数据**，只有 `role` 字段；它的权限 API 只是转发给 IceWhale-Files 的代理。
- **ZimaOS 对 admin 的处理本身就分裂**：SMB 数据驱动（`smb.go` 全文无 `admin`/`role`），Web UI 特权驱动（`shares.go:618`）。admin 的 share 行由 `message.go:139` 在 samba 用户创建时自动写入——**仅当该存储 `len(shares)==0`**，已有 share 的存储不会再自动加 admin。
- **POSIX 权限完全不参与**：`force user = root` + `0777` + `nt acl support = no`，全仓库无 `chown`/`setfacl`。
- **新用户默认权限为零**，必须管理员显式授权。
- **不存在 per-user home 目录**，隔离 100% 依赖 share 表的路径白名单。
- **btrfs 快照是绿地**：磁盘用 `mkfs.btrfs` 格式化，但 `subvolume`/`snapshot` 代码零命中。
- **但主存储 `/DATA` 是 ext4，不是 btrfs**（真机实测，见下）。`mkfs.btrfs` 只作用于数据盘，系统盘由 OS 镜像层格式化为 ext4。
- **`MountToolFSWithSession` 是空壳**（`fuse_adapter.go:326`），当前 FUSE 路径完全绕过访问控制。
- **ZimaOS 网关（Caddy）不做鉴权**，仅反向代理；各应用自行验签（用 UserService 的 JWKS 公钥）。
- **agent 与 ZimaOS 账号体系完全独立**，且 agent web 当前在所有网络接口上**无鉴权暴露**——后者独立于本方案存在，建议优先修复。

---

## 6. 真机实测结论（ZimaCube Pro / ZimaOS v1.7.0 / 10.0.0.75）

详见 v7 §2.5。九项结论中，**第 1 项推翻了 v5 的核心假设**：

| # | 结论 | 影响 |
|---|---|---|
| 1 | **`/DATA` 是 ext4，btrfs 只在数据盘** | 用户主数据**没有快照能力**，只有 versions+trash 兜底。快照从「全局能力」降级为「按存储可选增强」 |
| 2 | **ext4 不支持 reflink** | `/DATA` 上 versions 是 O(size) 全量拷贝，而该盘已用 87%、剩 31G。`MaxVersionFileSize` 需按剩余空间动态定 |
| 3 | btrfs 盘挂在顶层子卷、无嵌套子卷 | `.toolfs-state` 建独立子卷的方案在数据盘上可行 |
| 4 | **admin(`casaos`) 的 3 条 share 行 = `casa.casaos.conf` 的 3 个 section** | 「数据驱动 admin 视图」实证成立，改后可见范围不变 |
| 5 | **真实存在父 rw / 子 ro 的 Grant**（`HDD-Storage` rw + `HDD-Storage/rauc/temp` ro） | 最长前缀+子目录收窄不是假想场景。且暴露 SMB 缺陷：子目录只读可被父共享绕过，ToolFS 比 SMB 严格 |
| 6 | **14 行 share 中 8 行 `is_deleted=1`** | 实证「用 `/share/permission` 会造成权限放大」，§5.1 改用 `/share`+Status 过滤的决策成立 |
| 7 | `/media/ZimaOS-HD` 是指向 `/DATA` 的符号链接，且表里存的是带尾斜杠的 `/media/ZimaOS-HD/` | 路径规范化要同时处理尾斜杠、别名、以及别名本身是软链 |
| 8 | **挂载传播**：内容互见与传播标志无关；但 bind 源若是 FUSE 挂载点本身，toolfsd 重挂后容器**静默读到旧数据** | 确立「bind 源取父目录 + `rslave`」；ZimaOS 新建挂载默认继承 `shared`，无需 `--make-rshared` |
| 9 | **跨挂载可见性**（最小 FUSE passthrough 实测）：两个独立挂载（不同 superblock）指向同一后端，TTL=0 下双向即时可见；TTL=30s 时 **negative entry 陈旧恰好 30s** | 共享目录并发无需额外机制；放开缓存的前提是跨挂载失效，且 `NegativeTimeout` 须单独保持 0 |

## 7. 待确认的开放问题

| # | 问题 | 阻塞性质 |
|---|---|---|
| 1 | **账号绑定方案**：短期管理员映射表 / 目标 ZimaOS 轻量授权流程 | ⚠️ per-user 隔离前提（**不阻塞 ToolFS 开工**） |
| 2 | Web 服务实例池改造（40 处调用点 / 12 文件）由谁承担、何时排期 | ⚠️ 落地时间 |
| 3 | 实例数上限与空闲回收策略 | 资源约束 |
| 4 | 过渡期是否接受「单一专用账号 + 完整误删保护」先行落地 | 决定能否立即开工 |
| 5 | IceWhale-Files 需提供修复后的逐用户 Grant 接口 | 联调阻塞 |
| 6 | 「主数据只有版本与回收站、无时间点快照」是否可接受 | 产品决策 |
| 7 | **agent web 无鉴权暴露何时修复**（独立于本方案） | ⚠️ 严重性不低于本方案要解决的问题 |

---

## 8. 阅读建议

- **要实施** → 只读 [`design-rbac-snapshot.md`](design-rbac-snapshot.md)（v7，现行）。
- **想知道某个设计为什么是现在这样** → 看本文件第 2、3 节。
- **快照与闪回** → v7 的 §7.4–7.7 已是完整设计，不需要再回头翻 v2。v2 第 6 章仅在需要了解「copy 型后端为什么被否决」时有参考价值。
