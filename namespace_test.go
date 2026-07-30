package toolfs

// P2 namespace tests.
//
// The headline assertion is the project's acceptance criterion:
// user 1 sees only the directories granted to user 1, user 2 sees only theirs.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// twoUserFixture lays out two users with disjoint grants plus a shared one.
func twoUserFixture(t *testing.T) (base string, resolver *StaticIdentityResolver, provider *StaticPermissionProvider) {
	t.Helper()
	base = t.TempDir()

	dirs := []string{"Documents", "Media", "Shared"}
	for _, d := range dirs {
		full := filepath.Join(base, d)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatalf("准备目录 %s: %v", d, err)
		}
		if err := os.WriteFile(filepath.Join(full, "file.txt"), []byte(d), 0o644); err != nil {
			t.Fatalf("写文件: %v", err)
		}
		// 任务开始前就存在的文件，供「删除后撤销」用例使用
		if err := os.WriteFile(filepath.Join(full, "victim.txt"), []byte("VICTIM"), 0o644); err != nil {
			t.Fatalf("写文件: %v", err)
		}
	}

	resolver = NewStaticIdentityResolver(map[string]ZimaIdentity{
		"agent-user-1": {Username: "alice", UserID: 1001, Role: "user"},
		"agent-user-2": {Username: "bob", UserID: 1002, Role: "user"},
	})
	provider = NewStaticPermissionProvider(map[string][]Grant{
		"alice": {
			{ID: 1, Name: "Documents", Path: filepath.Join(base, "Documents")},
			{ID: 3, Name: "Shared", Path: filepath.Join(base, "Shared")},
		},
		"bob": {
			{ID: 2, Name: "Media", Path: filepath.Join(base, "Media")},
			{ID: 3, Name: "Shared", Path: filepath.Join(base, "Shared")},
		},
	})
	return base, resolver, provider
}

// 验收标准：两个用户各自只看到自己被授权的目录。
func TestAcceptance_EachUserSeesOnlyGrantedEntries(t *testing.T) {
	_, resolver, provider := twoUserFixture(t)
	ctx := context.Background()

	aliceView, err := BuildInstanceViewForUser(ctx, resolver, provider, "agent-user-1", AgentCapabilities{})
	if err != nil {
		t.Fatalf("构建 alice 视图: %v", err)
	}
	bobView, err := BuildInstanceViewForUser(ctx, resolver, provider, "agent-user-2", AgentCapabilities{})
	if err != nil {
		t.Fatalf("构建 bob 视图: %v", err)
	}

	assertEntries(t, "alice", aliceView.Entries(), []string{"Documents", "Shared"})
	assertEntries(t, "bob", bobView.Entries(), []string{"Media", "Shared"})

	// 交叉访问：对方独有的目录在本视图中根本不存在
	if _, ok := aliceView.Entry("Media"); ok {
		t.Error("alice 的视图里出现了 bob 独有的 Media")
	}
	if _, ok := bobView.Entry("Documents"); ok {
		t.Error("bob 的视图里出现了 alice 独有的 Documents")
	}
}

func assertEntries(t *testing.T, who string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s 看到 %v，期望 %v", who, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s 看到 %v，期望 %v", who, got, want)
		}
	}
}

// 未绑定的 agent 账号必须是「无访问」，不能退化成「无限制」。
func TestUnboundAgentAccountIsDenied(t *testing.T) {
	_, resolver, provider := twoUserFixture(t)

	_, err := BuildInstanceViewForUser(context.Background(), resolver, provider, "agent-user-unknown", AgentCapabilities{})
	if err == nil {
		t.Fatal("未绑定账号仍构建出了视图")
	}
	if err != ErrIdentityNotBound {
		t.Errorf("返回错误为 %v，期望 ErrIdentityNotBound", err)
	}
}

// 过渡模式：全部实例绑定同一个专用账号，此时具备完整误删保护但无用户间隔离。
func TestFallbackIdentityServesTransitionMode(t *testing.T) {
	_, resolver, provider := twoUserFixture(t)
	resolver.SetFallback(&ZimaIdentity{Username: "alice", UserID: 1001, Role: "user"})

	view, err := BuildInstanceViewForUser(context.Background(), resolver, provider, "agent-user-unknown", AgentCapabilities{})
	if err != nil {
		t.Fatalf("过渡模式下构建视图失败: %v", err)
	}
	assertEntries(t, "fallback", view.Entries(), []string{"Documents", "Shared"})
}

// 虚拟后端不得因为持有文件 Grant 而自动出现在命名空间里。
func TestVirtualBackendsAreNotExposed(t *testing.T) {
	_, resolver, provider := twoUserFixture(t)

	view, err := BuildInstanceViewForUser(context.Background(), resolver, provider, "agent-user-1", AgentCapabilities{})
	if err != nil {
		t.Fatalf("构建视图: %v", err)
	}
	for _, forbidden := range []string{"memory", "rag", toolfsStateDir} {
		if _, ok := view.Entry(forbidden); ok {
			t.Errorf("命名空间里出现了 %q，RBAC 视图只应包含 Grant 派生的条目", forbidden)
		}
	}
}

// 命名冲突必须追加稳定 Grant ID，而不是静默覆盖。
func TestCollidingNamesGetStableSuffix(t *testing.T) {
	base := t.TempDir()
	for _, d := range []string{"a/Documents", "b/Documents"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatalf("准备目录: %v", err)
		}
	}

	view, err := BuildInstanceView(
		&ZimaIdentity{Username: "alice", UserID: 1001},
		NewAccessSnapshot("alice", 1001, "user", []Grant{
			{ID: 7, Name: "Documents", Path: filepath.Join(base, "a", "Documents")},
			{ID: 9, Name: "Documents", Path: filepath.Join(base, "b", "Documents")},
		}, 1),
		AgentCapabilities{},
	)
	if err != nil {
		t.Fatalf("构建视图: %v", err)
	}

	entries := view.Entries()
	if len(entries) != 2 {
		t.Fatalf("冲突命名后只剩 %d 个条目，发生了静默覆盖: %v", len(entries), entries)
	}
	if _, ok := view.Entry("Documents"); !ok {
		t.Error("缺少基础名 Documents")
	}
	if _, ok := view.Entry("Documents~9"); !ok {
		t.Errorf("缺少带 Grant ID 后缀的条目，实际为 %v", entries)
	}
}

// 嵌套收窄：父目录 rw、子目录 ro，写权限必须在子目录处被拒。
func TestViewAppliesNestedNarrowing(t *testing.T) {
	base := t.TempDir()
	child := filepath.Join(base, "storage", "rauc", "temp")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("准备目录: %v", err)
	}

	storage := filepath.Join(base, "storage")
	view, err := BuildInstanceView(
		&ZimaIdentity{Username: "alice", UserID: 1001},
		NewAccessSnapshot("alice", 1001, "user", []Grant{
			{ID: 1, Name: "storage", Path: storage, ReadOnly: false},
			{ID: 2, Name: "temp", Path: child, ReadOnly: true},
		}, 1),
		AgentCapabilities{},
	)
	if err != nil {
		t.Fatalf("构建视图: %v", err)
	}

	entry, ok := view.Entry("storage")
	if !ok {
		t.Fatal("缺少 storage 条目")
	}
	if !view.Allows(entry, "other/file", OpModify) {
		t.Error("父目录的写权限未生效")
	}
	if view.Allows(entry, "rauc/temp/file", OpModify) {
		t.Error("子目录的只读收窄未生效")
	}
	if !view.Allows(entry, "rauc/temp/file", OpRead) {
		t.Error("子目录应当仍可读")
	}
}

// DenyPatterns：非法 pattern 必须拒绝建挂载，匹配失败不得按 allow 处理。
func TestDenyPatterns(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "proj", ".ssh"), 0o755); err != nil {
		t.Fatalf("准备目录: %v", err)
	}

	snapshot := NewAccessSnapshot("alice", 1001, "user", []Grant{
		{ID: 1, Name: "proj", Path: filepath.Join(base, "proj")},
	}, 1)
	identity := &ZimaIdentity{Username: "alice", UserID: 1001}

	view, err := BuildInstanceView(identity, snapshot, AgentCapabilities{DenyPatterns: []string{".ssh"}})
	if err != nil {
		t.Fatalf("构建视图: %v", err)
	}
	entry, _ := view.Entry("proj")
	if view.Allows(entry, ".ssh", OpRead) {
		t.Error("被 deny 的路径仍被放行")
	}
	if view.Allows(entry, ".ssh/id_rsa", OpRead) {
		t.Error("deny 未覆盖到被排除目录下的文件")
	}
	if !view.Allows(entry, "src/main.go", OpRead) {
		t.Error("未被 deny 的路径被误伤")
	}

	if _, err := BuildInstanceView(identity, snapshot, AgentCapabilities{DenyPatterns: []string{"["}}); err == nil {
		t.Error("非法 deny pattern 未导致建视图失败")
	}
}

// 撤权后，重新拉取的视图必须立刻不再包含该条目。
func TestRevocationRemovesEntry(t *testing.T) {
	_, resolver, provider := twoUserFixture(t)
	ctx := context.Background()

	view, err := BuildInstanceViewForUser(ctx, resolver, provider, "agent-user-1", AgentCapabilities{})
	if err != nil {
		t.Fatalf("构建视图: %v", err)
	}
	if _, ok := view.Entry("Shared"); !ok {
		t.Fatal("撤权前应当看得到 Shared")
	}

	provider.SetGrants("alice", []Grant{{ID: 1, Name: "Documents", Path: view.entries["Documents"].HostPath}})

	rebuilt, err := BuildInstanceViewForUser(ctx, resolver, provider, "agent-user-1", AgentCapabilities{})
	if err != nil {
		t.Fatalf("撤权后重建视图: %v", err)
	}
	if _, ok := rebuilt.Entry("Shared"); ok {
		t.Error("撤权后 Shared 仍在视图中")
	}
	if rebuilt.Generation <= view.Generation {
		t.Error("generation 未推进，派生缓存无法据此失效")
	}
}
