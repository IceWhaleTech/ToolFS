//go:build linux || darwin

package toolfs

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMountAgentLastReadRevocationDeniesFileContents(t *testing.T) {
	requireFUSE(t)
	root := t.TempDir()
	for _, name := range []string{"allowed", "blocked"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "marker"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, a := newAgentScopeProvider(t)
	a.mu.Lock()
	a.root = root
	a.mu.Unlock()
	identity := ZimaIdentity{Username: "alice", UserID: 7}
	mounts, err := NewMountSet(MountSetConfig{
		Root:        t.TempDir(),
		DefaultCaps: AgentCapabilities{ViewAliases: []PathAlias{{Host: root, View: "/Docs"}}},
	}, NewStaticIdentityResolver(map[string]ZimaIdentity{"7": identity}), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mounts.Close(); err != nil {
			t.Errorf("unmount fixture: %v", err)
		}
	})
	projected := make(chan struct{}, 8)
	p.OnChange(func(int64) {
		mounts.Reproject(context.Background())
		projected <- struct{}{}
	})
	mount, err := mounts.Mount(t.Context(), "7", "photos", &identity, nil)
	if err != nil {
		t.Fatalf("mount authority fixture: %v", err)
	}
	allowed := filepath.Join(mount.MountPoint, "Docs", "allowed", "marker")
	blocked := filepath.Join(mount.MountPoint, "Docs", "blocked", "marker")
	data, err := os.ReadFile(allowed)
	if err != nil || string(data) != "allowed" {
		t.Fatalf("positive content control: %q, %v", data, err)
	}
	if _, err := os.ReadFile(blocked); err == nil {
		t.Fatal("ungranted file contents readable before revocation")
	}
	open, err := os.Open(allowed)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	a.change(t, p, "path", func() { a.read = false })
	select {
	case <-projected:
	case <-time.After(5 * time.Second):
		t.Fatal("authority event did not reproject the mounted view")
	}
	for _, path := range []string{allowed, blocked} {
		if data, err := os.ReadFile(path); err == nil {
			t.Errorf("revocation exposed file contents at %s: %q", path, data)
		}
	}
	if data, err := io.ReadAll(open); err == nil {
		t.Errorf("descriptor opened before revocation still readable: %q", data)
	}
	for _, name := range []string{"allowed", "blocked"} {
		data, err := os.ReadFile(filepath.Join(root, name, "marker"))
		if err != nil || string(data) != name {
			t.Errorf("backing fixture changed: %q, %v", data, err)
		}
	}
}
