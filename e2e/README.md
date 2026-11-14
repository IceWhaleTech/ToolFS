# ToolFS Agent 权限回归

独立 Go/WASI module，不改变产品 Go 版本或依赖。`AGENT-FS-01` 在平台一次性 ZimaOS VM
上运行当前 checkout 编译的测试二进制：HTTP/SSE 权威 fixture + 真实 FUSE，覆盖默认无权限、
A/B 增授、逐项撤销、最后撤销、owner 上限和旧句柄。只使用临时目录，不替换系统服务。

这是 **组件回归 + 真实 FUSE，不是两服务端到端联调**：权限端使用测试 HTTP/SSE fixture，
不会请求 VM 系统自带或另行部署的 UserService；FUSE 内核挂载是真实的。

```sh
GOPRIVATE='github.com/IceWhaleTech/*' bash e2e/build-artifacts.sh --out /tmp/toolfs-e2e-bin
cd e2e
GOPRIVATE='github.com/IceWhaleTech/*' GOOS=wasip1 GOARCH=wasm go vet ./...
# 用 zimaos-e2e/scripts/build-plugin.sh ./plugin /tmp/toolfs.wasm 构建插件。
```

平台加载此插件时，`E2E_BIN_TESTS` 指向 `tests/toolfs-agent-authorization.test`。
运行 `toolfs` suite 的 `AGENT-FS-01`；用例必须实际执行真实 FUSE，SKIP 不视为通过。
上传/运行使用平台现有身份与一次性测试资源，不能用生产设备或真实用户目录代替。

场景逐一验证普通读取和零拷贝读取两种真实挂载模式，权限变化后 mount handle、
`InstanceView`、bind source 和 mount point 均保持不变。独立旧文件句柄测试也同时运行，
避免只依赖路径重新查找来判定撤权。卸载诊断、错误链、失败留账、非递归目录清理、
完成卸载后只重试目录清理，以及清理中的挂载不再被重投影等测试一并纳入必测名单。
每项必测用例（包括两种 FUSE 子用例）必须连续三次 PASS；不能因父测试 PASS 而忽略
子用例 SKIP。产物脚本已编译完整根测试包，无需额外变更构建筛选。

删除回归以默认 `AllowDelete=false` 建立具名 authority Agent 挂载，删除仅按有效
read/write/delete 判定；`AllowDelete=true` 不得弥补缺少的关系。只撤 delete 时同一挂载
仍可读写，unlink/rmdir、跨目录移动和覆盖被拒绝，同目录不覆盖改名保持原语义；清空后
旧读写句柄的后续 IO 也拒绝。static/direct-user 保留原 capability 保护，拒绝统一为
`EACCES`。provider、真实 mutation callback、兼容模式和真实 FUSE 测试均列入必测，
真实 FUSE 缺失不能报告通过。callback 测试直接执行删除与 rename handler，不能代替内核挂载。
