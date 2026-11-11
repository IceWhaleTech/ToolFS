package toolfs

import (
	"os"
	"path/filepath"
	"testing"
)

// Static fixtures name their backing grants explicitly; production authority
// views are bounded by the entries API and never discover these by scanning.
func expansionFixture(t *testing.T, role string) (string, *InstanceView) {
	t.Helper()
	base := t.TempDir()
	var grants []Grant
	for _, name := range []string{"Disk", "Backup"} {
		host := filepath.Join(base, name)
		if err := os.Mkdir(host, 0700); err != nil {
			t.Fatal(err)
		}
		grants = append(grants, Grant{Path: host})
	}
	view, err := BuildInstanceView(&ZimaIdentity{Username: "alice", UserID: 7, Role: role}, NewAccessSnapshot("alice", 7, role, grants, 1), capsBase(base))
	if err != nil {
		t.Fatal(err)
	}
	return base, view
}

func TestExplicitGrantReplacementRetiresOldRoot(t *testing.T) {
	base, view := expansionFixture(t, "user")
	if err := view.openRoots(); err != nil {
		t.Fatal(err)
	}
	defer view.closeRoots()
	old := entryFor(t, view, "Disk")
	if err := os.Rename(filepath.Join(base, "Disk"), filepath.Join(base, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "Disk"), 0700); err != nil {
		t.Fatal(err)
	}
	next, err := BuildInstanceView(view.Identity, view.Access, view.Caps)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := view.reproject(next); err != nil || !changed {
		t.Fatalf("replacement: %v %v", changed, err)
	}
	if old == entryFor(t, view, "Disk") || view.Allows(old, "", OpRead) {
		t.Fatal("old explicit grant remains live")
	}
}

func TestExplicitFileReplacementRetiresOldRoot(t *testing.T) {
	base := t.TempDir()
	host := filepath.Join(base, "only.txt")
	if err := os.WriteFile(host, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	access := NewAccessSnapshot("alice", 7, "user", []Grant{{Path: host, ReadOnly: true}}, 1)
	view, err := BuildInstanceView(&ZimaIdentity{Username: "alice", UserID: 7}, access, capsBase(base))
	if err != nil {
		t.Fatal(err)
	}
	if err := view.openRoots(); err != nil {
		t.Fatal(err)
	}
	defer view.closeRoots()
	old := entryFor(t, view, "only.txt")
	if err := os.Rename(host, host+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(host, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := BuildInstanceView(view.Identity, access, view.Caps)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := view.reproject(next); err != nil || !changed {
		t.Fatalf("file replacement: %v %v", changed, err)
	}
	if view.Allows(old, old.fileName, OpRead) {
		t.Fatal("retired file grant remained readable")
	}
}
