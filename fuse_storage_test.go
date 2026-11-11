//go:build linux || darwin

package toolfs

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IceWhaleTech/ZimaOS-Common/permission"
)

func TestMountStorageRootTracksTopologyWithoutDetachingCWD(t *testing.T) {
	requireFUSE(t)
	base, target := t.TempDir(), t.TempDir()
	for _, name := range []string{"Disk", "Backup"} {
		if err := os.Mkdir(filepath.Join(base, name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, name, "marker"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(target, filepath.Join(base, "System")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "marker"), []byte("system"), 0600); err != nil {
		t.Fatal(err)
	}
	homes := filepath.Join(target, ".home")
	for _, user := range []string{"alice", "bob"} {
		if err := os.MkdirAll(filepath.Join(homes, user), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(homes, user, "marker"), []byte(user), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(homes, filepath.Join(base, "Home")); err != nil {
		t.Fatal(err)
	}
	a := newFakeAuthority()
	a.allow["/media"] = []string{"read", "write", "delete"}
	a.allow["/media/System/.home/alice"] = []string{"read", "write"}
	a.deny["/media/System/.home"] = []string{"read", "write", "delete"}
	a.pathAliases = []permission.PathAlias{
		{Canonical: "/media", Alias: base},
		{Canonical: "/media/Disk", Alias: filepath.Join(base, "Disk")},
		{Canonical: "/media/Backup", Alias: filepath.Join(base, "Backup")},
		{Canonical: "/media/System", Alias: target},
		{Canonical: "/media/System/.home", Alias: "/media/Home"},
	}
	a.entries = []PermissionEntry{{Kind: "home", Name: "Home", Path: "/media/Home", ViewPath: "/Home", DefaultPath: "/media/Home/alice"}}
	for _, name := range []string{"Disk", "Backup", "System"} {
		a.entries = append(a.entries, PermissionEntry{Kind: "storage", Name: name, Path: "/media/" + name, ViewPath: "/" + name, DefaultPath: "/media/" + name})
	}
	if err := os.MkdirAll(filepath.Join(base, "Restricted/Projects/A"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "Restricted/Projects/A/marker"), []byte("shared"), 0600); err != nil {
		t.Fatal(err)
	}
	a.deny["/media/Restricted"] = []string{"read", "write", "delete"}
	a.allow["/media/Restricted/Projects/A"] = []string{"read"}
	a.entries = append(a.entries, PermissionEntry{Kind: "storage", Name: "Restricted", Path: "/media/Restricted", ViewPath: "/Restricted", DefaultPath: "/media/Restricted", TraverseOnly: true})
	p := startProvider(t, a)
	identity := ZimaIdentity{Username: "alice", UserID: 7, Role: "admin"}
	caps := AgentCapabilities{}
	mounts, err := NewMountSet(MountSetConfig{Root: t.TempDir(), DefaultCaps: caps}, NewStaticIdentityResolver(nil), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mounts.Close(); err != nil {
			t.Errorf("unmount storage fixture: %v", err)
		}
	})
	projected := make(chan struct{}, 8)
	p.OnChange(func(int64) { mounts.Reproject(t.Context()); projected <- struct{}{} })
	mount, err := mounts.Mount(t.Context(), "7", "", &identity, nil)
	if err != nil {
		t.Fatalf("mount storage authority fixture: %v", err)
	}
	assertDirNames(t, "initial", mount.MountPoint, []string{"Backup", "Disk", "Home", "Restricted", "System"})
	assertDirNames(t, "own home", filepath.Join(mount.MountPoint, "Home"), []string{"alice"})
	for _, rel := range []string{"Home/bob/marker", "System/.home/bob/marker"} {
		if _, err := os.ReadFile(filepath.Join(mount.MountPoint, rel)); err == nil {
			t.Fatalf("personal-space collection exposed %s", rel)
		}
	}
	if data, err := os.ReadFile(filepath.Join(mount.MountPoint, "System/marker")); err != nil || string(data) != "system" {
		t.Fatalf("standard alias: %q %v", data, err)
	}
	assertDirNames(t, "restricted root", filepath.Join(mount.MountPoint, "Restricted"), []string{"Projects"})
	assertDirNames(t, "restricted ancestor", filepath.Join(mount.MountPoint, "Restricted/Projects"), []string{"A"})
	if data, err := os.ReadFile(filepath.Join(mount.MountPoint, "Restricted/Projects/A/marker")); err != nil || string(data) != "shared" {
		t.Fatalf("deep share: %q %v", data, err)
	}
	diskPath := filepath.Join(mount.MountPoint, "Disk")
	diskDir, err := os.Open(diskPath)
	if err != nil {
		t.Fatal(err)
	}
	defer diskDir.Close()
	diskFile, err := os.Open(filepath.Join(diskPath, "marker"))
	if err != nil {
		t.Fatal(err)
	}
	defer diskFile.Close()
	before, err := os.Stat(diskPath)
	if err != nil {
		t.Fatal(err)
	}
	// The child process really holds its cwd across a topology change. It
	// checks getcwd after the parent has looked the same directory up again.
	helperCtx, stopHelper := context.WithTimeout(t.Context(), 10*time.Second)
	defer stopHelper()
	cwd := exec.CommandContext(helperCtx, "sh", "-c", `printf 'ready\n'; read line; pwd -P; cat marker`)
	cwd.Dir = diskPath
	input, err := cwd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cwd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cwd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = cwd.Process.Kill() })
	ready := make([]byte, len("ready\n"))
	if _, err := io.ReadFull(output, ready); err != nil || string(ready) != "ready\n" {
		t.Fatalf("cwd helper not ready: %q %v", ready, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); mounts.WatchStorage(ctx, 10*time.Millisecond) }()
	t.Cleanup(func() { cancel(); <-done })
	if err := os.Mkdir(filepath.Join(base, "New"), 0700); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.entries = append(a.entries, PermissionEntry{Kind: "storage", Name: "New", Path: "/media/New", ViewPath: "/New", DefaultPath: "/media/New"})
	a.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(mount.MountPoint, "New")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new disk did not appear without permission generation change")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if err := os.Rename(filepath.Join(base, "Backup"), filepath.Join(base, ".old-backup")); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	kept := a.entries[:0]
	for _, entry := range a.entries {
		if entry.Path != "/media/Backup" {
			kept = append(kept, entry)
		}
	}
	a.entries = kept
	a.mu.Unlock()
	mounts.Reproject(t.Context())
	assertDirNames(t, "changed", mount.MountPoint, []string{"Disk", "Home", "New", "Restricted", "System"})
	after, err := os.Stat(diskPath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("unaffected disk inode changed: %v", err)
	}
	if _, err := diskDir.Readdirnames(-1); err != nil {
		t.Fatalf("held disk directory closed by unrelated topology: %v", err)
	}
	if data, err := io.ReadAll(diskFile); err != nil || string(data) != "Disk" {
		t.Fatalf("held disk file failed: %q %v", data, err)
	}
	if _, err := io.WriteString(input, "continue\n"); err != nil {
		t.Fatal(err)
	}
	_ = input.Close()
	cwdOutput, err := io.ReadAll(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := cwd.Wait(); err != nil || !strings.Contains(string(cwdOutput), diskPath+"\nDisk") {
		t.Fatalf("cwd detached after unrelated disk changes: %q %v", cwdOutput, err)
	}
	if err := os.Rename(filepath.Join(base, "Disk"), filepath.Join(base, ".old-disk")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "Disk"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "Disk/marker"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	mounts.Reproject(t.Context())
	if data, err := os.ReadFile(filepath.Join(diskPath, "marker")); err != nil || string(data) != "replacement" {
		t.Fatalf("same-name replacement not opened: %q %v", data, err)
	}
	if _, err := diskFile.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if data, err := io.ReadAll(diskFile); err == nil {
		t.Fatalf("retired disk descriptor still readable: %q", data)
	}
	if p.client.Generation() != 1 {
		t.Fatal("topology faked an authority generation change")
	}
	a.change(func(allow, deny map[string][]string) {
		deny["/media/Disk"] = []string{"read"}
	})
	select {
	case <-projected:
	case <-time.After(5 * time.Second):
		t.Fatal("authority change did not reach view")
	}
	if data, err := os.ReadFile(filepath.Join(diskPath, "marker")); err == nil {
		t.Fatalf("revoked storage remains readable: %q", data)
	}
}
