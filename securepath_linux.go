//go:build linux

package toolfs

import (
	"os"

	"golang.org/x/sys/unix"
)

func secureSystemPath(path string) string { return path }

// O_PATH obtains metadata without opening devices/FIFOs or requiring read
// permission, and O_NOFOLLOW makes a final link refer to the link inode.
func openSecureMetadata(parent *os.File, name string) (*os.File, error) {
	return openSecureAt(parent, name, unix.O_PATH, 0)
}

// Anonymous probes need no pathname cleanup, even when another local writer
// concurrently changes names in the state area. Unsupported O_TMPFILE is a
// conservative false, not permission to remove a named probe.
func probeSecureClone(root *os.File) bool {
	src, err := openSecureAt(root, ".", unix.O_TMPFILE|unix.O_RDWR, 0600)
	if err != nil {
		return false
	}
	defer src.Close()
	dst, err := openSecureAt(root, ".", unix.O_TMPFILE|unix.O_RDWR, 0600)
	if err != nil {
		return false
	}
	defer dst.Close()
	if _, err := src.Write([]byte{0}); err != nil {
		return false
	}
	return cloneFile(dst, src) == nil
}
