//go:build linux || darwin
// +build linux darwin

package toolfs

// FUSE availability probing.
//
// go-fuse's Mount does not fail fast when no FUSE implementation is present —
// on macOS without macFUSE it blocks instead of returning an error, which
// would hang any test that calls it unguarded. Callers should probe first.

import (
	"os"
	"runtime"
)

// FUSEAvailable reports whether this machine can serve a FUSE mount.
func FUSEAvailable() bool {
	switch runtime.GOOS {
	case "linux":
		if _, err := os.Stat("/dev/fuse"); err != nil {
			return false
		}
		return true
	case "darwin":
		// Probe for the device node, not the bundle directory. An uninstalled
		// or unloaded macFUSE commonly leaves /Library/Filesystems/osxfuse.fs
		// behind, so the bundle's presence says nothing about usability —
		// and mounting against it hangs rather than failing.
		for _, dev := range []string{"/dev/macfuse0", "/dev/osxfuse0"} {
			if _, err := os.Stat(dev); err == nil {
				return true
			}
		}
		return false
	default:
		return false
	}
}
