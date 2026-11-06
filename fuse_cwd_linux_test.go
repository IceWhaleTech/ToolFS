//go:build linux

package toolfs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMountWorkingDirectorySurvivesRepeatedLookups(t *testing.T) {
	mount, _, cleanup := mountTwoUsers(t)
	defer cleanup()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMountWorkingDirectoryHelper$")
	cmd.Env = append(os.Environ(), "TOOLFS_CWD_FIXTURE="+filepath.Join(mount, "Documents"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("a live granted directory lost cwd identity: %v\n%s", err, out)
	}
}

func TestMountWorkingDirectoryHelper(t *testing.T) {
	path := os.Getenv("TOOLFS_CWD_FIXTURE")
	if path == "" {
		t.Skip("subprocess helper")
	}
	if err := os.Chdir(path); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
		if _, err := unix.Getcwd(make([]byte, 4096)); err != nil {
			t.Fatalf("lookup %d detached cwd: %v", i, err)
		}
	}
}
