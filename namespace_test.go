package toolfs

// P2 namespace tests.
//
// The headline assertion is the project's acceptance criterion:
// user 1 sees only the directories granted to user 1, user 2 sees only theirs.
//
// The view is a tree now, projected through the alias table (viewpath.go). The
// tests grant paths under a temp base and hand each build a one-line alias that
// strips that base, so a grant at <base>/Documents surfaces at the view root as
// "Documents" — the same shape the ZimaOS table gives real storage spaces.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// capsBase strips the temp base so a grant at <base>/X surfaces as "/X".
func capsBase(base string, patterns ...string) AgentCapabilities {
	return AgentCapabilities{
		DenyPatterns: patterns,
		ViewAliases:  []PathAlias{{Host: base, View: "/"}},
	}
}

// entryFor resolves a mount-relative view path to its grant, failing the test
// if it does not land on one.
func entryFor(t *testing.T, view *InstanceView, viewPath string) *NamespaceEntry {
	t.Helper()
	entry, _, err := view.Resolve(viewPath)
	if err != nil || entry == nil {
		t.Fatalf("resolve %q: entry=%v err=%v", viewPath, entry, err)
	}
	return entry
}

// hasEntry reports whether a view path resolves to a grant.
func hasEntry(view *InstanceView, viewPath string) bool {
	entry, _, err := view.Resolve(viewPath)
	return err == nil && entry != nil
}

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
			{ID: 1, Path: filepath.Join(base, "Documents")},
			{ID: 3, Path: filepath.Join(base, "Shared")},
		},
		"bob": {
			{ID: 2, Path: filepath.Join(base, "Media")},
			{ID: 3, Path: filepath.Join(base, "Shared")},
		},
	})
	return base, resolver, provider
}

// 验收标准：两个用户各自只看到自己被授权的目录。
func TestAcceptance_EachUserSeesOnlyGrantedEntries(t *testing.T) {
	base, resolver, provider := twoUserFixture(t)
	ctx := context.Background()

	aliceView, err := BuildInstanceViewForUser(ctx, resolver, provider, "agent-user-1", "", capsBase(base))
	if err != nil {
		t.Fatalf("构建 alice 视图: %v", err)
	}
	bobView, err := BuildInstanceViewForUser(ctx, resolver, provider, "agent-user-2", "", capsBase(base))
	if err != nil {
		t.Fatalf("构建 bob 视图: %v", err)
	}

	assertEntries(t, "alice", aliceView.Entries(), []string{"Documents", "Shared"})
	assertEntries(t, "bob", bobView.Entries(), []string{"Media", "Shared"})

	// 交叉访问：对方独有的目录在本视图中根本不存在
	if hasEntry(aliceView, "Media") {
		t.Error("alice 的视图里出现了 bob 独有的 Media")
	}
	if hasEntry(bobView, "Documents") {
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

	_, err := BuildInstanceViewForUser(context.Background(), resolver, provider, "agent-user-unknown", "", AgentCapabilities{})
	if err == nil {
		t.Fatal("未绑定账号仍构建出了视图")
	}
	if err != ErrIdentityNotBound {
		t.Errorf("返回错误为 %v，期望 ErrIdentityNotBound", err)
	}
}

// 过渡模式：全部实例绑定同一个专用账号，此时具备完整误删保护但无用户间隔离。
func TestFallbackIdentityServesTransitionMode(t *testing.T) {
	base, resolver, provider := twoUserFixture(t)
	resolver.SetFallback(&ZimaIdentity{Username: "alice", UserID: 1001, Role: "user"})

	view, err := BuildInstanceViewForUser(context.Background(), resolver, provider, "agent-user-unknown", "", capsBase(base))
	if err != nil {
		t.Fatalf("过渡模式下构建视图失败: %v", err)
	}
	assertEntries(t, "fallback", view.Entries(), []string{"Documents", "Shared"})
}

// 虚拟后端不得因为持有文件 Grant 而自动出现在命名空间里。
func TestVirtualBackendsAreNotExposed(t *testing.T) {
	base, resolver, provider := twoUserFixture(t)

	view, err := BuildInstanceViewForUser(context.Background(), resolver, provider, "agent-user-1", "", capsBase(base))
	if err != nil {
		t.Fatalf("构建视图: %v", err)
	}
	for _, forbidden := range []string{"memory", "rag", toolfsStateDir} {
		if hasEntry(view, forbidden) {
			t.Errorf("命名空间里出现了 %q，RBAC 视图只应包含 Grant 派生的条目", forbidden)
		}
	}
}

// 树投影：同名子目录在不同父目录下并存，无需后缀 —— 路径本身把它们分开。
// （旧扁平模型会把两个 Documents 撞在一起并加 ~id 后缀；树模型按父路径天然分开。）
func TestTreeSeparatesSameNameUnderDifferentParents(t *testing.T) {
	base := t.TempDir()
	for _, d := range []string{"a/Documents", "b/Documents"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatalf("准备目录: %v", err)
		}
	}

	view, err := BuildInstanceView(
		&ZimaIdentity{Username: "alice", UserID: 1001},
		NewAccessSnapshot("alice", 1001, "user", []Grant{
			{ID: 7, Path: filepath.Join(base, "a", "Documents")},
			{ID: 9, Path: filepath.Join(base, "b", "Documents")},
		}, 1),
		capsBase(base),
	)
	if err != nil {
		t.Fatalf("构建视图: %v", err)
	}

	// 顶层是两个父目录 a、b，各自下面有 Documents —— 不是两个撞名的顶层条目。
	assertEntries(t, "tree", view.Entries(), []string{"a", "b"})
	if !hasEntry(view, "a/Documents") {
		t.Error("缺少 a/Documents")
	}
	if !hasEntry(view, "b/Documents") {
		t.Error("缺少 b/Documents")
	}
	// 顶层 a、b 是合成穿越目录，本身不是 grant。
	if _, _, err := view.Resolve("a"); err != ErrNotAuthorized {
		t.Errorf("顶层合成目录 a 不应解析为 grant，err=%v", err)
	}
}

// 真实 ZimaOS 别名表：home 落 /Home/<user>，系统盘 /DATA 落 /ZimaOS-HD,
// 其它盘 /media/<disk> 落 /<disk>；合成祖先把共享 topic 摆到完整路径；
// /DATA 授权下的 .home 子目录被分流抑制（它归 /Home）。
func TestZimaProjectionPlacesGrantsUnderSpaces(t *testing.T) {
	view, err := BuildInstanceView(
		&ZimaIdentity{Username: "casaos", UserID: 1},
		NewAccessSnapshot("casaos", 1, "user", []Grant{
			{ID: 1, Path: "/DATA/.home/casaos"},                            // 自己的 home
			{ID: 2, Path: "/DATA/.home/alice/topics/Shared", ReadOnly: true}, // alice 分享的 topic
			{ID: 3, Path: "/media/Backup"},                                 // 另一块盘
			{ID: 4, Path: "/DATA", ReadOnly: true},                         // 系统盘根（owner）
		}, 1),
		AgentCapabilities{}, // 默认 ZimaOS 别名表
	)
	if err != nil {
		t.Fatalf("构建视图: %v", err)
	}

	// 顶层 = 存储空间 + /Home（Home 是合成的，Backup/ZimaOS-HD 是 grant 叶）。
	assertEntries(t, "zima", view.Entries(), []string{"Backup", "Home", "ZimaOS-HD"})

	// 合成祖先把共享 topic 摆到完整视图路径。
	if !hasEntry(view, "Home/casaos") {
		t.Error("缺少 Home/casaos")
	}
	if !hasEntry(view, "Home/alice/topics/Shared") {
		t.Error("缺少 Home/alice/topics/Shared（合成祖先路径）")
	}
	if e := entryFor(t, view, "ZimaOS-HD"); e.HostPath != "/DATA" {
		t.Errorf("ZimaOS-HD 应映射 /DATA，实际 %q", e.HostPath)
	}
	if e := entryFor(t, view, "Backup"); e.HostPath != "/media/Backup" {
		t.Errorf("Backup 应映射 /media/Backup，实际 %q", e.HostPath)
	}
	// Home 顶层本身是合成穿越目录，不是 grant。
	if _, _, err := view.Resolve("Home"); err != ErrNotAuthorized {
		t.Errorf("Home 顶层应是合成目录，err=%v", err)
	}
	// casaos 的 home 授权覆盖其下的 topics/Work（吸收，经 rel 到达）。
	if e, rel, _ := view.Resolve("Home/casaos/topics/Work"); e == nil || e.GrantID != 1 || rel != "topics/Work" {
		t.Errorf("Home/casaos/topics/Work 未落到 home grant: grant=%v rel=%q", e, rel)
	}

	// .home 分流抑制：列 /ZimaOS-HD 时不得露出 .home（它规范到 /Home）。
	zima := entryFor(t, view, "ZimaOS-HD")
	if !view.childDiverges(zima, "", ".home") {
		t.Error(".home 未在 /ZimaOS-HD 下被分流抑制")
	}
	if view.childDiverges(zima, "", "Documents") {
		t.Error("/ZimaOS-HD 下的普通目录 Documents 被误抑制")
	}
}

// 授权对象是单个文件（分享一个文件而非目录）：root 必须开在父目录，文件挂在
// 自己的完整视图路径。此前 openRoots 直接对文件 OpenRoot → "not a directory"
// → 整个挂载失败（一个共享文件就能挂掉某账号）。
func TestFileGrantOpensParentAndExposesTheFile(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "GGG", "docs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("准备目录: %v", err)
	}
	file := filepath.Join(dir, "readme.md")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}

	view, err := BuildInstanceView(
		&ZimaIdentity{Username: "alice", UserID: 1},
		NewAccessSnapshot("alice", 1, "user", []Grant{{ID: 1, Path: file, ReadOnly: true}}, 1),
		capsBase(base),
	)
	if err != nil {
		t.Fatalf("构建视图: %v", err)
	}
	if err := view.openRoots(); err != nil {
		t.Fatalf("openRoots(文件 grant)失败（修复前就是这里挂的）: %v", err)
	}
	t.Cleanup(view.closeRoots)

	// 文件挂在完整视图路径，叶子是文件 grant（root 开在父目录）。
	entry := entryFor(t, view, "GGG/docs/readme.md")
	if entry.fileName != "readme.md" {
		t.Errorf("文件 grant 叶子 fileName=%q，期望 readme.md", entry.fileName)
	}
	if filepath.Base(entry.HostPath) != "docs" {
		t.Errorf("文件 grant HostPath=%q，期望其父目录 docs", entry.HostPath)
	}
	// 决策按完整 host 路径：对文件本身可读。
	if !view.Allows(entry, entry.fileName, OpRead) {
		t.Error("共享文件应可读")
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
			{ID: 1, Path: storage, ReadOnly: false},
			{ID: 2, Path: child, ReadOnly: true},
		}, 1),
		capsBase(base),
	)
	if err != nil {
		t.Fatalf("构建视图: %v", err)
	}

	// storage 是 grant；其下的 temp grant 被吸收，收窄由决策 trie 施加。
	entry := entryFor(t, view, "storage")
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
		{ID: 1, Path: filepath.Join(base, "proj")},
	}, 1)
	identity := &ZimaIdentity{Username: "alice", UserID: 1001}

	view, err := BuildInstanceView(identity, snapshot, capsBase(base, ".ssh"))
	if err != nil {
		t.Fatalf("构建视图: %v", err)
	}
	entry := entryFor(t, view, "proj")
	if view.Allows(entry, ".ssh", OpRead) {
		t.Error("被 deny 的路径仍被放行")
	}
	if view.Allows(entry, ".ssh/id_rsa", OpRead) {
		t.Error("deny 未覆盖到被排除目录下的文件")
	}
	if !view.Allows(entry, "src/main.go", OpRead) {
		t.Error("未被 deny 的路径被误伤")
	}

	if _, err := BuildInstanceView(identity, snapshot, capsBase(base, "[")); err == nil {
		t.Error("非法 deny pattern 未导致建视图失败")
	}
}

// 撤权后，重新拉取的视图必须立刻不再包含该条目。
func TestRevocationRemovesEntry(t *testing.T) {
	base, resolver, provider := twoUserFixture(t)
	ctx := context.Background()

	view, err := BuildInstanceViewForUser(ctx, resolver, provider, "agent-user-1", "", capsBase(base))
	if err != nil {
		t.Fatalf("构建视图: %v", err)
	}
	shared := entryFor(t, view, "Shared")

	provider.SetGrants("alice", []Grant{{ID: 1, Path: entryFor(t, view, "Documents").HostPath}})

	rebuilt, err := BuildInstanceViewForUser(ctx, resolver, provider, "agent-user-1", "", capsBase(base))
	if err != nil {
		t.Fatalf("撤权后重建视图: %v", err)
	}
	if hasEntry(rebuilt, "Shared") {
		t.Error("撤权后 Shared 仍在视图中")
	}
	if rebuilt.Generation <= view.Generation {
		t.Error("generation 未推进，派生缓存无法据此失效")
	}
	_ = shared
}

// 视图随权威实时重建：撤销一条授权后，重投影让运行中的视图不再暴露该目录，
// 无需重挂。对应「分享被撤销、但视图仍陈旧」这一活性缺口的根治。
func TestReprojectDropsRevokedGrantWithoutRemount(t *testing.T) {
	base, resolver, provider := twoUserFixture(t)
	ctx := context.Background()

	view, err := BuildInstanceViewForUser(ctx, resolver, provider, "agent-user-1", "", capsBase(base))
	if err != nil {
		t.Fatalf("构建 alice 视图: %v", err)
	}
	assertEntries(t, "alice 初始", view.Entries(), []string{"Documents", "Shared"})

	// 另一账户撤销了对 alice 的 Shared 分享 —— 权威里 alice 不再持有它。
	provider.SetGrants("alice", []Grant{{ID: 1, Path: filepath.Join(base, "Documents")}})

	fresh, err := BuildInstanceViewForIdentity(ctx, provider, view.Identity, "", capsBase(base))
	if err != nil {
		t.Fatalf("重建视图: %v", err)
	}
	changed, err := view.reproject(fresh)
	if err != nil {
		t.Fatalf("重投影: %v", err)
	}
	if !changed {
		t.Fatal("授权已变，重投影却报告未变")
	}

	// 撤销后：同一个 view 对象上，Shared 从视图里消失、Documents 仍在 —— 无需重挂。
	assertEntries(t, "alice 撤销后", view.Entries(), []string{"Documents"})
	if hasEntry(view, "Shared") {
		t.Error("撤销的 Shared 仍出现在重投影后的视图里")
	}

	// 幂等：授权不再变化时，重投影是 no-op，不做无谓重建。
	same, err := BuildInstanceViewForIdentity(ctx, provider, view.Identity, "", capsBase(base))
	if err != nil {
		t.Fatalf("重建视图(无变化): %v", err)
	}
	if again, _ := view.reproject(same); again {
		t.Error("授权未变，重投影却重建了视图")
	}
}
