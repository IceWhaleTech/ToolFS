# zimaos-toolfsd 构建与安装

构建配置位于 `feat/authority-permissions` 分支。分支推送会运行
`.github/workflows/toolfsd.yml`，使用 `go.mod` 声明的 Go 版本，先运行 `go vet`
和竞态测试，再用 GoReleaser 从 `cmd/toolfsd` 构建 Linux amd64、arm64 的
`zimaos-toolfsd`。程序名称、systemd 单元、配置样例和许可证安装位置都由本仓库
定义，系统构建直接复制发布包中的 `build/sysroot/`。
CI 安装 FUSE 并要求真实挂载和 control API 的关键测试通过，避免环境缺少 FUSE 时
测试被跳过而仍显示成功。

## GitHub Actions 配置

为仓库配置以下 Actions Secrets：

| Secret | 用途 |
| --- | --- |
| `API_TOKEN_GITHUB` | 只需读取私有依赖 `IceWhaleTech/ZimaOS-Common` 的权限 |
| `OSS_KEY_ID` | 阿里云 OSS AccessKey ID |
| `OSS_KEY_SECRET` | 阿里云 OSS AccessKey Secret |

构建产物上传到 `casaos` 桶，工作流 Summary 提供下载链接：

```text
https://casaos.oss-cn-shanghai.aliyuncs.com/IceWhaleTech/ToolFS/actions/<run_id>/toolfsd/
  linux-amd64-zimaos-toolfsd-v<version>.tar.gz
  linux-arm64-zimaos-toolfsd-v<version>.tar.gz
  checksums.txt
```

分支构建的版本格式为 `<最近版本>-snapshot.<commit>`；仓库没有标签时以 `0.0.0`
作为版本基数。以后在包含该 workflow 的提交上推送 `v*.*.*` 标签，会使用标签版本，
上传到 `IceWhaleTech/ToolFS/releases/download/<tag>/`。工作流不创建 GitHub Release，
也不合并分支。`workflow_dispatch` 为后续手动构建预留；新 workflow 尚未进入默认分支时，
GitHub 可能不显示手动运行入口，当前使用分支推送触发即可。

## 包内布局

与 ZimaOS 其他服务一样，压缩包包含可直接复制到目标根文件系统的 `build/sysroot/`：

```text
build/sysroot/
├── etc/casaos/zimaos-toolfsd.env.sample
└── usr/
    ├── bin/zimaos-toolfsd
    ├── lib/
    │   ├── modules-load.d/zimaos-toolfsd.conf
    │   └── systemd/system/zimaos-toolfsd.service
    └── share/licenses/zimaos-toolfsd/LICENSE
```

二进制使用 `CGO_ENABLED=0`，无需安装 libfuse 开发库或动态运行库；目标系统需要
内核 FUSE、`/dev/fuse` 和 `fusermount3`（或 `fusermount`）。

安装后运行 `systemctl enable --now zimaos-toolfsd.service`。ZimaOS 系统构建会统一
执行 `systemctl preset-all`，通过单元的 `WantedBy=multi-user.target` 启用服务。
该服务使用：

| 配置 | 默认值 |
| --- | --- |
| 挂载根目录 | `/run/toolfs/mounts` |
| control socket | `/run/toolfs/toolfsd.sock`，权限 `0600` |
| ZimaOS 服务地址目录 | `/var/run/casaos` |
| 权限缓存 | `/var/lib/casaos/toolfs/permission-cache.json` |
| 可选运行配置 | `/etc/casaos/zimaos-toolfsd.env` |

配置样例随包提供，升级不会覆盖管理员的运行配置。服务保持宿主机挂载命名空间，
让 Agent Management 能将挂载的父目录以 `rslave` 绑定进实例容器。
挂载根位于可写的 `/run`，避免在只读系统镜像的 `/mnt` 下创建目录。
`/run/toolfs` 在服务停止或重启时保留，避免删除 Agent Management 在同一目录下管理的实例 socket。
Agent Management 提交已验证的 ZimaOS 身份时，无需额外的账号绑定文件；未提供身份且未配置绑定的请求会被拒绝。

## 运行中的存储视图

每个用户/app 的实际布局为 `/run/toolfs/mounts/u<uid>/<app>/workspace`。
`<app>` 是稳定的普通 bind source，`workspace` 才是 FUSE；app 为空时省略这一层。
`/var/run` 在常规 ZimaOS 系统中链接到 `/run`，以服务的 `-root` 实际配置为准。

权限来自 UserService 选定主体的 snapshot；导航范围来自同主体、同 generation 的
`GET /v2/permissions/entries`，没有管理员绕过，也不在 ToolFS 扫描 `/media`。
Home 唯一入口映射到 `workspace/Home/<用户名>/...`，storage 只给出盘根，深层分享
按盘根逐层导航。只导航祖先不枚举真实目录，不赋予写删权限。
`/var/log` 等盘外 grant 不出现在用户视图。

权限事件立即重投影；5 秒周期重新读取 entries，在 generation 不变时刷新增删盘。
未变 entry、根句柄与 inode 复用；同名后端换对象会退休旧 entry。metadata 失败保留
最后完整结构以便恢复，文件和导航名称仍按实时权限过滤，不退回本地扫描。
标准根 alias 仅根据 authority 的等价声明选择 backing，数据 symlink 不跟随。
容器启动时建立的顶层便捷 symlink 不由该循环更新，新盘以 workspace 路径为准。

必须先部署支持 entries 的 UserService，再部署 ToolFS；旧 UserService 缺该 API 时
初次挂载失败。`default_path` 给 UI 默认进入本人 Home 并返回集合使用，本轮不改
Agent Management 容器工作目录，Files/UI 也未切换。
本地已实现两端 API 契约、投影与 fd-relative no-follow 后端；根 `..`、并发替换、
clone 固定 fd 降级等边界有测试覆盖。硬链接身份隔离不在保证范围内。
**Linux 真实 FUSE 挂载及设备联调尚未验收；本地测试与交叉构建通过不代表可直接
发布。Files/UI 尚未切换。**

## Agent 删除授权与热生效

具名 authority Agent 使用 `agent:<uid>:<app>`，删除要求 UserService 对目标路径的有效
`read`、`write`、`delete` 同时成立。不再受 `-allow-delete` 或请求 `allow_delete` 的二次
门禁影响；这些字段仅保留 static / 空 app direct-user 的兼容语义，不能为 Agent 补出
不存在的 delete。仅读写不可删除，owner 上限、显式 deny、DenyPatterns、保留路径和
回收站/版本保护不变。authority 缓存未知时拒绝，不回退用户授权。

unlink/rmdir 及破坏性 rename 统一检查 `OpDelete`。同目录不覆盖改名保持原写权限语义，
跨目录移动需源 delete，覆盖需目标 delete，跨 grant 仍返回 `EXDEV`。原来被 capability
提前拒绝的操作现在统一返回 `EACCES`，只读等其他错误码不变。

授权和撤权沿现有 SSE、rewarm、原地 Reproject 和实时 generation 重新判权，不修改运行中
的 Caps，不调用 Ensure/Release、不卸载重挂。只撤 delete 后读写保留，清空授权后旧句柄的
后续 IO 也拒绝。此保证依赖默认零内核缓存；保存成功不是全部消费者的同步确认屏障，
也不追溯取消已经完成或进行中的 IO。

首次升级仍需独立安排：旧进程没有 FUSE 热接管，替换磁盘二进制不会改变运行中的程序；
停止旧 daemon 会影响挂载，AM 可能回收缺少挂载的容器。日常授权无需重启容器不代表首次
部署也能保持旧容器零中断。不得绕过 reaper 或使用 force/lazy unmount 掩盖失效挂载。
本地代码和 fixture 通过不代表新实现已部署到设备，也不能代替真实 UserService/AM/容器验收。

## 本地验证

先为私有 Go 依赖配置 Git 读取凭据，并安装 FUSE、Go 1.25、GoReleaser v2、curl 和 binutils：

```bash
export GOPRIVATE='github.com/IceWhaleTech/*'
go vet ./...
go test -race -timeout 10m ./...
goreleaser check
goreleaser release --snapshot --clean
bash scripts/smoke-toolfsd.sh dist
sudo bash scripts/smoke-toolfsd-readonly.sh dist
```

包检查会验证两个架构、静态链接和校验和，并启动本机架构的 daemon，检查
control API、socket 权限、未绑定账号拒绝和正常退出后的 socket 清理。
CI 还会在独立挂载命名空间内将 `/mnt` 设为只读、将 `/run` 设为可写，
不传 `-root` 启动 daemon，验证真实默认路径适用于只读系统镜像。

## 卸载失败诊断与重试

`DELETE /v1/mounts/{agentUserID}?app={app}` 成功仍返回空 `204`；不存在仍返回
`404` 和原有 `error`，不附加诊断字段。失败返回 `500`，保留原有 `error` 文本并
增加可选 `stage`、`code`，旧客户端可以忽略这两个字段：

- `stage`：`fuse-unmount` 或 `directory-cleanup`，分别表示内核挂载卸载和卸载后的
  空目录清理阶段。
- `code`：`busy`、`permission-denied`、`not-empty`、`invalid-layout` 或 `unknown`。
  分类来自错误链中的 errno 或明确的目录布局错误类型；错误文字含有 `busy` 等词
  不构成分类依据。

control socket 仅限 root 访问，兼容保留的 `error` 可能含宿主路径，不能直接转发到
公开 API 或日志。Agent Management 只消费白名单 `stage/code` 和 HTTP status，
不回显或记录上游原文；ToolFS 的 Go 错误链仍支持 `errors.Is` / `errors.As`。

FUSE 卸载失败时保留挂载和根句柄；FUSE 已卸载但目录清理失败时保留清理账本，
后续重试不重复卸载。目录仍只做非递归删除，拒绝符号链接、布局不匹配和意外内容，
不使用 force/lazy unmount，不删除其他 app 或持久数据。权限事件继续在原挂载和
原 view 上重投影，不要求回收容器来应用授权。

`e2e/` 的 `AGENT-FS-01` 同时覆盖卸载诊断/重试，以及普通读取与零拷贝读取模式下的
同挂载授权生命周期和旧句柄撤权。必测项均须连续三次通过，任一 FUSE SKIP 都视为
场景失败。本机缺少 FUSE 时单元测试通过不能替代 Linux 平台验收。这些诊断用于定位
后续失败，不代表已确定或修复 248 历史卸载 500 的底层原因。
