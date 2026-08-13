//go:build linux || darwin
// +build linux darwin

package toolfs

// P3 protection chain tests.
//
// These exercise boundProtection — the implementation the FUSE path actually
// uses. An earlier path-based StateRoot was tested here instead; it has been
// removed, because testing the copy that does not run is how the low-space
// guard came to be missing from the one that does.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newProtection builds a protection chain over a fresh backing directory.
func newProtection(t *testing.T, policy ProtectionPolicy) (*boundProtection, string) {
	t.Helper()
	data := t.TempDir()

	handle, err := newSecureBackend().OpenRoot(data)
	if err != nil {
		t.Fatalf("打开根: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	p, err := newBoundProtection(handle, policy)
	if err != nil {
		t.Fatalf("建保护链: %v", err)
	}
	return p, data
}

// write puts a file into the backing directory, bypassing the protection chain.
func write(t *testing.T, data, rel, content string) {
	t.Helper()
	full := filepath.Join(data, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("准备目录: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}
}

func TestTrashIsRenameAndRestorable(t *testing.T) {
	p, data := newProtection(t, DefaultProtectionPolicy())
	write(t, data, "report.md", "ORIGINAL")

	entry, err := p.Trash("alice", "report.md")
	if err != nil {
		t.Fatalf("移入回收站: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "report.md")); !os.IsNotExist(err) {
		t.Error("原路径仍存在，删除未生效")
	}

	if _, err := p.Restore(entry.ID); err != nil {
		t.Fatalf("恢复: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(data, "report.md"))
	if err != nil {
		t.Fatalf("回读: %v", err)
	}
	if string(got) != "ORIGINAL" {
		t.Errorf("恢复后内容为 %q，期望 ORIGINAL", string(got))
	}
}

// 递归删目录只 rename 目录本身——这是 O(1) 的前提。
func TestTrashDirectoryIsConstantTime(t *testing.T) {
	p, data := newProtection(t, DefaultProtectionPolicy())
	for i := range 50 {
		write(t, data, filepath.Join("project", "deep", "f"+string(rune('a'+i%26))+".txt"), "x")
	}

	entry, err := p.Trash("alice", "project")
	if err != nil {
		t.Fatalf("移入回收站: %v", err)
	}
	if !entry.IsDir {
		t.Error("目录条目未标记为 IsDir")
	}
	if _, err := p.Restore(entry.ID); err != nil {
		t.Fatalf("恢复: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "project", "deep")); err != nil {
		t.Errorf("恢复后内层结构缺失: %v", err)
	}
}

// 排除规则是必备项：codex 会驱动 npm install / git，无排除规则会打满磁盘。
func TestExclusionRulesSkipVersioning(t *testing.T) {
	p, _ := newProtection(t, DefaultProtectionPolicy())

	for _, rel := range []string{
		"node_modules/react/index.js", "deep/node_modules/pkg/a.js",
		".git/objects/ab/cdef", "__pycache__/mod.pyc", ".venv/lib/site.py",
	} {
		if !p.excluded(rel) {
			t.Errorf("%q 未被排除——版本保护会在一次任务内打满磁盘", rel)
		}
	}
	for _, rel := range []string{"src/main.go", "README.md", "gitignore.txt"} {
		if p.excluded(rel) {
			t.Errorf("%q 被误排除", rel)
		}
	}
}

func TestVersionCapturedBeforeOverwrite(t *testing.T) {
	policy := DefaultProtectionPolicy()
	policy.VersionDebounce = 0
	p, data := newProtection(t, policy)
	write(t, data, "note.txt", "V1")

	entry, err := p.CaptureVersion("alice", "note.txt")
	if err != nil || entry == nil {
		t.Fatalf("取版本失败: %v", err)
	}
	write(t, data, "note.txt", "V2")

	content, err := p.ReadVersion(entry.StoredName)
	if err != nil {
		t.Fatalf("读版本: %v", err)
	}
	if string(content) != "V1" {
		t.Errorf("版本内容为 %q，期望覆盖前的 V1", string(content))
	}
}

// 去抖：agent 的突发写入不应每次都产生版本。
func TestVersionDebounceSuppressesBurstWrites(t *testing.T) {
	policy := DefaultProtectionPolicy()
	policy.VersionDebounce = time.Hour
	p, data := newProtection(t, policy)
	write(t, data, "note.txt", "V1")

	if first, err := p.CaptureVersion("alice", "note.txt"); err != nil || first == nil {
		t.Fatalf("首次取版本失败: %v", err)
	}
	second, err := p.CaptureVersion("alice", "note.txt")
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
	p, data := newProtection(t, policy)

	for i := range 8 {
		write(t, data, "note.txt", string(rune('a'+i)))
		if _, err := p.CaptureVersion("alice", "note.txt"); err != nil {
			t.Fatalf("取版本: %v", err)
		}
	}
	versions, err := p.ListVersions("note.txt")
	if err != nil {
		t.Fatalf("版本列表: %v", err)
	}
	if len(versions) > policy.MaxVersionsPerFile {
		t.Errorf("保留了 %d 个版本，上限为 %d", len(versions), policy.MaxVersionsPerFile)
	}
}

// 大文件跳过版本——仅在没有 reflink 时；有 reflink 时上限已被自动解除。
func TestLargeFileSkipsVersioningWithoutReflink(t *testing.T) {
	policy := DefaultProtectionPolicy()
	policy.VersionDebounce = 0
	policy.MaxVersionFileSize = 16
	p, data := newProtection(t, policy)
	write(t, data, "big.bin", string(make([]byte, 1024)))

	entry, err := p.CaptureVersion("alice", "big.bin")
	if err != nil {
		t.Fatalf("取版本报错: %v", err)
	}
	if p.CloneSupported() {
		if entry == nil {
			t.Error("支持 reflink 时大文件仍被跳过——上限本应自动解除")
		}
		return
	}
	if entry != nil {
		t.Error("无 reflink 时超过大小上限的文件仍产生了版本")
	}
}

// 新建文件没有旧内容可留，不应报错。
func TestVersionOfMissingFileIsNoop(t *testing.T) {
	p, _ := newProtection(t, DefaultProtectionPolicy())

	entry, err := p.CaptureVersion("alice", "brand-new.txt")
	if err != nil {
		t.Fatalf("对不存在的文件取版本报错: %v", err)
	}
	if entry != nil {
		t.Error("对不存在的文件产生了版本")
	}
}

// 恢复到已存在的路径必须拒绝，不能静默覆盖。
func TestRestoreRefusesToClobber(t *testing.T) {
	p, data := newProtection(t, DefaultProtectionPolicy())
	write(t, data, "note.txt", "OLD")

	entry, err := p.Trash("alice", "note.txt")
	if err != nil {
		t.Fatalf("移入回收站: %v", err)
	}
	write(t, data, "note.txt", "NEW")

	if _, err := p.Restore(entry.ID); err == nil {
		t.Error("恢复覆盖了已存在的文件")
	}
	current, _ := os.ReadFile(filepath.Join(data, "note.txt"))
	if string(current) != "NEW" {
		t.Errorf("现有文件被改成了 %q", string(current))
	}
}

// reflink 自适应：能力靠实测探测，而非按文件系统名推断。
func TestCloneCapabilityIsProbed(t *testing.T) {
	p, _ := newProtection(t, DefaultProtectionPolicy())
	t.Logf("本机文件系统的 reflink 支持: %v", p.CloneSupported())

	if p.CloneSupported() && p.policy.MaxVersionFileSize != 0 {
		t.Errorf("支持 reflink 但仍保留了 %d 字节的大小上限", p.policy.MaxVersionFileSize)
	}
	if !p.CloneSupported() && p.policy.MaxVersionFileSize == 0 {
		t.Error("不支持 reflink 却解除了大小上限——全量拷贝会失去约束")
	}
}

// 不论走 reflink 还是全量拷贝，留存内容都必须一致。
func TestVersionContentIdenticalRegardlessOfCloneSupport(t *testing.T) {
	policy := DefaultProtectionPolicy()
	policy.VersionDebounce = 0
	p, data := newProtection(t, policy)

	payload := make([]byte, 300000)
	for i := range payload {
		payload[i] = byte(i % 253)
	}
	if err := os.WriteFile(filepath.Join(data, "big.bin"), payload, 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}

	entry, err := p.CaptureVersion("alice", "big.bin")
	if err != nil || entry == nil {
		t.Fatalf("取版本失败: %v", err)
	}
	stored, err := p.ReadVersion(entry.StoredName)
	if err != nil {
		t.Fatalf("读版本: %v", err)
	}
	if !bytes.Equal(stored, payload) {
		t.Errorf("留存内容与原文件不一致（clone=%v）", p.CloneSupported())
	}
}
