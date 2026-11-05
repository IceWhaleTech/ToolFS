//go:build linux || darwin

package toolfs

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func validatePrivateMountRoot(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || st.Uid != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("toolfs: non-root container mounts require a root-owned private mount root (0700): %s", path)
	}
	return nil
}

// allow_other must never turn a container's view into a public host mount.
// Require a private root-owned ancestor and reject links or writable directory
// components between it and the mount. Root remains trusted as mount authority.
func validateContainerMountLocation(path string) error {
	for path = filepath.Clean(path); ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || st.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("toolfs: unsafe non-root container mount location: %s", path)
		}
		if info.Mode().Perm()&0o077 == 0 {
			return nil
		}
		if filepath.Dir(path) == path {
			return fmt.Errorf("toolfs: non-root container mount has no private host ancestor")
		}
	}
}
