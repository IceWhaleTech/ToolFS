//go:build darwin

package toolfs

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func secureSystemPath(path string) string {
	// These are fixed OS aliases, not arbitrary data symlinks. Do not resolve
	// further links or add authority/storage aliases to this list.
	for _, prefix := range []string{"/var", "/tmp", "/etc"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return "/private" + path
		}
	}
	return path
}

func openSecureMetadata(parent *os.File, name string) (*os.File, error) {
	// O_EVTONLY is metadata-only, including for FIFOs/devices. O_SYMLINK opens
	// a symlink itself (lstat semantics), never its target.
	return openSecureAt(parent, name, unix.O_EVTONLY|unix.O_SYMLINK, 0)
}

func probeSecureClone(*os.File) bool { return false }
