//go:build linux

package toolfs

// Copy-on-write file cloning on Linux.
//
// FICLONE makes the destination share the source's extents instead of copying
// them. On btrfs and XFS that turns preserving a file version from an O(size)
// copy into an O(1) metadata operation costing essentially no space — which is
// what lets the size ceiling on versioning be lifted.

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile asks the filesystem to share extents between the two descriptors.
// Filesystems without reflink support return an errno the caller maps onto
// ErrCloneUnsupported.
func cloneFile(dst, src *os.File) error {
	return unix.IoctlFileClone(int(dst.Fd()), int(src.Fd()))
}
