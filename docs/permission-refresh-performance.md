---
title: 权限刷新修复性能验证
status: active
updated: 2026-11-21
---

# 权限刷新修复性能验证

2026-11-21，Go 1.27.4、Linux amd64、AMD EPYC 9275F，GOMAXPROCS=4，
每场景5次、每次250ms，取中位数。旧版代码为 v0.1.10，使用相同基准代码通过 Go overlay
测量；本轮使用包含强制刷新修复的 Common。两次测试按顺序运行。

| 场景 | v0.1.10 | 本轮 | HTTP | 分配 |
| --- | ---: | ---: | ---: | ---: |
| 已热文件操作判断 | 31.67 ns/op | 31.85 ns/op | 0/op | 0 B/op，0 allocs/op |
| 全局事件、1个已服务主体重热 | 未测 | 85.784 µs/event | 1/event | 9475 B/event，118 allocs/event |
| 全局事件、8个已服务主体重热 | 未测 | 564.983 µs/event | 8/event | 73250 B/event，903 allocs/event |

文件操作缓存中位数约增加0.6%，仍无网络请求或堆分配。强刷移除与同版本恢复通过本地
CacheRevision 使该缓存失效，不增加逐文件远端查询。Common 普通快照缓存的独立对照为
13.13→12.89 ns/op（8个路径根）、13.11→12.89 ns/op（80个路径根），均零分配。

全局事件基准包含 SSE 传递、缓存失效及各主体快照重取，使用本机 HTTP 模拟服务；不包含
真实权限数据库、完整挂载视图重建或设备 FUSE 吞吐。它说明全量订阅的控制面开销随已服务
主体数增加，不能把0.565ms解释为真实设备的完整重投影耗时。已有视图差异复用由回归验证。

```sh
GOMAXPROCS=4 go test . -run '^$' \
  -bench 'Benchmark(LiveDecisionCache|GlobalGenerationRewarm)$' \
  -benchmem -benchtime=250ms -count=5
```

[原始数据](benchmarks/permission-refresh-20261121.txt)。本轮不改变全部 Common 消费者的订阅
默认值；ToolFS 全量订阅服务于当前全局版本配对和跨域授权依赖契约。

## 75 隔离设备验证

2026-11-21，将使用已固定 Common 提交构建的 Linux amd64 静态测试程序放到75的独立
/run 目录执行。8项顶层用例全部通过，无跳过：真实控制接口挂载/卸载、双用户隔离、
存储拓扑与 cwd 保持、应用授权生命周期（复制及零拷贝）、版本失配无事件恢复、同版本
强刷失败/恢复的判断缓存、全局事件订阅和身份/版本配对。

另启动隔离候选 daemon 对接75现有 UserService，用户1的 blue 挂载请求返回201。测试进程、
挂载及临时文件均已清理，没有修改用户真实授权。该步骤验证挂载链路，不等同于使用用户
Token 完成浏览器到容器的全链路验收。
