//go:build linux || darwin

package toolfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type mountLayout struct {
	user, bind, point string
}

func mountLayoutFor(root string, identity *ZimaIdentity, app string) (mountLayout, error) {
	if identity == nil || (app != "" && (app == "." || app == ".." || filepath.Base(app) != app)) {
		return mountLayout{}, fmt.Errorf("toolfs: invalid mount directory identity")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return mountLayout{}, err
	}
	user := filepath.Join(root, identity.mountDirName())
	bind := filepath.Join(user, app)
	return mountLayout{user: user, bind: bind, point: filepath.Join(bind, "workspace")}, nil
}

func (l mountLayout) paths() []string {
	if l.user == l.bind {
		return []string{l.user, l.point}
	}
	return []string{l.user, l.bind, l.point}
}

// The lifecycle lock protects this check through directory preparation and mount.
// Different account spellings can resolve to the same UID, and an empty app's
// workspace must not become another app's bind directory.
func (s *MountSet) checkMountLayout(layout mountLayout) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, live := range s.mounts {
		point, err := filepath.Abs(live.handle.MountPoint())
		if err != nil {
			return err
		}
		if layout.point == point || strings.HasPrefix(layout.point, point+string(filepath.Separator)) ||
			strings.HasPrefix(point, layout.point+string(filepath.Separator)) {
			return fmt.Errorf("toolfs: workspace overlaps mount %s/%s", live.agentUserID, live.app)
		}
	}
	return nil
}

type preparedMountDirectory struct {
	path    string
	info    os.FileInfo
	created bool
}

type preparedMountDirectories struct {
	dirs []preparedMountDirectory
}

func (l mountLayout) prepare() (*preparedMountDirectories, error) {
	prepared := &preparedMountDirectories{}
	for _, path := range l.paths() {
		err := os.Mkdir(path, 0o755)
		created := err == nil
		if err != nil && !errors.Is(err, os.ErrExist) {
			return prepared, fmt.Errorf("toolfs: prepare mount directory %s: %w", path, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return prepared, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return prepared, fmt.Errorf("toolfs: mount path is not a real directory: %s", path)
		}
		prepared.dirs = append(prepared.dirs, preparedMountDirectory{path: path, info: info, created: created})
	}
	return prepared, nil
}

// rollback never adopts preexisting paths or traverses a replaced parent. The
// operation lock excludes other MountSet management calls, not arbitrary host
// filesystem writers; nonrecursive removal remains the final safety boundary.
func (p *preparedMountDirectories) rollback() error {
	for _, dir := range p.dirs {
		info, err := os.Lstat(dir.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("toolfs: inspect failed mount directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, dir.info) {
			return fmt.Errorf("toolfs: failed mount directory was replaced: %s", dir.path)
		}
	}
	for i := len(p.dirs) - 1; i >= 0; i-- {
		if !p.dirs[i].created {
			continue
		}
		if err := os.Remove(p.dirs[i].path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("toolfs: roll back mount directory: %w", err)
		}
	}
	return nil
}

// removeMountDirectories removes only the known, empty directories after a
// successful unmount. It must never recurse into a live or unexpected tree.
func (s *MountSet) removeMountDirectories(live *liveMount) error {
	layout, err := mountLayoutFor(s.config.Root, live.handle.Identity(), live.app)
	if err != nil {
		return err
	}
	actualBind, err := filepath.Abs(live.bindSource)
	if err != nil {
		return err
	}
	actualPoint, err := filepath.Abs(live.handle.MountPoint())
	if err != nil {
		return err
	}
	if actualBind != layout.bind || actualPoint != layout.point {
		return fmt.Errorf("toolfs: mount cleanup path is outside the instance layout")
	}
	for _, path := range layout.paths() {
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
	if err := os.Remove(layout.point); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if layout.bind != layout.user {
		if err := os.Remove(layout.bind); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Remove(layout.user); err != nil && !errors.Is(err, os.ErrNotExist) &&
		!errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
		return err
	}
	return nil
}
