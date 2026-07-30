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
// The state root lives outside the mount and must sit on the same filesystem as
// the data it protects: trash relies on rename, which returns EXDEV across
// devices. A mismatch is detected at open time and fails loudly rather than
// silently degrading to a copy.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// StateDirName is the reserved directory holding trash, versions and journal.
const StateDirName = toolfsStateDir

var (
	// ErrStateRootCrossDevice reports a state root on a different filesystem
	// than the data, which would make trash renames fail with EXDEV.
	ErrStateRootCrossDevice = errors.New("toolfs: state root is on a different filesystem than the protected data")
	// ErrProtectionUnavailable reports that no state root is configured.
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

// StateRoot owns the protection area for one backing device.
type StateRoot struct {
	// Path is the state root directory, outside any mount.
	Path   string
	policy ProtectionPolicy

	mu sync.Mutex
	// lastVersion tracks debounce windows per canonical path.
	lastVersion map[string]time.Time
	// seq makes stored names unique without relying on wall-clock resolution.
	seq uint64
}

// OpenStateRoot prepares a state root and verifies it can protect dataPath.
//
// The same-filesystem check is not cosmetic: trash is implemented as rename,
// which fails with EXDEV across devices. Detecting it here turns a silent
// protection failure into a startup error.
func OpenStateRoot(statePath, dataPath string, policy ProtectionPolicy) (*StateRoot, error) {
	if statePath == "" {
		return nil, ErrProtectionUnavailable
	}
	if policy.ExcludePatterns == nil {
		policy.ExcludePatterns = DefaultExclusions
	}

	for _, sub := range []string{"trash", "versions", "meta"} {
		if err := os.MkdirAll(filepath.Join(statePath, sub), 0o700); err != nil {
			return nil, fmt.Errorf("toolfs: cannot create state root: %w", err)
		}
	}
	// Owner-only: the protection area holds copies of user content, so it must
	// not be readable by anything that can reach the path.
	if err := os.Chmod(statePath, 0o700); err != nil {
		return nil, err
	}

	if dataPath != "" {
		same, err := sameFilesystem(statePath, dataPath)
		if err != nil {
			return nil, err
		}
		if !same {
			return nil, fmt.Errorf("%w: state=%s data=%s", ErrStateRootCrossDevice, statePath, dataPath)
		}
	}

	return &StateRoot{
		Path:        statePath,
		policy:      policy,
		lastVersion: make(map[string]time.Time),
	}, nil
}

// Policy exposes the effective policy.
func (s *StateRoot) Policy() ProtectionPolicy { return s.policy }

// IsExcluded reports whether a grant-relative path is exempt from versioning.
//
// Matching is per path component, so "node_modules" excludes everything under
// it regardless of depth, which is the point: the cost is in the tree, not the
// directory entry.
func (s *StateRoot) IsExcluded(rel string) bool {
	if rel == "" {
		return false
	}
	for _, component := range strings.Split(rel, "/") {
		for _, pattern := range s.policy.ExcludePatterns {
			if component == pattern {
				return true
			}
			if matched, err := path.Match(pattern, component); err == nil && matched {
				return true
			}
		}
	}
	return false
}

// TrashFile moves a file or directory into the trash.
//
// Implemented as a rename, so it is O(1) and a recursive delete moves only the
// top directory rather than walking it.
func (s *StateRoot) TrashFile(username, hostPath string, isDir bool, size int64) (*TrashEntry, error) {
	id := s.nextID()
	stored := id
	target := filepath.Join(s.Path, "trash", stored)

	if err := os.Rename(hostPath, target); err != nil {
		if isCrossDevice(err) {
			// Should have been caught at open time; report it as such rather
			// than falling back to a copy, which would change the cost model
			// silently.
			return nil, fmt.Errorf("%w: %v", ErrStateRootCrossDevice, err)
		}
		return nil, err
	}

	entry := &TrashEntry{
		ID:           id,
		Username:     username,
		OriginalPath: canonicalHostPath(hostPath),
		StoredName:   stored,
		IsDir:        isDir,
		Size:         size,
		DeletedAt:    time.Now(),
	}
	if err := s.writeMeta("trash", id, entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// RestoreFromTrash moves an entry back to its original path.
//
// The caller is responsible for checking that the *current* grants still allow
// writing there. Restoring on the strength of the grants held at deletion time
// would let a revoked user pull their data back.
func (s *StateRoot) RestoreFromTrash(id string) (*TrashEntry, error) {
	var entry TrashEntry
	if err := s.readMeta("trash", id, &entry); err != nil {
		return nil, ErrTrashEntryNotFound
	}

	source := filepath.Join(s.Path, "trash", entry.StoredName)
	if _, err := os.Stat(entry.OriginalPath); err == nil {
		return nil, fmt.Errorf("toolfs: %s already exists", entry.OriginalPath)
	}
	if err := os.MkdirAll(filepath.Dir(entry.OriginalPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(source, entry.OriginalPath); err != nil {
		return nil, err
	}
	_ = os.Remove(s.metaPath("trash", id))
	return &entry, nil
}

// ListTrash returns trash entries, newest first.
func (s *StateRoot) ListTrash() ([]TrashEntry, error) {
	var out []TrashEntry
	err := s.eachMeta("trash", func(data []byte) error {
		var entry TrashEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			return nil // skip unreadable metadata rather than failing the listing
		}
		out = append(out, entry)
		return nil
	})
	sortByTimeDesc(len(out), func(i int) time.Time { return out[i].DeletedAt }, func(i, j int) {
		out[i], out[j] = out[j], out[i]
	})
	return out, err
}

// CaptureVersion stores the current content of hostPath before it is replaced.
//
// It returns nil without error when the path is exempt: excluded by pattern,
// larger than the ceiling, within the debounce window, or when free space is
// below the floor. Those are policy decisions, not failures.
func (s *StateRoot) CaptureVersion(username, hostPath, rel string) (*VersionEntry, error) {
	if s.IsExcluded(rel) {
		return nil, nil
	}

	info, err := os.Stat(hostPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // creating a new file: nothing to preserve
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, nil
	}
	if s.policy.MaxVersionFileSize > 0 && info.Size() > s.policy.MaxVersionFileSize {
		return nil, nil
	}

	canonical := canonicalHostPath(hostPath)
	if !s.shouldVersion(canonical) {
		return nil, nil
	}
	// Degrade to trash-only rather than filling the disk. On ext4 a version is
	// a full copy, so low headroom plus versioning is how a disk gets wedged.
	if s.policy.MinFreeBytes > 0 {
		free, err := freeBytes(s.Path)
		if err == nil && free < uint64(s.policy.MinFreeBytes) {
			return nil, nil
		}
	}

	id := s.nextID()
	target := filepath.Join(s.Path, "versions", id)
	if err := copyFile(hostPath, target); err != nil {
		return nil, err
	}

	entry := &VersionEntry{
		ID:           id,
		Username:     username,
		OriginalPath: canonical,
		StoredName:   id,
		Size:         info.Size(),
		CapturedAt:   time.Now(),
	}
	if err := s.writeMeta("versions", id, entry); err != nil {
		return nil, err
	}

	s.markVersioned(canonical)
	s.pruneVersions(canonical)
	return entry, nil
}

// ListVersions returns retained versions for a path, newest first.
func (s *StateRoot) ListVersions(hostPath string) ([]VersionEntry, error) {
	canonical := canonicalHostPath(hostPath)
	var out []VersionEntry
	err := s.eachMeta("versions", func(data []byte) error {
		var entry VersionEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			return nil
		}
		if entry.OriginalPath == canonical {
			out = append(out, entry)
		}
		return nil
	})
	sortByTimeDesc(len(out), func(i int) time.Time { return out[i].CapturedAt }, func(i, j int) {
		out[i], out[j] = out[j], out[i]
	})
	return out, err
}

// OpenVersion returns a reader over a stored version's content.
func (s *StateRoot) OpenVersion(id string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(s.Path, "versions", id))
}

// shouldVersion applies the debounce window.
func (s *StateRoot) shouldVersion(canonical string) bool {
	if s.policy.VersionDebounce <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	last, seen := s.lastVersion[canonical]
	return !seen || time.Since(last) >= s.policy.VersionDebounce
}

func (s *StateRoot) markVersioned(canonical string) {
	s.mu.Lock()
	s.lastVersion[canonical] = time.Now()
	s.mu.Unlock()
}

// pruneVersions enforces MaxVersionsPerFile, dropping the oldest first.
func (s *StateRoot) pruneVersions(canonical string) {
	if s.policy.MaxVersionsPerFile <= 0 {
		return
	}
	versions, err := s.ListVersions(canonical)
	if err != nil || len(versions) <= s.policy.MaxVersionsPerFile {
		return
	}
	for _, stale := range versions[s.policy.MaxVersionsPerFile:] {
		_ = os.Remove(filepath.Join(s.Path, "versions", stale.StoredName))
		_ = os.Remove(s.metaPath("versions", stale.ID))
	}
}

// nextID produces a sortable, collision-free identifier.
func (s *StateRoot) nextID() string {
	s.mu.Lock()
	s.seq++
	seq := s.seq
	s.mu.Unlock()
	return fmt.Sprintf("%d-%06d", time.Now().UnixNano(), seq)
}

func (s *StateRoot) metaPath(kind, id string) string {
	return filepath.Join(s.Path, "meta", kind+"-"+id+".json")
}

func (s *StateRoot) writeMeta(kind, id string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// Write to a temporary file and rename, so a crash mid-write cannot leave
	// a half-written record that later reads would mistake for a valid one.
	tmp := s.metaPath(kind, id) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.metaPath(kind, id))
}

func (s *StateRoot) readMeta(kind, id string, out any) error {
	data, err := os.ReadFile(s.metaPath(kind, id))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (s *StateRoot) eachMeta(kind string, fn func([]byte) error) error {
	entries, err := os.ReadDir(filepath.Join(s.Path, "meta"))
	if err != nil {
		return err
	}
	prefix := kind + "-"
	for _, item := range entries {
		name := item.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Path, "meta", name))
		if err != nil {
			continue
		}
		if err := fn(data); err != nil {
			return err
		}
	}
	return nil
}

// copyFile duplicates a file's content and mode.
func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(target)
		return err
	}
	return out.Close()
}

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
