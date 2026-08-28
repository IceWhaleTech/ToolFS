//go:build linux || darwin
// +build linux darwin

package toolfs

// Protected-mutation tests over a real mount.
//
// The point of these is that an agent shelling out to `rm` takes exactly the
// same path as one calling an API: both land on the FUSE handler.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// mountWritable mounts alice with a caps set chosen by the caller.
// It returns the backing data directory as well, so tests can inspect the
// protection area directly rather than guessing where it landed.
func mountWritable(t *testing.T, caps AgentCapabilities) (mnt, dataBase string, cleanup func()) {
	t.Helper()
	requireFUSE(t)

	dataBase, resolver, provider := twoUserFixture(t)

	mnt = filepath.Join(t.TempDir(), "u1001", "workspace")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		t.Fatalf("准备挂载点: %v", err)
	}

	handle, err := MountForIdentity(context.Background(), resolver, provider,
		"agent-user-1", "", mnt, caps, MountConfig{})
	if err != nil {
		t.Skipf("跳过：本环境无法挂载 FUSE (%v)", err)
	}
	return mnt, dataBase, func() { _ = handle.Unmount() }
}

// 默认不授予删除：agent 无法删文件，这是防误删的第一道。
func TestMutate_DeleteIsWithheldByDefault(t *testing.T) {
	mnt, _, cleanup := mountWritable(t, AgentCapabilities{})
	defer cleanup()

	target := filepath.Join(mnt, "Documents", "file.txt")
	if err := os.Remove(target); err == nil {
		t.Error("默认配置下删除成功了——删除权限必须默认不授予")
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("文件不应被删除: %v", err)
	}
}

// 显式授予删除后，删除仍然可恢复：条目进回收站而非真删。
func TestMutate_DeleteGoesToTrashWhenPermitted(t *testing.T) {
	mnt, dataBase, cleanup := mountWritable(t, AgentCapabilities{AllowDelete: true})
	defer cleanup()

	target := filepath.Join(mnt, "Documents", "file.txt")
	if err := os.Remove(target); err != nil {
		t.Fatalf("授予删除后仍失败: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("删除未生效")
	}

	// 后端目录里应当出现保护区，且原内容仍在其中
	backing := filepath.Join(dataBase, "Documents")
	trash := filepath.Join(backing, toolfsStateDir, "trash")
	items, err := os.ReadDir(trash)
	if err != nil {
		t.Fatalf("读回收站目录: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("回收站有 %d 个条目，期望 1 个", len(items))
	}
	content, err := os.ReadFile(filepath.Join(trash, items[0].Name()))
	if err != nil {
		t.Fatalf("读回收站内容: %v", err)
	}
	if string(content) != "Documents" {
		t.Errorf("回收站里的内容为 %q，期望原文件内容", string(content))
	}
}

// 覆盖写前保留旧版本。
func TestMutate_OverwriteCapturesVersion(t *testing.T) {
	mnt, dataBase, cleanup := mountWritable(t, AgentCapabilities{})
	defer cleanup()

	target := filepath.Join(mnt, "Documents", "file.txt")
	if err := os.WriteFile(target, []byte("REWRITTEN"), 0o644); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("回读: %v", err)
	}
	if string(got) != "REWRITTEN" {
		t.Errorf("写入未生效，读到 %q", string(got))
	}

	backing := filepath.Join(dataBase, "Documents")
	versions := filepath.Join(backing, toolfsStateDir, "versions")
	items, err := os.ReadDir(versions)
	if err != nil {
		t.Fatalf("读版本目录: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("版本目录有 %d 个条目，期望 1 个", len(items))
	}
	old, err := os.ReadFile(filepath.Join(versions, items[0].Name()))
	if err != nil {
		t.Fatalf("读版本内容: %v", err)
	}
	if string(old) != "Documents" {
		t.Errorf("保留的旧版本为 %q，期望覆盖前的内容", string(old))
	}
}

// 保护区必须对命名空间不可见——否则被删内容可经挂载点读回。
func TestMutate_ProtectionAreaIsInvisible(t *testing.T) {
	mnt, _, cleanup := mountWritable(t, AgentCapabilities{AllowDelete: true})
	defer cleanup()

	if err := os.Remove(filepath.Join(mnt, "Documents", "file.txt")); err != nil {
		t.Fatalf("删除: %v", err)
	}

	items, err := os.ReadDir(filepath.Join(mnt, "Documents"))
	if err != nil {
		t.Fatalf("列目录: %v", err)
	}
	for _, item := range items {
		if item.Name() == toolfsStateDir {
			t.Error("保护区出现在了挂载点的目录列表里")
		}
	}
	if _, err := os.Stat(filepath.Join(mnt, "Documents", toolfsStateDir)); !os.IsNotExist(err) {
		t.Errorf("保护区可经挂载点 stat 到，err=%v", err)
	}
	if _, err := os.ReadDir(filepath.Join(mnt, "Documents", toolfsStateDir, "trash")); err == nil {
		t.Error("回收站内容可经挂载点读取——被删数据仍可达")
	}
}

// 新建文件与目录。
func TestMutate_CreateFileAndDirectory(t *testing.T) {
	mnt, _, cleanup := mountWritable(t, AgentCapabilities{})
	defer cleanup()

	newFile := filepath.Join(mnt, "Documents", "created.txt")
	if err := os.WriteFile(newFile, []byte("NEW"), 0o644); err != nil {
		t.Fatalf("新建文件: %v", err)
	}
	got, err := os.ReadFile(newFile)
	if err != nil || string(got) != "NEW" {
		t.Errorf("回读新建文件得到 %q（err=%v）", string(got), err)
	}

	newDir := filepath.Join(mnt, "Documents", "subdir")
	if err := os.Mkdir(newDir, 0o755); err != nil {
		t.Fatalf("新建目录: %v", err)
	}
	info, err := os.Stat(newDir)
	if err != nil || !info.IsDir() {
		t.Errorf("新建目录未生效: %v", err)
	}
}

// 只读 Grant 上的写入必须被拒。
func TestMutate_ReadOnlyGrantRejectsWrites(t *testing.T) {
	requireFUSE(t)

	base := t.TempDir()
	roDir := filepath.Join(base, "ReadOnly")
	if err := os.MkdirAll(roDir, 0o755); err != nil {
		t.Fatalf("准备目录: %v", err)
	}
	if err := os.WriteFile(filepath.Join(roDir, "doc.txt"), []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}

	resolver := NewStaticIdentityResolver(map[string]ZimaIdentity{
		"agent-ro": {Username: "alice", UserID: 1001, Role: "user"},
	})
	provider := NewStaticPermissionProvider(map[string][]Grant{
		"alice": {{ID: 1, Name: "ReadOnly", Path: roDir, ReadOnly: true}},
	})

	mnt := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		t.Fatalf("准备挂载点: %v", err)
	}
	handle, err := MountForIdentity(context.Background(), resolver, provider,
		"agent-ro", "", mnt, AgentCapabilities{AllowDelete: true}, MountConfig{})
	if err != nil {
		t.Skipf("跳过：本环境无法挂载 FUSE (%v)", err)
	}
	defer func() { _ = handle.Unmount() }()

	target := filepath.Join(mnt, "ReadOnly", "doc.txt")
	if err := os.WriteFile(target, []byte("HACKED"), 0o644); err == nil {
		t.Error("只读 Grant 上写入成功了")
	}
	if err := os.Remove(target); err == nil {
		t.Error("只读 Grant 上删除成功了——AllowDelete 不应能提升只读 Grant")
	}

	// 只读 Grant 不应被创建保护区（那会是 ToolFS 对它的唯一一次写）
	if _, err := os.Stat(filepath.Join(roDir, toolfsStateDir)); !os.IsNotExist(err) {
		t.Error("只读 Grant 下创建了保护区")
	}

	content, _ := os.ReadFile(filepath.Join(roDir, "doc.txt"))
	if string(content) != "ORIGINAL" {
		t.Errorf("只读区文件被改成了 %q", string(content))
	}
}

// 大文件读写。
//
// 这一条是被压测暴露出来的：此前 Read 返回 h.data[off:] 而忽略 len(dest)，
// 内核按 4KB/128KB 分块请求时应答超长，写回失败（EINVAL），表现为
// 【任何大于单次读块的文件都读成空】。既有用例全部使用几字节的小文件，
// 恰好都小于一个读块，因此完全没有覆盖到。
func TestMutate_LargeFileRoundTrip(t *testing.T) {
	mnt, dataBase, cleanup := mountWritable(t, AgentCapabilities{})
	defer cleanup()

	// 刻意跨越常见读块大小（4KB、128KB）
	for _, size := range []int{4096, 4097, 128 << 10, (128 << 10) + 1, 1 << 20} {
		name := fmt.Sprintf("size-%d", size)
		t.Run(name, func(t *testing.T) {
			payload := make([]byte, size)
			for i := range payload {
				payload[i] = byte(i % 251)
			}

			target := filepath.Join(mnt, "Documents", name+".bin")
			if err := os.WriteFile(target, payload, 0o644); err != nil {
				t.Fatalf("写入 %d 字节: %v", size, err)
			}

			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("读回 %d 字节: %v", size, err)
			}
			if len(got) != size {
				t.Fatalf("读回 %d 字节，期望 %d —— 大于一个读块的文件被读成了空/截断", len(got), size)
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("内容不一致（长度对但字节不同）")
			}

			// 后端落盘也必须一致
			backing := filepath.Join(dataBase, "Documents", name+".bin")
			onDisk, err := os.ReadFile(backing)
			if err != nil {
				t.Fatalf("读后端: %v", err)
			}
			if !bytes.Equal(onDisk, payload) {
				t.Errorf("后端落盘内容与写入不一致")
			}
		})
	}
}

// 随机偏移写入（非顺序）——流式写句柄用 WriteAt，必须支持。
func TestMutate_RandomOffsetWrites(t *testing.T) {
	mnt, _, cleanup := mountWritable(t, AgentCapabilities{})
	defer cleanup()

	target := filepath.Join(mnt, "Documents", "sparse.bin")
	f, err := os.OpenFile(target, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("打开: %v", err)
	}
	// 先写靠后的偏移，再写靠前的
	if _, err := f.WriteAt([]byte("TAIL"), 8192); err != nil {
		t.Fatalf("写偏移 8192: %v", err)
	}
	if _, err := f.WriteAt([]byte("HEAD"), 0); err != nil {
		t.Fatalf("写偏移 0: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if len(got) != 8196 {
		t.Fatalf("文件长度 %d，期望 8196", len(got))
	}
	if string(got[:4]) != "HEAD" || string(got[8192:]) != "TAIL" {
		t.Errorf("偏移写入的内容不正确：头 %q 尾 %q", got[:4], got[8192:])
	}
}
