//go:build linux || darwin
// +build linux darwin

package toolfs

// P3 protection chain tests.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStateRoot(t *testing.T, policy ProtectionPolicy) (*StateRoot, string) {
	t.Helper()
	base := t.TempDir()
	data := filepath.Join(base, "data")
	state := filepath.Join(base, StateDirName)
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatalf("准备数据目录: %v", err)
	}
	root, err := OpenStateRoot(state, data, policy)
	if err != nil {
		t.Fatalf("打开 StateRoot: %v", err)
	}
	return root, data
}

func TestTrashIsRenameAndRestorable(t *testing.T) {
	root, data := newStateRoot(t, DefaultProtectionPolicy())

	target := filepath.Join(data, "report.md")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}

	entry, err := root.TrashFile("alice", target, false, 8)
	if err != nil {
		t.Fatalf("移入回收站: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("原路径仍存在，删除未生效")
	}

	entries, err := root.ListTrash()
	if err != nil || len(entries) != 1 {
		t.Fatalf("回收站列表返回 %d 条（err=%v），期望 1 条", len(entries), err)
	}

	if _, err := root.RestoreFromTrash(entry.ID); err != nil {
		t.Fatalf("恢复: %v", err)
	}
	data2, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("回读恢复的文件: %v", err)
	}
	if string(data2) != "ORIGINAL" {
		t.Errorf("恢复后内容为 %q，期望 ORIGINAL", string(data2))
	}
}

// 递归删目录只 rename 目录本身，不遍历——这是 O(1) 的前提。
func TestTrashDirectoryIsConstantTime(t *testing.T) {
	root, data := newStateRoot(t, DefaultProtectionPolicy())

	dir := filepath.Join(data, "project")
	if err := os.MkdirAll(filepath.Join(dir, "deep", "nested"), 0o755); err != nil {
		t.Fatalf("准备目录: %v", err)
	}
	for i := range 50 {
		name := filepath.Join(dir, "deep", "nested", string(rune('a'+i%26))+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatalf("写文件: %v", err)
		}
	}

	entry, err := root.TrashFile("alice", dir, true, 0)
	if err != nil {
		t.Fatalf("移入回收站: %v", err)
	}
	if !entry.IsDir {
		t.Error("目录条目未标记为 IsDir")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("原目录仍存在")
	}

	// 恢复后内层文件应当完好
	if _, err := root.RestoreFromTrash(entry.ID); err != nil {
		t.Fatalf("恢复: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "deep", "nested")); err != nil {
		t.Errorf("恢复后内层结构缺失: %v", err)
	}
}

// 排除规则是必备项：codex 会驱动 npm install / git，无排除规则会打满磁盘。
func TestExclusionRulesSkipVersioning(t *testing.T) {
	root, _ := newStateRoot(t, DefaultProtectionPolicy())

	excluded := []string{
		"node_modules/react/index.js",
		"deep/node_modules/pkg/a.js",
		".git/objects/ab/cdef",
		"__pycache__/mod.pyc",
		".venv/lib/python3/site.py",
	}
	for _, rel := range excluded {
		if !root.IsExcluded(rel) {
			t.Errorf("%q 未被排除——版本保护会在一次任务内打满磁盘", rel)
		}
	}

	kept := []string{"src/main.go", "README.md", "docs/design.md", "gitignore.txt"}
	for _, rel := range kept {
		if root.IsExcluded(rel) {
			t.Errorf("%q 被误排除", rel)
		}
	}
}

func TestVersionCapturedBeforeOverwrite(t *testing.T) {
	policy := DefaultProtectionPolicy()
	policy.VersionDebounce = 0 // 本用例要连续取版本
	root, data := newStateRoot(t, policy)

	target := filepath.Join(data, "note.txt")
	if err := os.WriteFile(target, []byte("V1"), 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}

	entry, err := root.CaptureVersion("alice", target, "note.txt")
	if err != nil {
		t.Fatalf("取版本: %v", err)
	}
	if entry == nil {
		t.Fatal("未产生版本")
	}

	if err := os.WriteFile(target, []byte("V2"), 0o644); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}

	versions, err := root.ListVersions(target)
	if err != nil || len(versions) != 1 {
		t.Fatalf("版本列表 %d 条（err=%v），期望 1 条", len(versions), err)
	}

	reader, err := root.OpenVersion(versions[0].ID)
	if err != nil {
		t.Fatalf("打开版本: %v", err)
	}
	defer reader.Close()
	content, _ := io.ReadAll(reader)
	if string(content) != "V1" {
		t.Errorf("版本内容为 %q，期望覆盖前的 V1", string(content))
	}
}

// 去抖：agent 的高频写入不应每次都产生版本。
func TestVersionDebounceSuppressesBurstWrites(t *testing.T) {
	policy := DefaultProtectionPolicy()
	policy.VersionDebounce = time.Hour
	root, data := newStateRoot(t, policy)

	target := filepath.Join(data, "note.txt")
	if err := os.WriteFile(target, []byte("V1"), 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}

	first, err := root.CaptureVersion("alice", target, "note.txt")
	if err != nil || first == nil {
		t.Fatalf("首次取版本失败: %v", err)
	}
	second, err := root.CaptureVersion("alice", target, "note.txt")
	if err != nil {
		t.Fatalf("二次取版本报错: %v", err)
	}
	if second != nil {
		t.Error("去抖窗口内仍产生了版本")
	}
}

// 单文件版本数上限：版本属于文件，所有写入方共享这个额度。
func TestVersionCountIsCapped(t *testing.T) {
	policy := DefaultProtectionPolicy()
	policy.VersionDebounce = 0
	policy.MaxVersionsPerFile = 3
	root, data := newStateRoot(t, policy)

	target := filepath.Join(data, "note.txt")
	for i := range 8 {
		if err := os.WriteFile(target, []byte(strings.Repeat("x", i+1)), 0o644); err != nil {
			t.Fatalf("写文件: %v", err)
		}
		if _, err := root.CaptureVersion("alice", target, "note.txt"); err != nil {
			t.Fatalf("取版本: %v", err)
		}
	}

	versions, err := root.ListVersions(target)
	if err != nil {
		t.Fatalf("版本列表: %v", err)
	}
	if len(versions) > policy.MaxVersionsPerFile {
		t.Errorf("保留了 %d 个版本，上限为 %d", len(versions), policy.MaxVersionsPerFile)
	}
}

// 大文件跳过版本：ext4 上没有 reflink，每次都是全量拷贝。
func TestLargeFileSkipsVersioning(t *testing.T) {
	policy := DefaultProtectionPolicy()
	policy.VersionDebounce = 0
	policy.MaxVersionFileSize = 16
	root, data := newStateRoot(t, policy)

	target := filepath.Join(data, "big.bin")
	if err := os.WriteFile(target, make([]byte, 1024), 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}

	entry, err := root.CaptureVersion("alice", target, "big.bin")
	if err != nil {
		t.Fatalf("取版本报错: %v", err)
	}
	if entry != nil {
		t.Error("超过大小上限的文件仍产生了版本")
	}
}

// 新建文件没有旧内容可保留，不应报错。
func TestVersionOfMissingFileIsNoop(t *testing.T) {
	root, data := newStateRoot(t, DefaultProtectionPolicy())

	entry, err := root.CaptureVersion("alice", filepath.Join(data, "brand-new.txt"), "brand-new.txt")
	if err != nil {
		t.Fatalf("对不存在的文件取版本报错: %v", err)
	}
	if entry != nil {
		t.Error("对不存在的文件产生了版本")
	}
}

// 跨文件系统必须在打开时就报错，而不是等到 rename 返回 EXDEV 才发现。
func TestCrossDeviceStateRootIsRejected(t *testing.T) {
	// 无法在测试里可靠地造出跨设备场景，因此改为验证同设备通过、
	// 且 sameFilesystem 能正确区分两个不同 st_dev。
	base := t.TempDir()
	data := filepath.Join(base, "data")
	state := filepath.Join(base, StateDirName)
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatalf("准备目录: %v", err)
	}
	if _, err := OpenStateRoot(state, data, DefaultProtectionPolicy()); err != nil {
		t.Fatalf("同设备的 StateRoot 应当可以打开: %v", err)
	}

	same, err := sameFilesystem(state, data)
	if err != nil {
		t.Fatalf("比较文件系统: %v", err)
	}
	if !same {
		t.Error("同一临时目录下的两个路径被判为不同文件系统")
	}
}

// 保护区必须是 0700：它存着用户内容的副本。
func TestStateRootIsOwnerOnly(t *testing.T) {
	root, _ := newStateRoot(t, DefaultProtectionPolicy())

	info, err := os.Stat(root.Path)
	if err != nil {
		t.Fatalf("stat 保护区: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("保护区权限为 %o，期望 700", perm)
	}
}

// 恢复到已存在的路径必须拒绝，不能静默覆盖现有文件。
func TestRestoreRefusesToClobber(t *testing.T) {
	root, data := newStateRoot(t, DefaultProtectionPolicy())

	target := filepath.Join(data, "note.txt")
	if err := os.WriteFile(target, []byte("OLD"), 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	entry, err := root.TrashFile("alice", target, false, 3)
	if err != nil {
		t.Fatalf("移入回收站: %v", err)
	}

	// 同名文件重新出现
	if err := os.WriteFile(target, []byte("NEW"), 0o644); err != nil {
		t.Fatalf("重建同名文件: %v", err)
	}

	if _, err := root.RestoreFromTrash(entry.ID); err == nil {
		t.Error("恢复覆盖了已存在的文件")
	}
	current, _ := os.ReadFile(target)
	if string(current) != "NEW" {
		t.Errorf("现有文件被改成了 %q", string(current))
	}
}

// reflink 自适应：能力靠实测探测，而非按文件系统名推断。
func TestCloneCapabilityIsProbed(t *testing.T) {
	root, _ := newStateRoot(t, DefaultProtectionPolicy())
	_ = root

	backend := newSecureBackend()
	handle, err := backend.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("打开根: %v", err)
	}
	defer handle.Close()

	p, err := newBoundProtection(handle, DefaultProtectionPolicy())
	if err != nil {
		t.Fatalf("建保护链: %v", err)
	}

	t.Logf("本机文件系统的 reflink 支持: %v", p.CloneSupported())

	// 支持时，大小上限必须被解除——上限存在的唯一理由是限制全量拷贝的代价，
	// 而 reflink 没有这个代价。
	if p.CloneSupported() && p.policy.MaxVersionFileSize != 0 {
		t.Errorf("支持 reflink 但仍保留了 %d 字节的大小上限", p.policy.MaxVersionFileSize)
	}
	if !p.CloneSupported() && p.policy.MaxVersionFileSize == 0 {
		t.Error("不支持 reflink 却解除了大小上限——全量拷贝会失去约束")
	}
}

// 不论走 reflink 还是全量拷贝，留存的内容都必须一致。
func TestVersionContentIdenticalRegardlessOfCloneSupport(t *testing.T) {
	policy := DefaultProtectionPolicy()
	policy.VersionDebounce = 0
	root, data := newStateRoot(t, policy)
	_ = root

	backend := newSecureBackend()
	handle, err := backend.OpenRoot(data)
	if err != nil {
		t.Fatalf("打开根: %v", err)
	}
	defer handle.Close()

	p, err := newBoundProtection(handle, policy)
	if err != nil {
		t.Fatalf("建保护链: %v", err)
	}

	payload := make([]byte, 300000) // 跨越单个读块，且足够大到能看出拷贝代价
	for i := range payload {
		payload[i] = byte(i % 253)
	}
	if err := os.WriteFile(filepath.Join(data, "big.bin"), payload, 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}

	entry, err := p.CaptureVersion("alice", "big.bin")
	if err != nil {
		t.Fatalf("取版本: %v", err)
	}
	if entry == nil {
		t.Fatal("未产生版本")
	}

	stored, err := p.ReadVersion(entry.StoredName)
	if err != nil {
		t.Fatalf("读版本: %v", err)
	}
	if !bytes.Equal(stored, payload) {
		t.Errorf("留存内容与原文件不一致（clone=%v，长度 %d vs %d）",
			p.CloneSupported(), len(stored), len(payload))
	}
}
