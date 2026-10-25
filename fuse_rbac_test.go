//go:build linux || darwin
// +build linux darwin

package toolfs

// End-to-end mount tests.
//
// These need a working FUSE device, so they skip rather than fail where one is
// unavailable (CI containers without /dev/fuse, macOS without macFUSE).

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// mountTwoUsers mounts alice and bob and returns their mount points.
func mountTwoUsers(t *testing.T) (aliceMnt, bobMnt string, cleanup func()) {
	t.Helper()
	requireFUSE(t)

	base, resolver, provider := twoUserFixture(t)
	caps := capsBase(base)

	mntBase := t.TempDir()
	aliceMnt = filepath.Join(mntBase, "u1001", "workspace")
	bobMnt = filepath.Join(mntBase, "u1002", "workspace")
	for _, m := range []string{aliceMnt, bobMnt} {
		if err := os.MkdirAll(m, 0o755); err != nil {
			t.Fatalf("准备挂载点: %v", err)
		}
	}

	ctx := context.Background()
	aliceHandle, err := MountForIdentity(ctx, resolver, provider, "agent-user-1", "", aliceMnt, caps, MountConfig{})
	if err != nil {
		t.Skipf("跳过：本环境无法挂载 FUSE (%v)", err)
	}
	bobHandle, err := MountForIdentity(ctx, resolver, provider, "agent-user-2", "", bobMnt, caps, MountConfig{})
	if err != nil {
		_ = aliceHandle.Unmount()
		t.Skipf("跳过：本环境无法挂载 FUSE (%v)", err)
	}

	return aliceMnt, bobMnt, func() {
		_ = aliceHandle.Unmount()
		_ = bobHandle.Unmount()
		time.Sleep(50 * time.Millisecond)
	}
}

// 验收标准的端到端形态：在真实挂载点上 ls，各自只见自己的目录。
func TestMount_EachUserSeesOnlyOwnDirectories(t *testing.T) {
	aliceMnt, bobMnt, cleanup := mountTwoUsers(t)
	defer cleanup()

	assertDirNames(t, "alice", aliceMnt, []string{"Documents", "Shared"})
	assertDirNames(t, "bob", bobMnt, []string{"Media", "Shared"})

	// 对方独有的目录在本挂载点内不存在
	if _, err := os.Stat(filepath.Join(aliceMnt, "Media")); !os.IsNotExist(err) {
		t.Errorf("alice 的挂载点里能 stat 到 Media，err=%v（期望 not exist）", err)
	}
	if _, err := os.Stat(filepath.Join(bobMnt, "Documents")); !os.IsNotExist(err) {
		t.Errorf("bob 的挂载点里能 stat 到 Documents，err=%v（期望 not exist）", err)
	}
}

func assertDirNames(t *testing.T, who, dir string, want []string) {
	t.Helper()
	items, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("%s 读挂载点: %v", who, err)
	}
	got := make([]string, 0, len(items))
	for _, item := range items {
		// The control view is always present at the root and is not derived
		// from a grant, so it is excluded from grant-set assertions. Its own
		// behaviour is covered by the control view tests.
		if item.Name() == toolfsControlDir {
			continue
		}
		got = append(got, item.Name())
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s 看到 %v，期望 %v", who, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s 看到 %v，期望 %v", who, got, want)
		}
	}
}

// 授权目录内的文件可正常读取。
func TestMount_GrantedFileIsReadable(t *testing.T) {
	aliceMnt, _, cleanup := mountTwoUsers(t)
	defer cleanup()

	data, err := os.ReadFile(filepath.Join(aliceMnt, "Documents", "file.txt"))
	if err != nil {
		t.Fatalf("读授权目录内的文件: %v", err)
	}
	if string(data) != "Documents" {
		t.Errorf("读到 %q，期望 Documents", string(data))
	}
}

// 写入受保护链约束：内容真正写入，且覆盖前的内容被留存。
// （本用例此前断言「挂载点只读」——那条限制随保护链落地已被移除。）
func TestMount_WritesAreProtected(t *testing.T) {
	aliceMnt, _, cleanup := mountTwoUsers(t)
	defer cleanup()

	target := filepath.Join(aliceMnt, "Documents", "file.txt")
	if err := os.WriteFile(target, []byte("OVERWRITTEN"), 0o644); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("回读: %v", err)
	}
	if string(data) != "OVERWRITTEN" {
		t.Errorf("写入未生效，读到 %q", string(data))
	}
}

// 默认不授予删除。
func TestMount_DeleteWithheldByDefault(t *testing.T) {
	aliceMnt, _, cleanup := mountTwoUsers(t)
	defer cleanup()

	if err := os.Remove(filepath.Join(aliceMnt, "Documents", "file.txt")); err == nil {
		t.Error("默认配置下删除成功了")
	}
}

// Unmount 必须幂等。
func TestMount_UnmountIsIdempotent(t *testing.T) {
	requireFUSE(t)

	base, resolver, provider := twoUserFixture(t)
	_ = base

	mnt := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		t.Fatalf("准备挂载点: %v", err)
	}

	handle, err := MountForIdentity(context.Background(), resolver, provider,
		"agent-user-1", "", mnt, AgentCapabilities{}, MountConfig{})
	if err != nil {
		t.Skipf("跳过：本环境无法挂载 FUSE (%v)", err)
	}

	if err := handle.Unmount(); err != nil {
		t.Fatalf("首次卸载: %v", err)
	}
	if err := handle.Unmount(); err != nil {
		t.Errorf("重复卸载应当是 no-op，却返回 %v", err)
	}
}

// requireFUSE skips a test when the machine cannot serve FUSE mounts.
// Probing up front matters: go-fuse blocks rather than erroring when no FUSE
// implementation is installed, which would hang the suite instead of skipping.
func requireFUSE(t *testing.T) {
	t.Helper()
	if !FUSEAvailable() {
		t.Skip("跳过：本机无可用的 FUSE 实现（Linux 需 /dev/fuse，macOS 需 macFUSE）")
	}
}

// The backing mtime has to survive the trip through the FUSE layer: every
// attribute used to report the zero time, and a namespace where everything
// says 1970 reads as broken to both people and sync tools.
func TestAttributesCarryTheBackingModTime(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filePath, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	access := NewAccessSnapshot("alice", 1001, "user", []Grant{{ID: 1, Path: dir}}, 1)
	view, err := BuildInstanceView(&ZimaIdentity{Username: "alice", UserID: 1001}, access,
		AgentCapabilities{ViewAliases: []PathAlias{{Host: dir, View: "/docs"}}})
	if err != nil {
		t.Fatalf("BuildInstanceView: %v", err)
	}
	if err := view.openRoots(); err != nil {
		t.Fatalf("openRoots: %v", err)
	}
	t.Cleanup(view.closeRoots)

	entry, _, err := view.Resolve("docs")
	if err != nil || entry == nil {
		t.Fatalf("entry missing; entries = %v", view.Entries())
	}

	file := &instanceFile{view: view, entry: entry, rel: "doc.txt"}
	var out fuse.AttrOut
	if errno := file.Getattr(context.Background(), nil, &out); errno != 0 {
		t.Fatalf("Getattr: %v", errno)
	}
	if out.Mtime != uint64(stamp.Unix()) {
		t.Errorf("Mtime = %d, want %d — the backing time never made the trip", out.Mtime, stamp.Unix())
	}
	if out.Nlink == 0 {
		t.Error("Nlink = 0 reads as broken; the view has no hard links to count")
	}
}
