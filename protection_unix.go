//go:build linux || darwin
// +build linux darwin

package toolfs

// Filesystem probes backing the protection chain's guard rails.

import (
	"errors"
	"os"
	"syscall"
)

// sameFilesystem reports whether two paths live on the same device.
//
// Trash is a rename, which returns EXDEV across devices, so a state root on a
// different filesystem silently breaks protection. Checking st_dev up front
// turns that into a startup error instead.
func sameFilesystem(a, b string) (bool, error) {
	infoA, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	infoB, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	statA, okA := infoA.Sys().(*syscall.Stat_t)
	statB, okB := infoB.Sys().(*syscall.Stat_t)
	if !okA || !okB {
		// Cannot tell; do not claim a mismatch we have not established.
		return true, nil
	}
	return statA.Dev == statB.Dev, nil
}

// isCrossDevice reports whether err is EXDEV.
func isCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

// freeBytes reports free space available to the caller at path.
func freeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
