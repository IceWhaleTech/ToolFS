package toolfs

// Per-user namespace construction.
//
// InstanceView is the immutable projection of one user's authorization into a
// filesystem namespace. Granularity is per-user, not per-session: one user has
// one mount point and one instance, shared by all of that user's agent
// conversations.
//
// The namespace is built exclusively from grants. Virtual backends (memory,
// rag, skill mounts) are deliberately absent — holding a file grant must never
// imply access to those, and the RBAC MVP does not expose them at all.

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// ErrNoIdentity indicates a mount was requested without a resolved identity.
var ErrNoIdentity = errors.New("toolfs: mount requires a resolved ZimaOS identity")

// NamespaceEntry is one top-level directory in a user's mount view.
type NamespaceEntry struct {
	// GrantID ties the entry back to its share row.
	GrantID int
	// VirtualName is what the agent sees at the root of its mount.
	VirtualName string
	// HostPath is the canonical host path this entry projects.
	HostPath string
	// ReadOnly mirrors the grant.
	ReadOnly bool

	// root is the constrained handle for this entry. Every IO under this
	// entry is issued through it, so the kernel enforces containment even if
	// a component of the path is swapped for a symlink mid-flight.
	root RootHandle
}

// InstanceView is an immutable namespace snapshot for one mounted user.
type InstanceView struct {
	Identity *ZimaIdentity
	Access   *AccessSnapshot
	Caps     AgentCapabilities

	// Generation mirrors the AccessSnapshot generation, so a permission
	// decision cache can be invalidated by comparing one integer.
	Generation uint64

	// entries is keyed by VirtualName.
	entries map[string]*NamespaceEntry
	// order is the stable sorted listing used by Readdir.
	order []string
}

// BuildInstanceView projects an authorization snapshot into a namespace.
//
// Naming rules, in order:
//   - prefer the share display name
//   - fall back to the base name of the host path
//   - on collision, append the stable grant id ("Documents~42") rather than
//     silently letting one entry shadow another
func BuildInstanceView(identity *ZimaIdentity, access *AccessSnapshot, caps AgentCapabilities) (*InstanceView, error) {
	if identity == nil {
		return nil, ErrNoIdentity
	}
	if access == nil {
		return nil, ErrNoGrants
	}
	if err := validateDenyPatterns(caps.DenyPatterns); err != nil {
		return nil, err
	}

	view := &InstanceView{
		Identity:   identity,
		Access:     access,
		Caps:       caps,
		Generation: access.Generation,
		entries:    make(map[string]*NamespaceEntry, len(access.Grants)),
	}

	for _, grant := range access.Grants {
		name := entryName(grant)
		if _, taken := view.entries[name]; taken {
			name = fmt.Sprintf("%s~%d", name, grant.ID)
		}
		if _, stillTaken := view.entries[name]; stillTaken {
			// Two grants with the same id cannot happen after normalization;
			// refuse rather than shadow.
			return nil, fmt.Errorf("toolfs: duplicate namespace entry %q", name)
		}
		view.entries[name] = &NamespaceEntry{
			GrantID:     grant.ID,
			VirtualName: name,
			HostPath:    grant.Path,
			ReadOnly:    grant.ReadOnly,
		}
	}

	view.order = make([]string, 0, len(view.entries))
	for name := range view.entries {
		view.order = append(view.order, name)
	}
	sort.Strings(view.order)

	return view, nil
}

// entryName derives the visible name for a grant.
func entryName(g Grant) string {
	if name := sanitizeEntryName(g.Name); name != "" {
		return name
	}
	if base := sanitizeEntryName(path.Base(g.Path)); base != "" {
		return base
	}
	return fmt.Sprintf("grant~%d", g.ID)
}

// sanitizeEntryName rejects names that could escape or shadow the namespace.
func sanitizeEntryName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return ""
	}
	if strings.ContainsAny(name, "/\\") || strings.IndexByte(name, 0) >= 0 {
		return ""
	}
	// Reserved names must never be produced by user data; the control view
	// and the protection area own them.
	if name == toolfsControlDir || name == toolfsStateDir {
		return ""
	}
	return name
}

const (
	// toolfsControlDir is the read-only control view reserved at mount root.
	toolfsControlDir = ".toolfs"
	// toolfsStateDir is the protection area name, reserved globally.
	toolfsStateDir = ".toolfs-state"
)

// Entries returns the stable, sorted list of visible names.
func (v *InstanceView) Entries() []string {
	if v == nil {
		return nil
	}
	out := make([]string, len(v.order))
	copy(out, v.order)
	return out
}

// Entry looks up one namespace entry by visible name.
func (v *InstanceView) Entry(name string) (*NamespaceEntry, bool) {
	if v == nil {
		return nil, false
	}
	entry, ok := v.entries[name]
	return entry, ok
}

// Resolve maps a path relative to the mount root onto an entry plus the
// remainder beneath it.
//
// The input must already be sanitized; this function does not clean "..".
func (v *InstanceView) Resolve(relPath string) (*NamespaceEntry, string, error) {
	if v == nil {
		return nil, "", ErrNoIdentity
	}
	trimmed := strings.Trim(relPath, "/")
	if trimmed == "" {
		return nil, "", nil // the mount root itself
	}
	segments := strings.SplitN(trimmed, "/", 2)
	entry, ok := v.entries[segments[0]]
	if !ok {
		return nil, "", ErrNotAuthorized
	}
	if len(segments) == 1 {
		return entry, "", nil
	}
	return entry, segments[1], nil
}

// ErrNotAuthorized is returned when a path is outside the granted namespace.
// FUSE maps it to ENOENT rather than EACCES so the namespace shape is not
// disclosed to an unauthorized caller.
var ErrNotAuthorized = errors.New("toolfs: path is not within any grant")

// Allows checks an operation against a resolved entry and the current caps.
func (v *InstanceView) Allows(entry *NamespaceEntry, rel string, op AccessOp) bool {
	if v == nil || entry == nil {
		return false
	}
	if v.isDenied(rel) {
		return false
	}
	hostPath := entry.HostPath
	if rel != "" {
		hostPath = path.Join(hostPath, rel)
	}
	// Re-check against the authorization snapshot rather than trusting the
	// entry alone, so nested narrowing grants (parent rw, child ro) apply.
	return v.Access.Allows(hostPath, op, v.Caps)
}

// isDenied applies DenyPatterns against the grant-relative path.
// A pattern error is treated as denied; a match failure is never "allow".
func (v *InstanceView) isDenied(rel string) bool {
	if rel == "" || len(v.Caps.DenyPatterns) == 0 {
		return false
	}
	for _, pattern := range v.Caps.DenyPatterns {
		matched, err := path.Match(pattern, rel)
		if err != nil || matched {
			return true
		}
		// Also match on any leading directory component so that "**/.ssh/**"
		// style intent still catches nested paths.
		if strings.Contains(rel, "/") {
			for _, prefix := range prefixPaths(rel) {
				if ok, err := path.Match(pattern, prefix); err != nil || ok {
					return true
				}
			}
		}
	}
	return false
}

// prefixPaths yields every ancestor of rel, closest first.
func prefixPaths(rel string) []string {
	parts := strings.Split(rel, "/")
	out := make([]string, 0, len(parts))
	for i := 1; i < len(parts); i++ {
		out = append(out, strings.Join(parts[:i], "/"))
	}
	return out
}

// validateDenyPatterns rejects malformed patterns at build time rather than
// discovering them during a request, where a match error would be ambiguous.
func validateDenyPatterns(patterns []string) error {
	for _, pattern := range patterns {
		if _, err := path.Match(pattern, "probe"); err != nil {
			return fmt.Errorf("toolfs: invalid deny pattern %q: %w", pattern, err)
		}
	}
	return nil
}

// openRoots opens a constrained handle per entry. Callers must call
// closeRoots when the view is retired.
func (v *InstanceView) openRoots() error {
	backend := newSecureBackend()
	for _, name := range v.order {
		entry := v.entries[name]
		root, err := backend.OpenRoot(entry.HostPath)
		if err != nil {
			v.closeRoots()
			return fmt.Errorf("toolfs: cannot open grant root %q: %w", entry.HostPath, err)
		}
		entry.root = root
	}
	return nil
}

// closeRoots releases every held descriptor. Safe to call more than once.
func (v *InstanceView) closeRoots() {
	if v == nil {
		return
	}
	for _, entry := range v.entries {
		if entry.root != nil {
			_ = entry.root.Close()
			entry.root = nil
		}
	}
}

// BuildInstanceViewForUser resolves identity, pulls grants and projects them.
func BuildInstanceViewForUser(
	ctx context.Context,
	resolver IdentityResolver,
	provider PermissionProvider,
	agentUserID string,
	caps AgentCapabilities,
) (*InstanceView, error) {
	identity, err := resolver.Resolve(ctx, agentUserID)
	if err != nil {
		return nil, err
	}
	access, err := provider.GrantsFor(ctx, identity.Username)
	if err != nil {
		return nil, err
	}
	return BuildInstanceView(identity, access, caps)
}
