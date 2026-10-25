//go:build linux || darwin
// +build linux darwin

package toolfs

// P4 recovery tests.
//
// The headline capability: an agent's task touched a pile of files and the user
// wants all of it undone in one call.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// mountForRecovery mounts alice with delete permitted and a task label set,
// returning the mount point and the live view.
func mountForRecovery(t *testing.T, sessionID, taskID string) (mnt, dataBase string, view *InstanceView, cleanup func()) {
	t.Helper()
	requireFUSE(t)

	dataBase, resolver, provider := twoUserFixture(t)
	caps := capsBase(dataBase)
	caps.AllowDelete = true

	built, err := BuildInstanceViewForUser(context.Background(), resolver, provider,
		"agent-user-1", "", caps)
	if err != nil {
		t.Fatalf("构建视图: %v", err)
	}
	built.SessionID = sessionID
	built.TaskID = taskID
	if err := built.openRoots(); err != nil {
		t.Fatalf("打开 Grant 根: %v", err)
	}

	mnt = filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		t.Fatalf("准备挂载点: %v", err)
	}
	handle, err := mountView(mnt, built, MountConfig{})
	if err != nil {
		built.closeRoots()
		t.Skipf("跳过：本环境无法挂载 FUSE (%v)", err)
	}
	return mnt, dataBase, built, func() { _ = handle.Unmount() }
}

// 核心能力：一个任务动了多个文件，一条指令整体撤销。
func TestRecovery_UndoWholeTask(t *testing.T) {
	mnt, _, view, cleanup := mountForRecovery(t, "sess-1", "task-1")
	defer cleanup()

	docs := filepath.Join(mnt, "Documents")

	// 任务做了三件事：改一个、建一个、删一个
	if err := os.WriteFile(filepath.Join(docs, "file.txt"), []byte("MODIFIED"), 0o644); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}
	if err := os.WriteFile(filepath.Join(docs, "created.txt"), []byte("NEW"), 0o644); err != nil {
		t.Fatalf("新建: %v", err)
	}
	if err := os.Remove(filepath.Join(docs, "victim.txt")); err != nil {
		t.Fatalf("删除: %v", err)
	}

	changes, err := view.ListChanges(ChangeFilter{TaskID: "task-1"})
	if err != nil {
		t.Fatalf("列变更: %v", err)
	}
	if len(changes) < 3 {
		t.Fatalf("只记录到 %d 条变更，期望至少 3 条: %+v", len(changes), changes)
	}

	report, err := view.UndoChanges(ChangeFilter{TaskID: "task-1"})
	if err != nil {
		t.Fatalf("撤销: %v", err)
	}
	for _, r := range report.Results {
		t.Logf("undo %s %s -> %s %s", r.Change.Op, r.Change.Path, r.Outcome, r.Reason)
	}
	if report.Applied == 0 {
		t.Fatalf("没有任何变更被撤销: %+v", report)
	}

	// 被改的文件回到原内容
	restored, err := os.ReadFile(filepath.Join(docs, "file.txt"))
	if err != nil {
		t.Fatalf("回读被改文件: %v", err)
	}
	if string(restored) != "Documents" {
		t.Errorf("撤销后内容为 %q，期望原始的 Documents", string(restored))
	}

	// 被建的文件消失
	if _, err := os.Stat(filepath.Join(docs, "created.txt")); !os.IsNotExist(err) {
		t.Error("撤销后新建的文件仍在")
	}

	// 被删的文件回来
	if _, err := os.Stat(filepath.Join(docs, "victim.txt")); err != nil {
		t.Errorf("撤销后被删的文件未恢复: %v", err)
	}
}

// 冲突必须跳过并报告，而不是覆盖他人的工作，也不因一处冲突废掉整次撤销。
func TestRecovery_ConflictIsSkippedNotClobbered(t *testing.T) {
	mnt, dataBase, view, cleanup := mountForRecovery(t, "sess-1", "task-1")
	defer cleanup()

	docs := filepath.Join(mnt, "Documents")
	if err := os.WriteFile(filepath.Join(docs, "file.txt"), []byte("BY-AGENT"), 0o644); err != nil {
		t.Fatalf("agent 覆盖写: %v", err)
	}
	if err := os.WriteFile(filepath.Join(docs, "other.txt"), []byte("ALSO-BY-AGENT"), 0o644); err != nil {
		t.Fatalf("agent 建另一个文件: %v", err)
	}

	// 变更之后，别人又改了其中一个文件。
	// 这一笔必须【绕过挂载点】直接写后端——经挂载点写就会带上同样的 task 标签，
	// 从而本身也成为本任务的一部分，被一并撤销才是正确行为。
	// 绕过挂载点正是在模拟 SMB 或另一个用户。
	if err := os.WriteFile(filepath.Join(dataBase, "Documents", "file.txt"),
		[]byte("BY-SOMEONE-ELSE-LATER"), 0o644); err != nil {
		t.Fatalf("他人绕过挂载点改动: %v", err)
	}

	report, err := view.UndoChanges(ChangeFilter{TaskID: "task-1"})
	if err != nil {
		t.Fatalf("撤销: %v", err)
	}
	for _, r := range report.Results {
		t.Logf("undo %s %s -> %s %s", r.Change.Op, r.Change.Path, r.Outcome, r.Reason)
	}
	if report.Skipped == 0 {
		t.Errorf("冲突未被跳过: %+v", report)
	}

	// 他人的改动必须保留
	content, err := os.ReadFile(filepath.Join(docs, "file.txt"))
	if err != nil {
		t.Fatalf("回读: %v", err)
	}
	if string(content) == "Documents" {
		t.Error("撤销覆盖了他人在此之后的改动")
	}

	// 部分成功：无冲突的那一项仍应被撤销
	if _, err := os.Stat(filepath.Join(docs, "other.txt")); !os.IsNotExist(err) {
		t.Error("无冲突的变更未被撤销——一处冲突不应废掉整次撤销")
	}

	// 报告里要说明跳过原因
	var explained bool
	for _, r := range report.Results {
		if r.Outcome == UndoSkipped && r.Reason != "" {
			explained = true
		}
	}
	if !explained {
		t.Error("跳过项没有给出原因")
	}
}

// 三层恢复源合并为一条时间线。
func TestRecovery_RestorePointsMergeSources(t *testing.T) {
	mnt, _, view, cleanup := mountForRecovery(t, "sess-1", "task-1")
	defer cleanup()

	docs := filepath.Join(mnt, "Documents")
	target := filepath.Join(docs, "file.txt")

	if err := os.WriteFile(target, []byte("V2"), 0o644); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatalf("删除: %v", err)
	}

	points, err := view.ListRestorePoints("Documents/file.txt")
	if err != nil {
		t.Fatalf("列恢复点: %v", err)
	}
	if len(points) < 2 {
		t.Fatalf("只有 %d 个恢复点，期望版本与回收站各至少一个: %+v", len(points), points)
	}

	kinds := map[RestorePointKind]bool{}
	for _, p := range points {
		kinds[p.Kind] = true
	}
	if !kinds[RestoreFromVersion] || !kinds[RestoreFromTrash] {
		t.Errorf("恢复点未同时包含 version 与 trash 两类来源: %+v", points)
	}

	// 按时间倒序
	for i := 1; i < len(points); i++ {
		if points[i].At.After(points[i-1].At) {
			t.Error("恢复点未按时间倒序排列")
			break
		}
	}
}

// 闪回：把文件写回某个恢复点，且当前内容自身也被保留。
func TestRecovery_FlashbackPreservesCurrentContent(t *testing.T) {
	mnt, _, view, cleanup := mountForRecovery(t, "sess-1", "task-1")
	defer cleanup()

	target := filepath.Join(mnt, "Documents", "file.txt")
	if err := os.WriteFile(target, []byte("SECOND"), 0o644); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}

	points, err := view.ListRestorePoints("Documents/file.txt")
	if err != nil || len(points) == 0 {
		t.Fatalf("列恢复点失败: %v (%d 个)", err, len(points))
	}

	if err := view.Flashback("Documents/file.txt", points[0].ID); err != nil {
		t.Fatalf("闪回: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("回读: %v", err)
	}
	if string(content) != "Documents" {
		t.Errorf("闪回后内容为 %q，期望原始内容", string(content))
	}

	// 闪回前的内容也应当可再恢复——误闪回同样要能撤销
	after, err := view.ListRestorePoints("Documents/file.txt")
	if err != nil {
		t.Fatalf("闪回后列恢复点: %v", err)
	}
	if len(after) <= len(points) {
		t.Error("闪回未保留闪回前的内容")
	}
}

// 撤权后不得经旧版本取回数据。
func TestRecovery_RevokedGrantBlocksRestore(t *testing.T) {
	mnt, _, view, cleanup := mountForRecovery(t, "sess-1", "task-1")
	defer cleanup()

	if err := os.WriteFile(filepath.Join(mnt, "Documents", "file.txt"), []byte("V2"), 0o644); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}

	// 模拟撤权：换成一个不含该路径的授权快照
	view.mu.Lock()
	view.Access = NewAccessSnapshot(view.Identity.Username, view.Identity.UserID, "user", nil, 99)
	view.mu.Unlock()

	if _, err := view.ListRestorePoints("Documents/file.txt"); err == nil {
		t.Error("撤权后仍能列出恢复点")
	}
	if err := view.Flashback("Documents/file.txt", "any"); err == nil {
		t.Error("撤权后仍能闪回——恢复必须按【当前】授权判定")
	}
}
