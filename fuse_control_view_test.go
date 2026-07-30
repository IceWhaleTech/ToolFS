//go:build linux || darwin
// +build linux darwin

package toolfs

// Control view tests.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readControlLines reads one control file and decodes its JSON lines.
func readControlLines(t *testing.T, mnt, name string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(mnt, toolfsControlDir, name))
	if err != nil {
		t.Fatalf("读 %s: %v", name, err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("解析 %s 的一行 %q: %v", name, line, err)
		}
		out = append(out, record)
	}
	return out
}

// 控制视图出现在挂载根，且四个文件都可读。
func TestControlView_IsPresentAndReadable(t *testing.T) {
	mnt, _, _, cleanup := mountForRecovery(t, "sess-1", "task-1")
	defer cleanup()

	items, err := os.ReadDir(mnt)
	if err != nil {
		t.Fatalf("列挂载根: %v", err)
	}
	var seen bool
	for _, item := range items {
		if item.Name() == toolfsControlDir {
			seen = true
		}
	}
	if !seen {
		t.Fatal("挂载根没有出现 .toolfs 控制视图")
	}

	for _, name := range controlFiles {
		if _, err := os.ReadFile(filepath.Join(mnt, toolfsControlDir, name)); err != nil {
			t.Errorf("读 .toolfs/%s 失败: %v", name, err)
		}
	}
}

// 控制视图必须只读——它是一扇窗，不是一道门。
func TestControlView_IsReadOnly(t *testing.T) {
	mnt, _, _, cleanup := mountForRecovery(t, "sess-1", "task-1")
	defer cleanup()

	control := filepath.Join(mnt, toolfsControlDir)

	if err := os.WriteFile(filepath.Join(control, controlChanges), []byte("x"), 0o644); err == nil {
		t.Error("控制视图内的文件可写")
	}
	if err := os.WriteFile(filepath.Join(control, "new.txt"), []byte("x"), 0o644); err == nil {
		t.Error("可以在控制视图内新建文件")
	}
	if err := os.Remove(filepath.Join(control, controlReadme)); err == nil {
		t.Error("可以删除控制视图内的文件")
	}
}

// 变更记录反映实际操作，且带上 task 标签。
func TestControlView_ChangesReflectMutations(t *testing.T) {
	mnt, _, _, cleanup := mountForRecovery(t, "sess-7", "task-7")
	defer cleanup()

	docs := filepath.Join(mnt, "Documents")
	if err := os.WriteFile(filepath.Join(docs, "file.txt"), []byte("CHANGED"), 0o644); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}
	if err := os.Remove(filepath.Join(docs, "victim.txt")); err != nil {
		t.Fatalf("删除: %v", err)
	}

	changes := readControlLines(t, mnt, controlChanges)
	if len(changes) < 2 {
		t.Fatalf("只记录到 %d 条变更: %+v", len(changes), changes)
	}

	var sawModify, sawDelete bool
	for _, change := range changes {
		if change["task_id"] != "task-7" {
			t.Errorf("变更未带上 task 标签: %+v", change)
		}
		if change["session_id"] != "sess-7" {
			t.Errorf("变更未带上 session 标签: %+v", change)
		}
		// 路径应当是挂载点视角（含 Grant 名），而非 Grant 内相对路径
		if path, _ := change["path"].(string); !strings.HasPrefix(path, "Documents/") {
			t.Errorf("路径 %q 不是挂载点视角", path)
		}
		switch change["op"] {
		case string(MutationModify):
			sawModify = true
		case string(MutationDelete):
			sawDelete = true
		}
	}
	if !sawModify || !sawDelete {
		t.Errorf("变更记录缺少 modify 或 delete: %+v", changes)
	}
}

// 回收站与恢复点可查。
func TestControlView_TrashAndRestorePoints(t *testing.T) {
	mnt, _, _, cleanup := mountForRecovery(t, "sess-1", "task-1")
	defer cleanup()

	docs := filepath.Join(mnt, "Documents")
	if err := os.WriteFile(filepath.Join(docs, "file.txt"), []byte("V2"), 0o644); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}
	if err := os.Remove(filepath.Join(docs, "victim.txt")); err != nil {
		t.Fatalf("删除: %v", err)
	}

	trash := readControlLines(t, mnt, controlTrash)
	if len(trash) == 0 {
		t.Fatal("回收站视图为空")
	}
	var sawVictim bool
	for _, item := range trash {
		if path, _ := item["path"].(string); path == "Documents/victim.txt" {
			sawVictim = true
			if item["id"] == "" {
				t.Error("回收站条目缺少可用于恢复的 id")
			}
		}
	}
	if !sawVictim {
		t.Errorf("回收站视图里没有被删的文件: %+v", trash)
	}

	points := readControlLines(t, mnt, controlRestorePoints)
	if len(points) == 0 {
		t.Fatal("恢复点视图为空")
	}
	for _, point := range points {
		if point["id"] == "" || point["kind"] == "" {
			t.Errorf("恢复点缺少 id 或 kind: %+v", point)
		}
	}
}

// 撤权后，控制视图里对应的条目必须立刻消失——否则它会变成旁路。
func TestControlView_FiltersByCurrentGrants(t *testing.T) {
	mnt, _, view, cleanup := mountForRecovery(t, "sess-1", "task-1")
	defer cleanup()

	if err := os.WriteFile(filepath.Join(mnt, "Documents", "file.txt"), []byte("V2"), 0o644); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}
	if before := readControlLines(t, mnt, controlChanges); len(before) == 0 {
		t.Fatal("撤权前变更记录就是空的")
	}

	// 撤销全部授权
	view.Access = NewAccessSnapshot(view.Identity.Username, view.Identity.UserID, "user", nil, 99)

	after := readControlLines(t, mnt, controlChanges)
	if len(after) != 0 {
		t.Errorf("撤权后控制视图仍列出 %d 条变更——它成了绕过授权的旁路: %+v", len(after), after)
	}
	if points := readControlLines(t, mnt, controlRestorePoints); len(points) != 0 {
		t.Errorf("撤权后仍能看到恢复点: %+v", points)
	}
}

// 用户数据不得遮蔽保留名。
func TestControlView_ReservedNameCannotBeShadowed(t *testing.T) {
	view, err := BuildInstanceView(
		&ZimaIdentity{Username: "alice", UserID: 1001},
		NewAccessSnapshot("alice", 1001, "user", []Grant{
			{ID: 1, Name: toolfsControlDir, Path: "/tmp/whatever"},
			{ID: 2, Name: toolfsStateDir, Path: "/tmp/whatever2"},
		}, 1),
		AgentCapabilities{},
	)
	if err != nil {
		t.Fatalf("构建视图: %v", err)
	}
	for _, reserved := range []string{toolfsControlDir, toolfsStateDir} {
		if _, ok := view.Entry(reserved); ok {
			t.Errorf("名为 %q 的 Grant 进入了命名空间，可遮蔽保留名", reserved)
		}
	}
}
