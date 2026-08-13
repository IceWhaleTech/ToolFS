//go:build linux || darwin
// +build linux darwin

package toolfs

// Retention tests.
//
// The property that matters most here is negative: a purge must never remove
// something the user deleted moments ago, however tight space is. Reclaiming
// space is worth less than the recovery guarantee that justifies the trash.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ageTrashEntry rewrites an entry's timestamp so retention can be tested
// without waiting.
func ageTrashEntry(t *testing.T, p *boundProtection, id string, age time.Duration) {
	t.Helper()
	var entry TrashEntry
	if err := p.readMeta("trash", id, &entry); err != nil {
		t.Fatalf("读回收站元数据: %v", err)
	}
	entry.DeletedAt = time.Now().Add(-age)
	if err := p.writeMeta("trash", id, entry); err != nil {
		t.Fatalf("写回收站元数据: %v", err)
	}
}

// 刚删除的条目绝不能被自动清理掉。
func TestPurgeNeverRemovesRecentDeletions(t *testing.T) {
	p, data := newProtection(t, DefaultProtectionPolicy())
	write(t, data, "just-deleted.txt", "IMPORTANT")

	entry, err := p.Trash("alice", "just-deleted.txt")
	if err != nil {
		t.Fatalf("移入回收站: %v", err)
	}

	// 极端策略：要求腾出海量空间、且总量上限为 0——仍然不该动新条目
	report, err := p.PurgeTrash(RetentionPolicy{
		MinAge:          24 * time.Hour,
		MaxTotalBytes:   1,
		TargetFreeBytes: 1 << 62,
	})
	if err != nil {
		t.Fatalf("清理: %v", err)
	}
	if report.TrashRemoved != 0 {
		t.Errorf("刚删除的条目被清掉了（移除 %d 条）——MinAge 未被尊重", report.TrashRemoved)
	}
	if report.KeptYoung == 0 {
		t.Error("报告未说明有条目因过新而保留")
	}
	if _, err := p.Restore(entry.ID); err != nil {
		t.Errorf("清理后已无法恢复: %v", err)
	}
}

// 超过 MaxAge 的条目应被清掉。
func TestPurgeRemovesExpiredEntries(t *testing.T) {
	p, data := newProtection(t, DefaultProtectionPolicy())
	write(t, data, "old.txt", "STALE")

	entry, err := p.Trash("alice", "old.txt")
	if err != nil {
		t.Fatalf("移入回收站: %v", err)
	}
	ageTrashEntry(t, p, entry.ID, 40*24*time.Hour)

	report, err := p.PurgeTrash(RetentionPolicy{MinAge: time.Hour, MaxAge: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("清理: %v", err)
	}
	if report.TrashRemoved != 1 {
		t.Errorf("过期条目未被清理（移除 %d 条）", report.TrashRemoved)
	}
	if _, err := p.Restore(entry.ID); err == nil {
		t.Error("已清理的条目仍能恢复")
	}
}

// 目录条目的清理必须递归，否则空间根本没释放。
func TestPurgeRemovesTrashedDirectoryTree(t *testing.T) {
	p, data := newProtection(t, DefaultProtectionPolicy())
	for i := range 5 {
		write(t, data, filepath.Join("proj", "sub", string(rune('a'+i))+".txt"), "content")
	}

	entry, err := p.Trash("alice", "proj")
	if err != nil {
		t.Fatalf("移入回收站: %v", err)
	}
	ageTrashEntry(t, p, entry.ID, 40*24*time.Hour)

	report, err := p.PurgeTrash(RetentionPolicy{MinAge: time.Hour, MaxAge: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("清理: %v", err)
	}
	if report.TrashRemoved != 1 {
		t.Fatalf("目录条目未被清理（移除 %d 条）", report.TrashRemoved)
	}
	if _, err := os.Stat(filepath.Join(data, toolfsStateDir, "trash")); err != nil {
		t.Fatalf("回收站目录本身不应被删: %v", err)
	}
	items, _ := os.ReadDir(filepath.Join(data, toolfsStateDir, "trash"))
	if len(items) != 0 {
		t.Errorf("回收站仍残留 %d 项，目录树未被递归删除", len(items))
	}
}

// 空间充足时不做任何清理。
func TestPurgeIfLowOnSpaceIsNoopWhenAmple(t *testing.T) {
	p, data := newProtection(t, DefaultProtectionPolicy())
	write(t, data, "f.txt", "x")
	if _, err := p.Trash("alice", "f.txt"); err != nil {
		t.Fatalf("移入回收站: %v", err)
	}

	report, err := p.PurgeIfLowOnSpace(RetentionPolicy{MinAge: time.Hour, TargetFreeBytes: 1})
	if err != nil {
		t.Fatalf("检查: %v", err)
	}
	if report != nil {
		t.Errorf("空间充足却执行了清理: %+v", report)
	}
}

// 低空间时版本留存降级，但删除仍进回收站。
func TestLowSpaceDegradesVersioningNotTrash(t *testing.T) {
	policy := DefaultProtectionPolicy()
	policy.VersionDebounce = 0
	policy.MinFreeBytes = 1 << 62 // 必然低于阈值
	p, data := newProtection(t, policy)
	write(t, data, "note.txt", "V1")

	entry, err := p.CaptureVersion("alice", "note.txt")
	if err != nil {
		t.Fatalf("取版本报错: %v", err)
	}
	if p.CloneSupported() {
		// reflink 不占空间，低空间不应影响它
		if entry == nil {
			t.Error("支持 reflink 时不应因低空间跳过版本")
		}
	} else if entry != nil {
		t.Error("低空间下仍留存了版本——降级未生效")
	}

	// 删除必须照常可用：它是 rename，不占空间
	if _, err := p.Trash("alice", "note.txt"); err != nil {
		t.Errorf("低空间下删除失败，回收站不应受影响: %v", err)
	}
}

// 用量统计应涵盖回收站与版本。
func TestUsageBytesCoversBothStores(t *testing.T) {
	policy := DefaultProtectionPolicy()
	policy.VersionDebounce = 0
	p, data := newProtection(t, policy)

	write(t, data, "a.txt", "0123456789")
	if _, err := p.CaptureVersion("alice", "a.txt"); err != nil {
		t.Fatalf("取版本: %v", err)
	}
	write(t, data, "b.txt", "0123456789")
	if _, err := p.Trash("alice", "b.txt"); err != nil {
		t.Fatalf("移入回收站: %v", err)
	}

	used, err := p.UsageBytes()
	if err != nil {
		t.Fatalf("统计用量: %v", err)
	}
	if used < 20 {
		t.Errorf("用量统计为 %d，期望至少涵盖两份各 10 字节的内容", used)
	}
}
