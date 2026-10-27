//go:build linux || darwin

package toolfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// removeMountDirectories removes only the known, empty directories after a
// successful unmount. It must never recurse into a live or unexpected tree.
func (s *MountSet) removeMountDirectories(live *liveMount) error {
	identity := live.handle.Identity()
	if identity == nil || (live.app != "" && (live.app == "." || live.app == ".." || filepath.Base(live.app) != live.app)) {
		return fmt.Errorf("toolfs: invalid mount cleanup identity")
	}
	root, err := filepath.Abs(s.config.Root)
	if err != nil {
		return err
	}
	userDir := filepath.Join(root, identity.mountDirName())
	bind := filepath.Join(userDir, live.app)
	point := filepath.Join(bind, "workspace")
	actualBind, err := filepath.Abs(live.bindSource)
	if err != nil {
		return err
	}
	actualPoint, err := filepath.Abs(live.handle.MountPoint())
	if err != nil {
		return err
	}
	if actualBind != bind || actualPoint != point || bind == root {
		return fmt.Errorf("toolfs: mount cleanup path is outside the instance layout")
	}
	for _, path := range []string{userDir, bind, point} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("toolfs: mount cleanup path is not a real directory: %s", path)
		}
	}
	if err := os.Remove(point); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if bind != userDir {
		if err := os.Remove(bind); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Remove(userDir); err != nil && !errors.Is(err, os.ErrNotExist) &&
		!errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
		return err
	}
	return nil
}
