//go:build wasip1

package main

import (
	"strings"

	"github.com/IceWhaleTech/zimaos-e2e/pkg/plugin/sdk"
	"github.com/IceWhaleTech/zimaos-e2e/pkg/plugin/sdk/machine"
)

var (
	version = "0.0.0-dev"
	repo    string
	ref     string
	commit  string
)

func main() {}

func init() {
	sdk.Suite("toolfs", version)
	sdk.Describe("ToolFS Agent 默认拒绝、授权/撤权事件与真实 FUSE 生命周期回归", sdk.Target{Repo: repo, Ref: ref, Commit: commit})
	sdk.Artifacts("tests")
	sdk.Requires(sdk.Requirement{Labels: map[string]string{"os": "zimaos"}})
	sdk.Add(&sdk.Scenario{
		ID: "AGENT-FS-01", Title: "Agent 默认空权限与逐目录授权撤销", Priority: "P0", Tags: []string{"agent", "permission", "fuse"},
		Desc:   "部署当前构建的 ToolFS 测试二进制到一次性测试机的 /tmp，不替换系统服务\n运行 HTTP/SSE fixture，验证无授权 Agent 不使用用户权限，A/B 增授和全部撤销后为空\n分别以普通读取和零拷贝读取挂载真实 FUSE，验证同一 mount/view 下空→A→A+B→B→空，撤权后的旧文件/目录句柄拒绝访问\n以默认 capability 建立真实挂载，增加并仅撤销 delete，验证普通写入保留、删除/跨目录移动/覆盖拒绝；清空后旧读写句柄也拒绝\n运行卸载故障 fixture，核对阶段/errno 分类、失败留账、目录安全和重试不重复卸载\n连续运行三次，逐一核对全部必测项及两种真实挂载模式均 PASS，缺少 FUSE 或任一 SKIP 不能通过",
		Expect: []string{"无声明及 owner-only Agent 没有用户数据入口", "授权仅限确认目录，owner 是实时上限", "全部撤销后为空，旧句柄不能读取", "系统服务和真实用户数据不变"},
		Run: func(t *sdk.T) {
			const binary = "/tmp/toolfs-agent-authorization.test"
			t.Must(sdk.Call("file.put_artifact", map[string]any{"key": "E2E_BIN_TESTS", "remote": binary}, nil), "deploy test binary")
			m := machine.On(t)
			m.Run("test -c /dev/fuse && (command -v fusermount3 || command -v fusermount)")
			m.Run("chmod 700 " + binary)
			required := []string{
				"TestAgentDefaultsToNoDirectoryAccess",
				"TestAgentAuthorizationLifecycle",
				"TestAgentDeleteRequiresExplicitRelationAndRevokesLive",
				"TestAgentDeleteHonorsEffectiveRelations",
				"TestDeleteHandlersUseEffectiveAuthorization",
				"TestDeleteHandlersRecheckLiveRelations",
				"TestAuthorityProjectionAndDecisions",
				"TestCapabilitiesCannotPromoteReadOnlyGrant",
				"TestMutate_DeleteIsWithheldByDefault",
				"TestMutate_DeleteGoesToTrashWhenPermitted",
				"TestMountAgentDeleteRevocation",
				"TestAgentOwnerIsOnlyACeiling",
				"TestAgentAuthorityFailuresNeverSelectUser",
				"TestAgentRevocationFailsClosedWhenRewarmingFails",
				"TestAgentSubjectsDoNotShareUserOrOtherAppPermissions",
				"TestAgentWithoutEffectiveReadHasNoDirectoryEntries",
				"TestMountAgentAuthorizationLifecycle",
				"TestOpenFileHandleRechecksReadAfterRevocation",
				"TestControl_UnmountDiagnostics",
				"TestControl_UnmountDirectoryDiagnostics",
				"TestControl_UnmountInvalidLayout",
				"TestUnmountDiagnosticsPreservesErrorChain",
				"TestUnmountDiagnosticsAllowsOnlyKnownFields",
				"TestUnmountFailureKeepsMountForRetry",
				"TestUnmountRemovesOnlyItsEmptyDirectories",
				"TestUnmountDoesNotRecursivelyRemoveUnexpectedFiles",
				"TestUnmountCloseCleansDirectories",
				"TestUnmountRefusesSymlinkedInstanceDirectory",
				"TestMountHandleRetainsRootsUntilUnmountSucceeds",
				"TestReprojectSkipsUnmountedCleanupPendingMount",
				"TestStorageReprojectDoesNotReviveUnmountedInstance",
			}
			result := m.Root(binary + " -test.v -test.count=3 -test.timeout=120s -test.run='^(" + strings.Join(required, "|") + ")$'")
			log := result.Stdout + result.Stderr
			t.Artifact("tests.log", []byte(log))
			t.Eq(result.Exit, 0, "test exit")
			t.True(!strings.Contains(log, "--- SKIP:"), "required tests must not skip")
			for _, name := range append(required,
				"TestMountAgentAuthorizationLifecycle/copy",
				"TestMountAgentAuthorizationLifecycle/zero-copy",
				"TestOpenFileHandleRechecksReadAfterRevocation/copy",
				"TestOpenFileHandleRechecksReadAfterRevocation/zero-copy",
			) {
				t.Eq(strings.Count(log, "--- PASS: "+name+" "), 3, name+" must pass all three runs")
			}
		},
	})
}
