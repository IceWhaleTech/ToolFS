//go:build linux || darwin
// +build linux darwin

package toolfs

// Recovery: restore points, flashback, and task-level undo.
//
// The observation this is built on: an agent's high-frequency need is "undo
// everything that task just did", not "take me back to 3pm yesterday". Backup
// products offer the latter. So the primary interface here is undo by task,
// built on the trash and version stores rather than on filesystem snapshots —
// which matters because /DATA is ext4 and has no snapshot capability at all.
//
// Two rules that are easy to get wrong:
//
//   - Restore is authorized against *current* grants, never against the grants
//     held when the content was captured. Otherwise revoking access would not
//     stop the user from pulling their data back out of a version.
//   - Undo runs in reverse order and skips conflicts rather than clobbering.
//     Forward order breaks rename chains (A→B then B→C), and a blanket
//     overwrite would destroy work someone else did in the meantime.

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// MutationOp identifies what a recorded change did.
type MutationOp string

const (
	MutationCreate MutationOp = "create"
	MutationModify MutationOp = "modify"
	MutationDelete MutationOp = "delete"
	MutationRename MutationOp = "rename"
)

// ProtectedChange is one journal entry for a protected mutation.
//
// Named ProtectedChange rather than ChangeRecord because the latter is already
// taken by the snapshot module.
type ProtectedChange struct {
	Seq uint64 `json:"seq"`
	// SessionID and TaskID are the *agent-side* identifiers, supplied by the
	// caller. They have nothing to do with ToolFS's per-user mount
	// granularity: one user's mount carries many agent sessions.
	SessionID string `json:"session_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`

	Username string     `json:"username"`
	Op       MutationOp `json:"op"`
	// Path is relative to the grant root.
	Path string `json:"path"`
	// FromPath is set for renames, holding the original name.
	FromPath string `json:"from_path,omitempty"`

	// Recovery points at whatever lets this change be reversed: a version id
	// for modify, a trash id for delete, empty for create.
	Recovery string `json:"recovery,omitempty"`

	// AfterSize and AfterMtime capture the post-change state. Undo compares
	// against these to detect that someone else has since touched the path.
	AfterSize  int64     `json:"after_size"`
	AfterMtime time.Time `json:"after_mtime"`

	At time.Time `json:"at"`
}

// RestorePointKind distinguishes where a restore point came from.
type RestorePointKind string

const (
	// RestoreFromVersion is content preserved before an overwrite.
	RestoreFromVersion RestorePointKind = "version"
	// RestoreFromTrash is content preserved on deletion.
	RestoreFromTrash RestorePointKind = "trash"
)

// RestorePoint is one recoverable state of a path, from any source.
//
// Merging versions and trash into a single timeline is deliberate: a caller
// asking "what can this file go back to" should not have to know which
// mechanism happened to preserve each state.
type RestorePoint struct {
	ID   string           `json:"id"`
	Kind RestorePointKind `json:"kind"`
	Path string           `json:"path"`
	Size int64            `json:"size"`
	At   time.Time        `json:"at"`
	// stored is the grant-relative location of the preserved content.
	stored string
	isDir  bool
}

// UndoOutcome records what happened to one change during undo.
type UndoOutcome string

const (
	UndoApplied UndoOutcome = "applied"
	// UndoSkipped means the path changed after the recorded mutation, so
	// reversing it would destroy someone else's work.
	UndoSkipped UndoOutcome = "skipped"
	UndoFailed  UndoOutcome = "failed"
)

// UndoResult is the per-change outcome.
type UndoResult struct {
	Change  ProtectedChange `json:"change"`
	Outcome UndoOutcome     `json:"outcome"`
	Reason  string          `json:"reason,omitempty"`
}

// UndoReport summarizes an undo run.
//
// Partial success is the design: one conflict must not void the whole undo,
// and a conflict must not be silently overwritten either.
type UndoReport struct {
	Applied int          `json:"applied"`
	Skipped int          `json:"skipped"`
	Failed  int          `json:"failed"`
	Results []UndoResult `json:"results"`
}

// ChangeFilter selects which journal entries an operation applies to.
type ChangeFilter struct {
	SessionID string
	TaskID    string
	Since     time.Time
}

func (f ChangeFilter) matches(c ProtectedChange) bool {
	if f.SessionID != "" && c.SessionID != f.SessionID {
		return false
	}
	if f.TaskID != "" && c.TaskID != f.TaskID {
		return false
	}
	if !f.Since.IsZero() && c.At.Before(f.Since) {
		return false
	}
	return true
}

const stateJournalDir = toolfsStateDir + "/journal"

// recordChange appends a journal entry.
func (p *boundProtection) recordChange(change ProtectedChange) error {
	p.mu.Lock()
	p.seq++
	change.Seq = p.seq
	p.mu.Unlock()
	change.At = time.Now()

	// The journal directory is created once at init; doing it per record cost
	// an extra stat on every single mutation.
	data, err := json.Marshal(change)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%s/%020d.json", stateJournalDir, change.Seq)
	return p.root.WriteFile(name, data, 0o600)
}

// ListChanges returns matching journal entries in ascending sequence order.
func (p *boundProtection) ListChanges(filter ChangeFilter) ([]ProtectedChange, error) {
	items, err := p.root.ReadDir(stateJournalDir)
	if err != nil {
		return nil, nil // no journal yet is not an error
	}

	var out []ProtectedChange
	for _, item := range items {
		if !strings.HasSuffix(item.Name(), ".json") {
			continue
		}
		data, err := p.root.ReadFile(stateJournalDir + "/" + item.Name())
		if err != nil {
			continue
		}
		var change ProtectedChange
		if json.Unmarshal(data, &change) != nil {
			continue
		}
		if filter.matches(change) {
			out = append(out, change)
		}
	}
	// Names are zero-padded sequence numbers, so directory order is already
	// ascending; sort defensively in case a backend returns otherwise.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Seq < out[j-1].Seq; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// ListRestorePoints merges versions and trash for one path into one timeline,
// newest first.
func (p *boundProtection) ListRestorePoints(rel string) ([]RestorePoint, error) {
	var points []RestorePoint

	versions, err := p.ListVersions(rel)
	if err != nil {
		return nil, err
	}
	for _, v := range versions {
		points = append(points, RestorePoint{
			ID: v.ID, Kind: RestoreFromVersion, Path: v.OriginalPath,
			Size: v.Size, At: v.CapturedAt, stored: v.StoredName,
		})
	}

	trashed, err := p.ListTrash()
	if err != nil {
		return nil, err
	}
	for _, entry := range trashed {
		if entry.OriginalPath != rel {
			continue
		}
		points = append(points, RestorePoint{
			ID: entry.ID, Kind: RestoreFromTrash, Path: entry.OriginalPath,
			Size: entry.Size, At: entry.DeletedAt, stored: entry.StoredName,
			isDir: entry.IsDir,
		})
	}

	sortByTimeDesc(len(points), func(i int) time.Time { return points[i].At },
		func(i, j int) { points[i], points[j] = points[j], points[i] })
	return points, nil
}

// findRestorePoint locates a restore point by id.
func (p *boundProtection) findRestorePoint(rel, id string) (RestorePoint, bool) {
	points, err := p.ListRestorePoints(rel)
	if err != nil {
		return RestorePoint{}, false
	}
	for _, point := range points {
		if point.ID == id {
			return point, true
		}
	}
	return RestorePoint{}, false
}

// flashback writes a restore point's content back to its path.
//
// The current content, if any, is itself preserved first — so an unwanted
// flashback is recoverable too.
func (p *boundProtection) flashback(username string, point RestorePoint) error {
	if isReservedRel(point.Path) {
		return ErrNotAuthorized
	}

	// Bypass the debounce window here. Debouncing exists to stop a burst of
	// agent writes from producing a version each; a flashback is a deliberate,
	// destructive act, and failing to preserve what it overwrites would make an
	// unwanted flashback irreversible.
	if _, err := p.captureVersion(username, point.Path, true); err != nil {
		return err
	}

	switch point.Kind {
	case RestoreFromTrash:
		// A trashed entry may be a whole directory, so move it back rather
		// than trying to copy content.
		if _, err := p.root.Stat(point.Path); err == nil {
			if err := p.root.Remove(point.Path); err != nil {
				return err
			}
		}
		if parent := path.Dir(point.Path); parent != "." && parent != "/" {
			if err := p.root.MkdirAll(parent, 0o755); err != nil {
				return err
			}
		}
		return p.root.Rename(point.stored, point.Path)

	case RestoreFromVersion:
		content, err := p.root.ReadFile(point.stored)
		if err != nil {
			return err
		}
		if parent := path.Dir(point.Path); parent != "." && parent != "/" {
			if err := p.root.MkdirAll(parent, 0o755); err != nil {
				return err
			}
		}
		return p.root.WriteFile(point.Path, content, 0o644)

	default:
		return fmt.Errorf("toolfs: unknown restore point kind %q", point.Kind)
	}
}

// undoChanges reverses matching changes in reverse sequence order.
//
// Reverse order is required, not stylistic: a task that renames A→B and then
// B→C must be undone C→B then B→A. Undoing forward would collide on B.
func (p *boundProtection) undoChanges(username string, filter ChangeFilter) (*UndoReport, error) {
	changes, err := p.ListChanges(filter)
	if err != nil {
		return nil, err
	}

	report := &UndoReport{}
	for i := len(changes) - 1; i >= 0; i-- {
		change := changes[i]
		result := UndoResult{Change: change}

		if conflict := p.conflictReason(change); conflict != "" {
			result.Outcome = UndoSkipped
			result.Reason = conflict
			report.Skipped++
			report.Results = append(report.Results, result)
			continue
		}

		if err := p.undoOne(username, change); err != nil {
			result.Outcome = UndoFailed
			result.Reason = err.Error()
			report.Failed++
		} else {
			result.Outcome = UndoApplied
			report.Applied++
		}
		report.Results = append(report.Results, result)
	}
	return report, nil
}

// conflictReason reports why a change must not be reversed, or "" if it may be.
//
// The comparison is against the state recorded right after the change. If the
// path no longer matches, something else wrote it since — another agent
// session, another user sharing the directory, or SMB — and reversing would
// destroy that work.
func (p *boundProtection) conflictReason(change ProtectedChange) string {
	info, err := p.root.Stat(change.Path)

	if change.Op == MutationDelete {
		// The path should still be absent. If it exists again, someone
		// recreated it.
		if err == nil {
			return "path was recreated after the deletion"
		}
		return ""
	}

	if err != nil {
		return "path no longer exists"
	}
	if info.Size() != change.AfterSize {
		return fmt.Sprintf("size changed since the modification (%d → %d)", change.AfterSize, info.Size())
	}
	if !change.AfterMtime.IsZero() && !info.ModTime().Equal(change.AfterMtime) {
		return "modification time changed since the modification"
	}
	return ""
}

// undoOne reverses a single change.
func (p *boundProtection) undoOne(username string, change ProtectedChange) error {
	switch change.Op {
	case MutationCreate:
		// The file did not exist before; trash it rather than unlink, so an
		// unwanted undo is itself recoverable.
		_, err := p.Trash(username, change.Path)
		return err

	case MutationModify:
		if change.Recovery == "" {
			return errors.New("no preserved content for this modification")
		}
		point, ok := p.findRestorePoint(change.Path, change.Recovery)
		if !ok {
			return errors.New("preserved content has been pruned by retention")
		}
		return p.flashback(username, point)

	case MutationDelete:
		if change.Recovery == "" {
			return errors.New("no trash entry for this deletion")
		}
		_, err := p.Restore(change.Recovery)
		return err

	case MutationRename:
		if change.FromPath == "" {
			return errors.New("rename has no recorded source")
		}
		if _, err := p.root.Stat(change.FromPath); err == nil {
			return errors.New("original name is occupied again")
		}
		return p.root.Rename(change.Path, change.FromPath)

	default:
		return fmt.Errorf("toolfs: cannot undo operation %q", change.Op)
	}
}

// afterState captures the post-change size and mtime for conflict detection.
func (p *boundProtection) afterState(rel string) (int64, time.Time) {
	info, err := p.root.Stat(rel)
	if err != nil {
		return 0, time.Time{}
	}
	return info.Size(), info.ModTime()
}

// journal records a change, filling in the identity and agent-side session
// context from the view. Failures are swallowed on purpose: losing a journal
// entry must not fail the mutation the user asked for, and the protection
// copy itself is already durable.
func (p *boundProtection) journal(view *InstanceView, change ProtectedChange) {
	if view == nil || view.Identity == nil {
		return
	}
	change.Username = view.Identity.Username
	change.SessionID = view.SessionID
	change.TaskID = view.TaskID
	_ = p.recordChange(change)
}

// --- Instance-level recovery API ---
//
// These resolve a mount-relative path onto its grant, then delegate. They are
// where authorization is enforced: every one of them checks the *current*
// grants, so content preserved under an authorization that has since been
// revoked cannot be pulled back out.

// ListRestorePoints reports what a mount-relative path can be restored to.
func (v *InstanceView) ListRestorePoints(mountRel string) ([]RestorePoint, error) {
	entry, rel, err := v.Resolve(mountRel)
	if err != nil {
		return nil, err
	}
	if entry == nil || entry.protection == nil {
		return nil, nil
	}
	if !v.Allows(entry, rel, OpRead) {
		return nil, ErrNotAuthorized
	}
	return entry.protection.ListRestorePoints(rel)
}

// Flashback restores a mount-relative path to one of its restore points.
func (v *InstanceView) Flashback(mountRel, restorePointID string) error {
	entry, rel, err := v.Resolve(mountRel)
	if err != nil {
		return err
	}
	if entry == nil || entry.protection == nil {
		return ErrReadOnlyMount
	}
	// Writing back requires the write right the caller holds *now*.
	if !v.Allows(entry, rel, OpModify) {
		return ErrNotAuthorized
	}
	point, ok := entry.protection.findRestorePoint(rel, restorePointID)
	if !ok {
		return ErrTrashEntryNotFound
	}
	return entry.protection.flashback(v.Identity.Username, point)
}

// ListChanges reports journal entries across every writable grant.
func (v *InstanceView) ListChanges(filter ChangeFilter) ([]ProtectedChange, error) {
	var out []ProtectedChange
	for _, name := range v.order {
		entry := v.entries[name]
		if entry.protection == nil {
			continue
		}
		changes, err := entry.protection.ListChanges(filter)
		if err != nil {
			return nil, err
		}
		out = append(out, changes...)
	}
	return out, nil
}

// UndoChanges reverses matching changes across every writable grant.
//
// This is the capability agents actually need: one call undoes everything a
// task did, in reverse order, skipping anything that has been touched since
// rather than clobbering it.
func (v *InstanceView) UndoChanges(filter ChangeFilter) (*UndoReport, error) {
	combined := &UndoReport{}
	for _, name := range v.order {
		entry := v.entries[name]
		if entry.protection == nil {
			continue
		}
		// Undo writes, so it needs the write right currently held on the grant.
		if !v.Allows(entry, "", OpModify) {
			continue
		}
		report, err := entry.protection.undoChanges(v.Identity.Username, filter)
		if err != nil {
			return nil, err
		}
		combined.Applied += report.Applied
		combined.Skipped += report.Skipped
		combined.Failed += report.Failed
		combined.Results = append(combined.Results, report.Results...)
	}
	return combined, nil
}
