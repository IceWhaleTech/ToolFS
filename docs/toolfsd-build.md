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
| 挂载根目录 | `/mnt/toolfs` |
| control socket | `/run/toolfs/toolfsd.sock`，权限 `0600` |
| ZimaOS 服务地址目录 | `/var/run/casaos` |
| 权限缓存 | `/var/lib/casaos/toolfs/permission-cache.json` |
| 可选运行配置 | `/etc/casaos/zimaos-toolfsd.env` |

配置样例随包提供，升级不会覆盖管理员的运行配置。服务保持宿主机挂载命名空间，
让 Agent Management 能将挂载的父目录以 `rslave` 绑定进实例容器。
`/run/toolfs` 在服务停止或重启时保留，避免删除 Agent Management 在同一目录下管理的实例 socket。
Agent Management 提交已验证的 ZimaOS 身份时，无需额外的账号绑定文件；未提供身份且未配置绑定的请求会被拒绝。

## 本地验证

先为私有 Go 依赖配置 Git 读取凭据，并安装 FUSE、Go 1.25、GoReleaser v2、curl 和 binutils：

```bash
export GOPRIVATE='github.com/IceWhaleTech/*'
go vet ./...
go test -race -timeout 10m ./...
goreleaser check
goreleaser release --snapshot --clean
bash scripts/smoke-toolfsd.sh dist
```

包检查会验证两个架构、静态链接和校验和，并启动本机架构的 daemon，检查
control API、socket 权限、未绑定账号拒绝和正常退出后的 socket 清理。
