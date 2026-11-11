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
