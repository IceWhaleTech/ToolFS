//go:build linux || darwin
// +build linux darwin

package toolfs

// Retention: reclaiming space held by the protection area.
//
// Deletion is a rename into the trash, which is why it is O(1) — but it also
// means deletion never frees space. Without reclamation the protection area
// grows without bound, and once the disk is full nothing works, including the
// trash that deletion depends on.
//
// Reclamation destroys the ability to recover, so it is deliberately
// conservative:
//
//   - A minimum age is always honoured. Something deleted moments ago is never
//     purged out from under the user, however tight space is.
//   - Oldest first. What has survived longest is least likely to be wanted.
//   - Automatic purging only runs when space is actually short, and reports
//     what it removed rather than doing it silently.

import (
	"fmt"
	"time"
)

// RetentionPolicy bounds how long and how much the protection area keeps.
type RetentionPolicy struct {
	// MinAge is never violated. Entries younger than this are kept even when
	// space is short — the whole point of a trash is that a mistake noticed
	// immediately is recoverable.
	MinAge time.Duration
	// MaxAge drops entries older than this regardless of space.
	MaxAge time.Duration
	// MaxTotalBytes caps the protection area. Zero means no cap.
	MaxTotalBytes int64
	// TargetFreeBytes is the free space an automatic purge tries to reach.
	// Zero disables space-triggered purging.
	TargetFreeBytes int64
}

// DefaultRetentionPolicy returns conservative settings.
func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		MinAge:          24 * time.Hour,
		MaxAge:          30 * 24 * time.Hour,
		MaxTotalBytes:   0,
		TargetFreeBytes: 4 << 30, // 4 GiB
	}
}

// PurgeReport describes what a purge removed.
type PurgeReport struct {
	TrashRemoved    int   `json:"trash_removed"`
	VersionsRemoved int   `json:"versions_removed"`
	BytesReclaimed  int64 `json:"bytes_reclaimed"`
	// KeptYoung counts entries left alone solely because of MinAge. A non-zero
	// value alongside continued space pressure is the signal that MinAge, not
	// the purge, is what is holding space.
	KeptYoung int `json:"kept_young"`
}

// PurgeTrash reclaims space according to the policy.
//
// This is an administrative operation: it destroys recoverable data. Callers
// must gate it on the caller's role and audit it.
func (p *boundProtection) PurgeTrash(policy RetentionPolicy) (*PurgeReport, error) {
	report := &PurgeReport{}
	now := time.Now()

	entries, err := p.ListTrash()
	if err != nil {
		return report, err
	}
	versions, err := p.allVersions()
	if err != nil {
		return report, err
	}

	// ListTrash returns newest first; walk from the end so the oldest go first.
	reverse(entries)
	reverse(versions)

	var total int64
	for _, e := range entries {
		total += e.Size
	}
	for _, v := range versions {
		total += v.Size
	}

	// A single free-space reading drives the whole pass; re-reading per entry
	// would be both slow and inconsistent.
	needSpace := int64(0)
	if policy.TargetFreeBytes > 0 {
		if free, err := p.root.FreeBytes(); err == nil && int64(free) < policy.TargetFreeBytes {
			needSpace = policy.TargetFreeBytes - int64(free)
		}
	}
	overCap := int64(0)
	if policy.MaxTotalBytes > 0 && total > policy.MaxTotalBytes {
		overCap = total - policy.MaxTotalBytes
	}

	for _, entry := range entries {
		age := now.Sub(entry.DeletedAt)
		if policy.MinAge > 0 && age < policy.MinAge {
			report.KeptYoung++
			continue
		}
		expired := policy.MaxAge > 0 && age > policy.MaxAge
		if !expired && needSpace <= 0 && overCap <= 0 {
			break // nothing left to justify removing this or anything newer
		}
		if err := p.dropTrashEntry(entry); err != nil {
			continue
		}
		report.TrashRemoved++
		report.BytesReclaimed += entry.Size
		needSpace -= entry.Size
		overCap -= entry.Size
	}

	for _, version := range versions {
		age := now.Sub(version.CapturedAt)
		if policy.MinAge > 0 && age < policy.MinAge {
			report.KeptYoung++
			continue
		}
		expired := policy.MaxAge > 0 && age > policy.MaxAge
		if !expired && needSpace <= 0 && overCap <= 0 {
			break
		}
		if err := p.dropVersion(version); err != nil {
			continue
		}
		report.VersionsRemoved++
		report.BytesReclaimed += version.Size
		needSpace -= version.Size
		overCap -= version.Size
	}

	return report, nil
}

// PurgeIfLowOnSpace runs a purge only when free space is below the target.
//
// Returns nil without error when there is nothing to do, so callers can invoke
// it on a timer without special-casing the common case.
func (p *boundProtection) PurgeIfLowOnSpace(policy RetentionPolicy) (*PurgeReport, error) {
	if policy.TargetFreeBytes <= 0 {
		return nil, nil
	}
	free, err := p.root.FreeBytes()
	if err != nil {
		return nil, err
	}
	if int64(free) >= policy.TargetFreeBytes {
		return nil, nil
	}
	return p.PurgeTrash(policy)
}

// dropTrashEntry removes one trashed entry and its metadata.
func (p *boundProtection) dropTrashEntry(entry TrashEntry) error {
	if entry.IsDir {
		// A trashed directory holds a whole subtree; Remove only unlinks an
		// empty one, so this needs the recursive form.
		if err := p.removeTree(entry.StoredName); err != nil {
			return err
		}
	} else if err := p.root.Remove(entry.StoredName); err != nil {
		return err
	}
	return p.root.Remove(p.metaRel("trash", entry.ID))
}

// dropVersion removes one stored version and its metadata.
func (p *boundProtection) dropVersion(version VersionEntry) error {
	if err := p.root.Remove(version.StoredName); err != nil {
		return err
	}
	return p.root.Remove(p.metaRel("versions", version.ID))
}

// removeTree deletes a directory and everything beneath it, depth first.
func (p *boundProtection) removeTree(rel string) error {
	items, err := p.root.ReadDir(rel)
	if err != nil {
		return p.root.Remove(rel) // not a directory, or already gone
	}
	for _, item := range items {
		child := rel + "/" + item.Name()
		if item.IsDir() {
			if err := p.removeTree(child); err != nil {
				return err
			}
			continue
		}
		if err := p.root.Remove(child); err != nil {
			return err
		}
	}
	return p.root.Remove(rel)
}

// allVersions returns every stored version, newest first.
func (p *boundProtection) allVersions() ([]VersionEntry, error) {
	var out []VersionEntry
	err := p.eachMeta("versions", func(data []byte) {
		var entry VersionEntry
		if unmarshalQuiet(data, &entry) {
			out = append(out, entry)
		}
	})
	sortByTimeDesc(len(out), func(i int) time.Time { return out[i].CapturedAt },
		func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out, err
}

// UsageBytes reports how much the protection area currently holds.
func (p *boundProtection) UsageBytes() (int64, error) {
	var total int64
	entries, err := p.ListTrash()
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		total += e.Size
	}
	versions, err := p.allVersions()
	if err != nil {
		return total, err
	}
	for _, v := range versions {
		total += v.Size
	}
	return total, nil
}

// String renders a purge report for logs and audit records.
func (r *PurgeReport) String() string {
	if r == nil {
		return "no purge"
	}
	return fmt.Sprintf("purged %d trash + %d versions, reclaimed %d bytes, kept %d young",
		r.TrashRemoved, r.VersionsRemoved, r.BytesReclaimed, r.KeptYoung)
}

// reverse flips a slice in place.
func reverse[T any](items []T) {
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
}
