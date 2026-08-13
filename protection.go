//go:build linux || darwin
// +build linux darwin

package toolfs

// Protection chain: trash and file versions.
//
// This is where "prevent accidental deletion" actually happens. Two mechanisms,
// deliberately different in cost:
//
//   - Trash: deletion becomes a rename into the state root. O(1) regardless of
//     size, and recursive directory removal renames only the directory itself
//     rather than walking it.
//   - Versions: before content is overwritten, the previous content is copied
//     aside. This one is O(size) on filesystems without reflink, which /DATA
//     (ext4) is, so it needs guard rails — see the exclusion rules and the size
//     ceiling below.
//
// The state area lives under each grant root, reached through that grant's
// constrained handle — which is what keeps trash renames free of both TOCTOU
// and EXDEV. See protection_bound.go.
//
// This file now holds only the shared policy and record types.

import (
	"errors"
	"time"
)

// StateDirName is the reserved directory holding trash, versions and journal.
const StateDirName = toolfsStateDir

var (
	// ErrProtectionUnavailable reports that no protection area is available.
	// Protected mutations fail closed rather than proceeding unprotected.
	ErrProtectionUnavailable = errors.New("toolfs: protection area unavailable")
	// ErrTrashEntryNotFound reports an unknown trash entry.
	ErrTrashEntryNotFound = errors.New("toolfs: trash entry not found")
)

// DefaultExclusions are never versioned.
//
// This list is not optional. Codex drives npm install and git; node_modules
// alone is tens of thousands of files, and on the reference device /DATA is
// ext4 with no reflink and 31 GB free, so a single task would fill the disk.
// Deletion still goes to trash for these paths — only version retention is
// skipped, since .git already holds full history and node_modules is derived.
var DefaultExclusions = []string{
	".git",
	"node_modules",
	".venv",
	"__pycache__",
	"target",
	"dist",
	"build",
	".next",
	".cache",
	"vendor",
}

// ProtectionPolicy tunes trash and version behaviour.
type ProtectionPolicy struct {
	// VersionDebounce suppresses a new version when one was taken for the same
	// path within this window. Agents write in bursts; without it an editor
	// loop produces a version per keystroke-save.
	VersionDebounce time.Duration
	// MaxVersionsPerFile caps retained versions per path. Versions belong to
	// the file, not to a user: a version produced by one user is what another
	// restores from, and all writers share this budget.
	MaxVersionsPerFile int
	// MaxVersionFileSize skips versioning for files above this size. On a
	// filesystem without reflink this bounds the copy cost per write.
	MaxVersionFileSize int64
	// ExcludePatterns are matched against any path component and against the
	// grant-relative path. Empty means DefaultExclusions.
	ExcludePatterns []string
	// MinFreeBytes stops versioning (not trashing) when the filesystem has
	// less headroom than this. Degrading to trash-only beats filling the disk.
	MinFreeBytes int64
}

// DefaultProtectionPolicy returns settings sized for the reference device.
func DefaultProtectionPolicy() ProtectionPolicy {
	return ProtectionPolicy{
		VersionDebounce:    60 * time.Second,
		MaxVersionsPerFile: 10,
		MaxVersionFileSize: 64 << 20, // 64 MiB
		ExcludePatterns:    DefaultExclusions,
		MinFreeBytes:       2 << 30, // 2 GiB
	}
}

// TrashEntry records one trashed path.
type TrashEntry struct {
	ID string `json:"id"`
	// Username attributes the deletion; restore is checked against current
	// grants, not against this field.
	Username string `json:"username"`
	// OriginalPath is the canonical host path the entry came from.
	OriginalPath string `json:"original_path"`
	// StoredName is the entry's file name inside the trash directory.
	StoredName string    `json:"stored_name"`
	IsDir      bool      `json:"is_dir"`
	Size       int64     `json:"size"`
	DeletedAt  time.Time `json:"deleted_at"`
}

// VersionEntry records one retained previous content of a file.
type VersionEntry struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	OriginalPath string    `json:"original_path"`
	StoredName   string    `json:"stored_name"`
	Size         int64     `json:"size"`
	CapturedAt   time.Time `json:"captured_at"`
}

// The protection chain proper lives in protection_bound.go, bound to a grant's
// constrained root handle. An earlier path-based implementation lived here; it
// was removed because it had stopped being the code that actually runs — and
// the low-space guard had been fixed only in that dead copy, so the guard was
// silently absent from the live path. Two implementations of one thing is how
// that happens.

// sortByTimeDesc is a small insertion sort over an indexable sequence.
// The sets involved are bounded by MaxVersionsPerFile and by trash retention,
// so an allocation-free in-place sort is preferable to building key slices.
func sortByTimeDesc(n int, at func(int) time.Time, swap func(int, int)) {
	for i := 1; i < n; i++ {
		for j := i; j > 0 && at(j).After(at(j-1)); j-- {
			swap(j, j-1)
		}
	}
}
