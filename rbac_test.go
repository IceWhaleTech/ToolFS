package toolfs

// P1 authorization tests.
//
// The assertions here encode facts measured on a real ZimaOS device
// (ZimaCube Pro / v1.7.0), not hypotheticals:
//   - 8 of 14 share rows carried is_deleted=1, so filtering is load-bearing
//   - /media/ZimaOS-HD is a symlink to /DATA and the table stores the aliased
//     form with a trailing slash
//   - parent-rw with child-ro genuinely occurs (HDD-Storage + .../rauc/temp)

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestGrantOpsWithholdDeleteByDefault(t *testing.T) {
	rw := Grant{Path: "/DATA/Documents", ReadOnly: false}
	ro := Grant{Path: "/DATA/Media", ReadOnly: true}

	if rw.Ops()&OpDelete != 0 {
		t.Error("读写 Grant 默认就带上了 OpDelete —— 删除权限必须默认不授予")
	}
	if rw.Ops()&(OpRead|OpList|OpCreate|OpModify) == 0 {
		t.Error("读写 Grant 缺少基本操作权限")
	}
	if ro.Ops()&(OpCreate|OpModify|OpDelete) != 0 {
		t.Error("只读 Grant 带上了写类权限")
	}
}

func TestCapabilitiesCannotPromoteReadOnlyGrant(t *testing.T) {
	caps := AgentCapabilities{AllowDelete: true}

	ro := Grant{Path: "/DATA/Media", ReadOnly: true}
	if grantAllows(ro, OpDelete, caps) {
		t.Error("AllowDelete 把只读 Grant 提升成了可删——capability 只能收窄，不能放大")
	}

	rw := Grant{Path: "/DATA/Documents", ReadOnly: false}
	if !grantAllows(rw, OpDelete, caps) {
		t.Error("显式开启 AllowDelete 后，读写 Grant 仍不可删")
	}
	if grantAllows(rw, OpDelete, AgentCapabilities{}) {
		t.Error("未开启 AllowDelete 时读写 Grant 仍可删")
	}
}

func TestPathAliasIsFolded(t *testing.T) {
	// 表里存的是带尾斜杠的别名形式，且该别名本身是指向 /DATA 的软链
	snapshot := NewAccessSnapshot("alice", 0, "", []Grant{
		{ID: 1, Path: "/media/ZimaOS-HD/Documents/", ReadOnly: false},
	}, 1)

	for _, probe := range []string{
		"/DATA/Documents",
		"/DATA/Documents/report.md",
		"/media/ZimaOS-HD/Documents",
		"/media/ZimaOS-HD/Documents/report.md",
	} {
		if !snapshot.Allows(probe, OpRead, AgentCapabilities{}) {
			t.Errorf("%q 未命中授权——别名或尾斜杠归一失败", probe)
		}
	}

	if len(snapshot.Grants) != 1 {
		t.Errorf("同一目录产生了 %d 条 Grant，别名归一后应当只有 1 条", len(snapshot.Grants))
	}
	if snapshot.Grants[0].Path != "/DATA/Documents" {
		t.Errorf("canonical 形式为 %q，期望 /DATA/Documents（readlink -f 的结果）", snapshot.Grants[0].Path)
	}
}

func TestConflictingGrantsResolveToReadOnly(t *testing.T) {
	snapshot := NewAccessSnapshot("alice", 0, "", []Grant{
		{ID: 1, Path: "/DATA/Documents", ReadOnly: false},
		{ID: 2, Path: "/media/ZimaOS-HD/Documents", ReadOnly: true},
	}, 1)

	if snapshot.Allows("/DATA/Documents/x.txt", OpModify, AgentCapabilities{}) {
		t.Error("同一路径的 RO/RW 冲突被合并成了可写——应取更严格的一侧")
	}
	if !snapshot.Allows("/DATA/Documents/x.txt", OpRead, AgentCapabilities{}) {
		t.Error("冲突合并后连读权限都丢了")
	}
}

// 实测存在的形态：父目录 rw，子目录 ro。最长前缀匹配必须让子目录的收窄生效。
func TestNestedGrantNarrowsParent(t *testing.T) {
	snapshot := NewAccessSnapshot("alice", 0, "", []Grant{
		{ID: 1, Path: "/media/HDD-Storage", ReadOnly: false},
		{ID: 2, Path: "/media/HDD-Storage/rauc/temp", ReadOnly: true},
	}, 1)

	if !snapshot.Allows("/media/HDD-Storage/other/file", OpModify, AgentCapabilities{}) {
		t.Error("父目录的读写权限未生效")
	}
	if snapshot.Allows("/media/HDD-Storage/rauc/temp/file", OpModify, AgentCapabilities{}) {
		t.Error("子目录的只读收窄未生效——最长前缀匹配有误")
	}
	if !snapshot.Allows("/media/HDD-Storage/rauc/temp/file", OpRead, AgentCapabilities{}) {
		t.Error("子目录应当仍可读")
	}
}

func TestGrantMatchingIsSegmentAware(t *testing.T) {
	snapshot := NewAccessSnapshot("alice", 0, "", []Grant{
		{ID: 1, Path: "/DATA/Doc", ReadOnly: false},
	}, 1)

	if snapshot.Allows("/DATA/Documents/secret.md", OpRead, AgentCapabilities{}) {
		t.Error("/DATA/Documents 命中了 /DATA/Doc 的授权——前缀匹配未按路径段进行")
	}
	if !snapshot.Allows("/DATA/Doc/note.md", OpRead, AgentCapabilities{}) {
		t.Error("真正的子路径反而未命中")
	}
}

func TestZeroGrantUserSeesNothing(t *testing.T) {
	// admin 也走同一套逻辑：ToolFS 内不存在 if role == "admin" 的旁路
	for _, role := range []string{"user", "admin"} {
		snapshot := NewAccessSnapshot("someone", 0, role, nil, 1)
		if snapshot.Allows("/DATA/Documents", OpRead, AgentCapabilities{}) {
			t.Errorf("role=%s 的零 Grant 用户仍被放行——存在特权旁路", role)
		}
		if len(snapshot.Grants) != 0 {
			t.Errorf("role=%s 的零 Grant 用户构建出了 %d 条 Grant", role, len(snapshot.Grants))
		}
	}
}

func TestDeletedAndDisabledSharesProduceNoGrant(t *testing.T) {
	// 实测：14 行 share 中 8 行 is_deleted=1。上游 /share/permission 不过滤该列，
	// 直接采信会造成权限放大，因此 ToolFS 必须自行过滤。
	rows := []shareRow{
		{ID: 1, Path: "/DATA/Documents", Username: "alice", Status: "enable"},
		{ID: 2, Path: "/DATA/Secret", Username: "alice", Deleted: true},
		{ID: 3, Path: "/DATA/Disabled", Username: "alice", Status: "disable"},
		{ID: 4, Path: "/DATA/Missing", Username: "alice", Status: "missing"},
		{ID: 5, Path: "/DATA/Other", Username: "bob", Status: "enable"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Z-Skip-Auth") != "true" {
			t.Errorf("缺少服务间免鉴权头，实际头部: %v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": rows})
	}))
	defer srv.Close()

	provider := NewFilesServiceProvider(srv.URL, 0)
	snapshot, err := provider.GrantsFor(context.Background(), "alice")
	if err != nil {
		t.Fatalf("拉取授权: %v", err)
	}

	if len(snapshot.Grants) != 1 {
		t.Fatalf("产生了 %d 条 Grant，期望 1 条（其余应被 is_deleted/status/用户名过滤掉）: %+v",
			len(snapshot.Grants), snapshot.Grants)
	}
	if snapshot.Grants[0].Path != "/DATA/Documents" {
		t.Errorf("保留下来的是 %q，期望 /DATA/Documents", snapshot.Grants[0].Path)
	}

	for _, denied := range []string{"/DATA/Secret", "/DATA/Disabled", "/DATA/Missing", "/DATA/Other"} {
		if snapshot.Allows(denied, OpRead, AgentCapabilities{}) {
			t.Errorf("%q 仍被放行——权限放大", denied)
		}
	}
}

func TestProviderFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	provider := NewFilesServiceProvider(srv.URL, 0)
	snapshot, err := provider.GrantsFor(context.Background(), "alice")
	if err == nil {
		t.Fatal("上游报错时未返回错误——必须 fail closed，不能被读成「无限制」")
	}
	if snapshot != nil {
		t.Error("上游报错时仍返回了快照")
	}
}

func TestMalformedResponseFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()

	provider := NewFilesServiceProvider(srv.URL, 0)
	if _, err := provider.GrantsFor(context.Background(), "alice"); err == nil {
		t.Fatal("响应畸形时未返回错误")
	}
}

func TestSnapshotRefreshIsRaceFree(t *testing.T) {
	provider := NewStaticPermissionProvider(map[string][]Grant{
		"alice": {{ID: 1, Path: "/DATA/Documents"}},
	})

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%4 == 0 {
				provider.SetGrants("alice", []Grant{{ID: 1, Path: "/DATA/Documents", ReadOnly: i%8 == 0}})
			}
			snapshot, err := provider.GrantsFor(context.Background(), "alice")
			if err != nil {
				t.Errorf("并发拉取失败: %v", err)
				return
			}
			// 快照不可变：读它的任何字段都不应观察到半更新状态
			_ = snapshot.Allows("/DATA/Documents/x", OpRead, AgentCapabilities{})
		}(i)
	}
	wg.Wait()
}

func TestGenerationAdvancesOnRefresh(t *testing.T) {
	provider := NewStaticPermissionProvider(map[string][]Grant{
		"alice": {{ID: 1, Path: "/DATA/Documents"}},
	})

	first, err := provider.FreshGrantsFor(context.Background(), "alice")
	if err != nil {
		t.Fatalf("首次拉取: %v", err)
	}
	second, err := provider.FreshGrantsFor(context.Background(), "alice")
	if err != nil {
		t.Fatalf("再次拉取: %v", err)
	}

	if second.Generation <= first.Generation {
		t.Errorf("generation 未递增（%d → %d）——派生状态将无法据此失效",
			first.Generation, second.Generation)
	}
}
