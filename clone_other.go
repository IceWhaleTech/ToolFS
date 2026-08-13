//go:build !linux

package toolfs

import "os"

// cloneFile reports that copy-on-write cloning is unavailable.
//
// macOS has clonefile(2) for APFS, but this build is the development target
// only — production runs on Linux, and claiming a capability the tests cannot
// exercise here would be worse than declaring its absence.
func cloneFile(dst, src *os.File) error {
	return ErrCloneUnsupported
}
