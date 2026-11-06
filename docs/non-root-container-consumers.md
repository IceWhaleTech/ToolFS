---
title: 非 root 容器消费 ToolFS
status: proposed
owner: Kevin
updated: 2026-11-06
---

# 非 root 容器消费 ToolFS

本记录补充 [RBAC 挂载边界](design-rbac-snapshot.md#63-挂载的访问边界)。结果责任人为 Kevin，审核和发布决定记录在对应 Draft PR。

## 执行身份与授权身份

agentd 为修复 Manual bwrap，将 app-server 改为零 capabilities 的 Unix UID/GID `65534:65534`。ZimaOS 账号 ID 仍用于授权和挂载布局，不作为 Unix UID。

toolfsd 的 `-container-uid` 默认值为 `0`，保留库和直接调用的 root-only 行为。打包的 systemd 服务设置 `TOOLFSD_CONTAINER_UID=65534`，并将值返回为挂载响应的 `container_uid`。切换服务配置前需要停止旧实例；旧消费者可继续使用 `container_uid=0` 配置。新 agentd 拒绝未声明兼容执行 UID 的挂载。

非零配置启用 `allow_other`，因为 FUSE 挂载者是宿主 root。挂载属性使用指定 UID/GID，并启用 `default_permissions`，让可写授权目录和文件的 owner 权限适用于非 root 进程。它不增加 Grant，也不放宽删除、版本保护和恢复控制。

`allow_other` 本身不区分账号。隔离仍依靠 root 私有的宿主路径和每个容器接收的独立 bind。MountSet 拒绝非 root 所有或对其他用户开放的挂载根；挂载时再次检查私有祖先及中间目录，拒绝符号链接和可被非 root 修改的路径。稳定父目录以 `rslave` 绑定，不绑定 FUSE 挂载点本身。

服务的 `UMask=0077` 会把新建的 bind 父目录收紧为 `0700`。非 root 消费者需要遍历该导出目录，因此 ToolFS 在 root 私有祖先内，将 root 所有的 bind 父目录明确设为 `0755`；挂载根仍须为 `0700`，不修改 Wiki 或其他宿主数据的权限。目录通过不跟随符号链接的文件描述符校验和修改；挂载失败时通过同一描述符恢复原权限。

## 当前目录的身份

零 TTL 不意味着每次 LOOKUP 都创建新 inode。反复替换同一目录的 inode 会使已持有的 cwd 失效，Linux `getcwd()` 随后返回 ENOENT，Codex 因此无法探测 bwrap。

LOOKUP 继续检查当前授权及宿主目录身份。仅当 Grant entry、相对路径和 backing inode 都相同时，才复用 go-fuse 当前持有的目录节点。新目录通过同一入口发布，发布过程串行化，避免并发 LOOKUP 返回不同身份。缓存随 go-fuse 的引用生命周期释放；授权、条目和属性 TTL 保持为零。

## 验证与升级

先停止旧实例和 agentd，再发布 ToolFS 和依赖 `container_uid` 的 agentd。已有公开挂载根需要由运维确认为私有目录；服务不会静默更改挂载根的权限。

`go test -race ./...` 覆盖普通回归。Linux root 环境下的 `TestMountWorkingDirectorySurvivesRepeatedLookups` 通过真实 FUSE 挂载重现并验证 cwd；未修复版本在第一次重复查询后返回 ENOENT。私有路径测试同时验证公开目录、可写中间目录和符号链接被拒绝。独立子进程以 `umask 0077` 验证新建和已有 bind 目录为 `0755`、私有祖先保持 `0700`，以及挂载失败后恢复已有目录权限。

10.0.1.36 的临时容器集成验证覆盖非 root 读取、受保护的授权写入、删除默认关闭、Manual 禁止写入和删除、宿主路径不可遍历、授权撤销和实例回收。所有数据均为临时文件，生产 daemon 和 Wiki 文件未改动。amd64 已实测，arm64 进行编译验证。
