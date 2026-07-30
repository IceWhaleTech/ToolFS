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
)

// mountTwoUsers mounts alice and bob and returns their mount points.
func mountTwoUsers(t *testing.T) (aliceMnt, bobMnt string, cleanup func()) {
	t.Helper()
	requireFUSE(t)

	base, resolver, provider := twoUserFixture(t)
	_ = base

	mntBase := t.TempDir()
	aliceMnt = filepath.Join(mntBase, "u1001", "workspace")
	bobMnt = filepath.Join(mntBase, "u1002", "workspace")
	for _, m := range []string{aliceMnt, bobMnt} {
		if err := os.MkdirAll(m, 0o755); err != nil {
			t.Fatalf("准备挂载点: %v", err)
		}
	}

	ctx := context.Background()
	aliceHandle, err := MountForIdentity(ctx, resolver, provider, "agent-user-1", aliceMnt, AgentCapabilities{}, MountConfig{})
	if err != nil {
		t.Skipf("跳过：本环境无法挂载 FUSE (%v)", err)
	}
	bobHandle, err := MountForIdentity(ctx, resolver, provider, "agent-user-2", bobMnt, AgentCapabilities{}, MountConfig{})
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

// 挂载点在写保护链就绪前保持只读——在恢复能力存在之前允许覆盖写，
// 恰恰就是本项目要消除的误删风险。
func TestMount_IsReadOnlyUntilProtectionChainLands(t *testing.T) {
	aliceMnt, _, cleanup := mountTwoUsers(t)
	defer cleanup()

	target := filepath.Join(aliceMnt, "Documents", "file.txt")
	if err := os.WriteFile(target, []byte("OVERWRITTEN"), 0o644); err == nil {
		t.Error("覆盖写意外成功——保护链尚未落地时挂载点必须只读")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("回读: %v", err)
	}
	if string(data) != "Documents" {
		t.Errorf("文件内容被改成了 %q", string(data))
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
		"agent-user-1", mnt, AgentCapabilities{}, MountConfig{})
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
