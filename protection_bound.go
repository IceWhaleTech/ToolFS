//go:build linux || darwin
// +build linux darwin

package toolfs

// Protection chain bound to a single grant root.
//
// Everything here operates through the grant's constrained RootHandle, using
// paths relative to that root. Two problems disappear as a result:
//
//   - No TOCTOU. Moving a file into the trash is a rename issued through the
//     same descriptor that enforces containment, so no component of the path
//     can be swapped for a symlink between the check and the move.
//   - No EXDEV. The state area lives under the grant root, so it is on the
//     same filesystem by construction rather than by configuration that has to
//     be verified and can drift.
//
// The trade-off is that the protection area sits inside user data on disk. It
// is hidden from the ToolFS namespace by reserving the name, and it must also
// be excluded from SMB and the Files API — otherwise deleted content would be
// reachable through another channel.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"
	"time"
)

// Reserved sub-paths of the state area, relative to a grant root.
const (
	stateTrashDir    = toolfsStateDir + "/trash"
	stateVersionsDir = toolfsStateDir + "/versions"
	stateMetaDir     = toolfsStateDir + "/meta"
)

// ErrDeleteNotPermitted reports that the instance lacks the delete capability.
// Deletion is withheld by default: ZimaOS only distinguishes read-only from
// read-write, so "writable" would otherwise imply "deletable", which is the
// root of the accidental-deletion risk this project exists to remove.
var ErrDeleteNotPermitted = errors.New("toolfs: delete is not permitted for this instance")

// boundProtection is the protection chain for one grant root.
type boundProtection struct {
	root   RootHandle
	policy ProtectionPolicy

	mu          sync.Mutex
	lastVersion map[string]time.Time
	seq         uint64

	// cloneSupported records whether this filesystem can share extents.
	// Probed once at init rather than inferred from the filesystem name, since
	// what matters is whether the ioctl actually works here.
	cloneSupported bool
}

// newBoundProtection prepares the state area under a grant root.
func newBoundProtection(root RootHandle, policy ProtectionPolicy) (*boundProtection, error) {
	if policy.ExcludePatterns == nil {
		policy.ExcludePatterns = DefaultExclusions
	}
	for _, dir := range []string{stateTrashDir, stateVersionsDir, stateMetaDir, stateJournalDir} {
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("toolfs: cannot prepare state area: %w", err)
		}
	}
	p := &boundProtection{
		root:        root,
		policy:      policy,
		lastVersion: make(map[string]time.Time),
	}
	p.cloneSupported = p.probeClone()

	// With copy-on-write cloning, preserving a version costs neither time nor
	// space proportional to the file. The size ceiling exists purely to bound
	// the cost of full copies, so it does not apply here — which is the point
	// of probing: large files become protectable instead of being skipped.
	if p.cloneSupported {
		p.policy.MaxVersionFileSize = 0
	}
	return p, nil
}

// probeClone determines whether extent sharing works, by trying it on a tiny
// file inside the state area.
//
// Probing beats inferring from the filesystem type: reflink depends on how the
// volume was made and mounted, not only on which filesystem it is.
func (p *boundProtection) probeClone() bool {
	const src = toolfsStateDir + "/.clone-probe"
	const dst = toolfsStateDir + "/.clone-probe-copy"

	defer func() {
		_ = p.root.Remove(src)
		_ = p.root.Remove(dst)
	}()

	if err := p.root.WriteFile(src, []byte{0}, 0o600); err != nil {
		return false
	}
	_ = p.root.Remove(dst)
	return p.root.CloneFile(src, dst, 0o600) == nil
}

// CloneSupported reports whether versions are stored by extent sharing.
func (p *boundProtection) CloneSupported() bool { return p.cloneSupported }

// isReserved reports whether a grant-relative path lies in the state area.
// Reserved paths must never be visible in the namespace nor operated on.
func isReservedRel(rel string) bool {
	if rel == "" {
		return false
	}
	first := rel
	if idx := strings.IndexByte(rel, '/'); idx >= 0 {
		first = rel[:idx]
	}
	return first == toolfsStateDir || first == toolfsControlDir
}

// excluded reports whether rel is exempt from version retention.
func (p *boundProtection) excluded(rel string) bool {
	for _, component := range strings.Split(rel, "/") {
		for _, pattern := range p.policy.ExcludePatterns {
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

// Trash moves a grant-relative path into the state area.
//
// Implemented as a rename, so it is O(1) and a recursive delete moves only the
// top directory rather than walking it.
func (p *boundProtection) Trash(username, rel string) (*TrashEntry, error) {
	if isReservedRel(rel) {
		return nil, ErrNotAuthorized
	}
	info, err := p.root.Stat(rel)
	if err != nil {
		return nil, err
	}

	id := p.nextID()
	stored := stateTrashDir + "/" + id
	if err := p.root.Rename(rel, stored); err != nil {
		return nil, err
	}

	entry := &TrashEntry{
		ID:           id,
		Username:     username,
		OriginalPath: rel,
		StoredName:   stored,
		IsDir:        info.IsDir(),
		Size:         info.Size(),
		DeletedAt:    time.Now(),
	}
	if err := p.writeMeta("trash", id, entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// Restore moves a trashed entry back to where it came from.
//
// Callers must first check the *current* grants allow writing there. Restoring
// on the strength of the grants held at deletion time would let a user whose
// access was revoked pull their data back out.
func (p *boundProtection) Restore(id string) (*TrashEntry, error) {
	var entry TrashEntry
	if err := p.readMeta("trash", id, &entry); err != nil {
		return nil, ErrTrashEntryNotFound
	}
	if _, err := p.root.Stat(entry.OriginalPath); err == nil {
		return nil, fmt.Errorf("toolfs: %s already exists", entry.OriginalPath)
	}
	if parent := path.Dir(entry.OriginalPath); parent != "." && parent != "/" {
		if err := p.root.MkdirAll(parent, 0o755); err != nil {
			return nil, err
		}
	}
	if err := p.root.Rename(entry.StoredName, entry.OriginalPath); err != nil {
		return nil, err
	}
	_ = p.root.Remove(p.metaRel("trash", id))
	return &entry, nil
}

// unmarshalQuiet decodes JSON and reports success, so callers can skip records
// that cannot be read rather than failing a whole listing.
func unmarshalQuiet(data []byte, out any) bool {
	return json.Unmarshal(data, out) == nil
}

// ListTrash returns trash entries, newest first.
func (p *boundProtection) ListTrash() ([]TrashEntry, error) {
	var out []TrashEntry
	err := p.eachMeta("trash", func(data []byte) {
		var entry TrashEntry
		if json.Unmarshal(data, &entry) == nil {
			out = append(out, entry)
		}
	})
	sortByTimeDesc(len(out), func(i int) time.Time { return out[i].DeletedAt },
		func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out, err
}

// CaptureVersion preserves current content before it is overwritten.
//
// Returns nil without error when the path is exempt — excluded by pattern, over
// the size ceiling, or inside the debounce window. Those are policy outcomes,
// not failures, and the caller proceeds with the write either way.
func (p *boundProtection) CaptureVersion(username, rel string) (*VersionEntry, error) {
	return p.captureVersion(username, rel, false)
}

// captureVersion is CaptureVersion with an explicit debounce override.
func (p *boundProtection) captureVersion(username, rel string, force bool) (*VersionEntry, error) {
	if isReservedRel(rel) || p.excluded(rel) {
		return nil, nil
	}

	info, err := p.root.Stat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // new file: nothing to preserve
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, nil
	}
	if p.policy.MaxVersionFileSize > 0 && info.Size() > p.policy.MaxVersionFileSize {
		return nil, nil
	}
	if !force && !p.shouldVersion(rel) {
		return nil, nil
	}

	// Degrade to trash-only when space runs short. Versions are a convenience;
	// filling the disk breaks everything, including the trash that deletion
	// depends on. Cloned versions cost no space, so the check does not apply
	// to them.
	if !p.cloneSupported && p.policy.MinFreeBytes > 0 {
		if free, err := p.root.FreeBytes(); err == nil && free < uint64(p.policy.MinFreeBytes) {
			return nil, nil
		}
	}

	id := p.nextID()
	stored := stateVersionsDir + "/" + id

	// Prefer extent sharing; fall back to a full copy where it is unavailable.
	if err := p.root.CloneFile(rel, stored, 0o600); err != nil {
		if !errors.Is(err, ErrCloneUnsupported) {
			return nil, err
		}
		content, readErr := p.root.ReadFile(rel)
		if readErr != nil {
			return nil, readErr
		}
		if writeErr := p.root.WriteFile(stored, content, 0o600); writeErr != nil {
			return nil, writeErr
		}
	}

	entry := &VersionEntry{
		ID:           id,
		Username:     username,
		OriginalPath: rel,
		StoredName:   stored,
		Size:         info.Size(),
		CapturedAt:   time.Now(),
	}
	if err := p.writeMeta("versions", id, entry); err != nil {
		return nil, err
	}

	p.markVersioned(rel)
	p.pruneVersions(rel)
	return entry, nil
}

// latestVersionID returns the most recent retained version for a path.
//
// Needed because a write can be debounced: the previous content is already
// preserved by an earlier capture in the same window, and the journal entry
// must point at that one rather than record "nothing preserved".
func (p *boundProtection) latestVersionID(rel string) string {
	versions, err := p.ListVersions(rel)
	if err != nil || len(versions) == 0 {
		return ""
	}
	return versions[0].ID
}

// ListVersions returns retained versions for a path, newest first.
func (p *boundProtection) ListVersions(rel string) ([]VersionEntry, error) {
	var out []VersionEntry
	err := p.eachMeta("versions", func(data []byte) {
		var entry VersionEntry
		if json.Unmarshal(data, &entry) == nil && entry.OriginalPath == rel {
			out = append(out, entry)
		}
	})
	sortByTimeDesc(len(out), func(i int) time.Time { return out[i].CapturedAt },
		func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out, err
}

// ReadVersion returns a stored version's content.
func (p *boundProtection) ReadVersion(storedName string) ([]byte, error) {
	return p.root.ReadFile(storedName)
}

func (p *boundProtection) shouldVersion(rel string) bool {
	if p.policy.VersionDebounce <= 0 {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	last, seen := p.lastVersion[rel]
	return !seen || time.Since(last) >= p.policy.VersionDebounce
}

func (p *boundProtection) markVersioned(rel string) {
	p.mu.Lock()
	p.lastVersion[rel] = time.Now()
	p.mu.Unlock()
}

// pruneVersions enforces the per-file cap, dropping oldest first. The budget is
// shared by every writer: versions belong to the file, not to a user.
func (p *boundProtection) pruneVersions(rel string) {
	if p.policy.MaxVersionsPerFile <= 0 {
		return
	}
	versions, err := p.ListVersions(rel)
	if err != nil || len(versions) <= p.policy.MaxVersionsPerFile {
		return
	}
	for _, stale := range versions[p.policy.MaxVersionsPerFile:] {
		_ = p.root.Remove(stale.StoredName)
		_ = p.root.Remove(p.metaRel("versions", stale.ID))
	}
}

func (p *boundProtection) nextID() string {
	p.mu.Lock()
	p.seq++
	seq := p.seq
	p.mu.Unlock()
	return fmt.Sprintf("%d-%06d", time.Now().UnixNano(), seq)
}

func (p *boundProtection) metaRel(kind, id string) string {
	return stateMetaDir + "/" + kind + "-" + id + ".json"
}

// writeMeta writes through a temporary file and renames, so a crash cannot
// leave a half-written record that a later read would accept as valid.
func (p *boundProtection) writeMeta(kind, id string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	target := p.metaRel(kind, id)
	tmp := target + ".tmp"
	if err := p.root.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return p.root.Rename(tmp, target)
}

func (p *boundProtection) readMeta(kind, id string, out any) error {
	data, err := p.root.ReadFile(p.metaRel(kind, id))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (p *boundProtection) eachMeta(kind string, fn func([]byte)) error {
	items, err := p.root.ReadDir(stateMetaDir)
	if err != nil {
		return err
	}
	prefix := kind + "-"
	for _, item := range items {
		name := item.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := p.root.ReadFile(stateMetaDir + "/" + name)
		if err != nil {
			continue
		}
		fn(data)
	}
	return nil
}
