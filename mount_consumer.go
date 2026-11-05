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

// Only this directory crosses the container bind boundary. Its host ancestors
// remain private, while its mode must be independent of the daemon's umask.
// Keep the descriptor open so a failed mount can restore the original inode.
func prepareContainerBindSource(path string) (*os.File, os.FileMode, error) {
	if err := validateContainerMountLocation(filepath.Dir(path)); err != nil {
		return nil, 0, err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, err
	}
	dir := os.NewFile(uintptr(fd), path)
	info, err := dir.Stat()
	if err != nil {
		dir.Close()
		return nil, 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || st.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
		dir.Close()
		return nil, 0, fmt.Errorf("toolfs: unsafe container bind source: %s", path)
	}
	if err := dir.Chmod(0o755); err != nil {
		dir.Close()
		return nil, 0, err
	}
	return dir, info.Mode(), nil
}
