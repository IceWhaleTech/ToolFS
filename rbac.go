package toolfs

// RBAC access model built on top of ZimaOS grants.
//
// ToolFS does not define permissions. The single source of truth is the
// IceWhale-Files share table, whose rows read (samba_user, path, read_only).
// ToolFS consumes those rows and only ever *narrows* them, never widens.
//
// The narrowing that matters most for agents: ZimaOS has a single read_only
// dimension, so "writable" implies "deletable". That is precisely the root of
// accidental-deletion risk, so ToolFS splits deletion into its own capability
// that is withheld by default.

import (
	"path"
	"sort"
	"strings"
	"sync/atomic"
)

// AccessOp is a bitmask of the actions ToolFS distinguishes for access control.
// Named AccessOp rather than Operation because skills.go already uses the
// latter for chained skill operations.
type AccessOp uint16

const (
	// OpRead covers reading file content and stat.
	OpRead AccessOp = 1 << iota
	// OpList covers directory enumeration.
	OpList
	// OpCreate covers creating new files and directories.
	OpCreate
	// OpModify covers overwrite, append, truncate and setattr.
	OpModify
	// OpDelete covers unlink, rmdir, rename source and overwrite of an
	// existing target. Withheld unless AgentCapabilities.AllowDelete is set.
	OpDelete
)

// String renders an Operation for audit records and errors.
func (op AccessOp) String() string {
	if op == 0 {
		return "none"
	}
	names := []struct {
		bit  AccessOp
		name string
	}{
		{OpRead, "read"},
		{OpList, "list"},
		{OpCreate, "create"},
		{OpModify, "modify"},
		{OpDelete, "delete"},
	}
	parts := make([]string, 0, len(names))
	for _, n := range names {
		if op&n.bit != 0 {
			parts = append(parts, n.name)
		}
	}
	return strings.Join(parts, "|")
}

// Grant is one row of the ZimaOS share table, already normalized.
type Grant struct {
	// ID is the share row identity, used to disambiguate colliding names.
	ID int
	// Name is the share display name, preferred when building the namespace.
	Name string
	// Path is the canonical host path this grant covers.
	Path string
	// ReadOnly mirrors the share table column.
	ReadOnly bool
}

// Ops returns the operation set this grant confers, before capability narrowing.
// Note that OpDelete is never conferred here — it is released only by
// AgentCapabilities, and only for read-write grants.
func (g Grant) Ops() AccessOp {
	if g.ReadOnly {
		return OpRead | OpList
	}
	return OpRead | OpList | OpCreate | OpModify
}

// AgentCapabilities narrows what an agent may do inside its granted paths.
// It can only subtract from what ZimaOS allows, with the single exception of
// AllowDelete, which restores the delete right ToolFS withholds by default —
// and even then only on read-write grants.
type AgentCapabilities struct {
	// AllowDelete releases OpDelete on read-write grants. Default false.
	// A read-only grant can never be promoted to deletable.
	AllowDelete bool
	// AllowCommandExecution gates ToolFS's own command/skill API. It is not a
	// filesystem permission: a mounted filesystem cannot stop a process from
	// reading a script and then running `bash script.sh`.
	AllowCommandExecution bool
	// DenyPatterns are compiled at mount time against paths relative to the
	// grant root. An invalid pattern fails mount creation; a match failure is
	// never treated as "allow".
	DenyPatterns []string
}

// AccessSnapshot is an immutable view of one user's authorization at a point
// in time. It is published wholesale via atomic pointer swap, so an in-flight
// request keeps reading the snapshot it started with.
type AccessSnapshot struct {
	Username   string
	UserID     int
	Role       string
	Grants     []Grant
	matcher    *grantMatcher
	Generation uint64
}

// NewAccessSnapshot normalizes and indexes a set of grants.
//
// Normalization performs three jobs the share table requires:
//   - path alias folding (/media/ZimaOS-HD is a symlink to /DATA)
//   - trailing-slash folding (the table stores "/media/ZimaOS-HD/")
//   - conflict resolution when the same canonical path appears twice
func NewAccessSnapshot(username string, userID int, role string, grants []Grant, generation uint64) *AccessSnapshot {
	normalized := normalizeGrants(grants)
	return &AccessSnapshot{
		Username:   username,
		UserID:     userID,
		Role:       role,
		Grants:     normalized,
		matcher:    newGrantMatcher(normalized),
		Generation: generation,
	}
}

// Allows reports whether the snapshot permits op on hostPath, after applying
// capability narrowing.
//
// There is deliberately no `if role == "admin"` bypass here. ZimaOS is itself
// split on admin semantics — SMB is data-driven while the Web API short-circuits
// on role — and ToolFS follows the SMB side. An admin sees exactly the rows the
// share table holds for them, which the system writes automatically anyway.
func (s *AccessSnapshot) Allows(hostPath string, op AccessOp, caps AgentCapabilities) bool {
	if s == nil {
		return false
	}
	grant, ok := s.Lookup(hostPath)
	if !ok {
		return false
	}
	return grantAllows(grant, op, caps)
}

// Lookup returns the longest-prefix grant covering hostPath.
func (s *AccessSnapshot) Lookup(hostPath string) (Grant, bool) {
	if s == nil || s.matcher == nil {
		return Grant{}, false
	}
	return s.matcher.longestPrefix(canonicalHostPath(hostPath))
}

// grantAllows applies capability narrowing on top of a grant's own operations.
func grantAllows(g Grant, op AccessOp, caps AgentCapabilities) bool {
	allowed := g.Ops()
	// Delete is the one right ToolFS withholds by default, and it is only ever
	// released for read-write grants.
	if caps.AllowDelete && !g.ReadOnly {
		allowed |= OpDelete
	}
	return op != 0 && allowed&op == op
}

// canonicalHostPath folds the ZimaOS path aliases into one representation.
//
// Empirically /media/ZimaOS-HD is a symlink to /DATA, and the share table
// stores the aliased form with a trailing slash. Missing this makes older
// grants silently stop matching.
func canonicalHostPath(p string) string {
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, "\\", "/")
	for _, alias := range hostPathAliases {
		if p == alias.from {
			p = alias.to
			break
		}
		if strings.HasPrefix(p, alias.from+"/") {
			p = alias.to + p[len(alias.from):]
			break
		}
	}
	cleaned := path.Clean(p)
	if cleaned == "." {
		return "/"
	}
	return cleaned
}

// hostPathAliases folds alias roots onto their canonical target. The canonical
// side is the readlink -f result, so that one directory never yields two
// namespace entries.
var hostPathAliases = []struct{ from, to string }{
	{from: "/media/ZimaOS-HD", to: "/DATA"},
}

// normalizeGrants canonicalizes paths, folds duplicates and sorts the result.
//
// Conflict rule: when the same canonical path appears more than once, the most
// restrictive wins (read-only beats read-write). Merging towards the permissive
// side would let a stale or mistaken row widen access.
func normalizeGrants(grants []Grant) []Grant {
	byPath := make(map[string]Grant, len(grants))
	for _, g := range grants {
		g.Path = canonicalHostPath(g.Path)
		if g.Path == "" {
			continue
		}
		existing, seen := byPath[g.Path]
		if !seen {
			byPath[g.Path] = g
			continue
		}
		if g.ReadOnly {
			existing.ReadOnly = true
		}
		// Keep the lowest stable ID so namespace naming stays deterministic
		// across refreshes.
		if g.ID != 0 && (existing.ID == 0 || g.ID < existing.ID) {
			existing.ID = g.ID
			existing.Name = g.Name
		}
		byPath[g.Path] = existing
	}

	out := make([]Grant, 0, len(byPath))
	for _, g := range byPath {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// grantMatcher resolves the longest matching grant for a host path.
//
// Matching is by path segment, mirroring ZimaOS-Common/file.PathMatcher, so
// that "/DATA/Doc" never matches "/DATA/Documents". Nested grants keep
// longest-prefix semantics, which matters because parent-rw / child-ro really
// does occur in production share tables.
type grantMatcher struct {
	root *matcherNode
}

type matcherNode struct {
	children map[string]*matcherNode
	grant    *Grant
}

func newMatcherNode() *matcherNode {
	return &matcherNode{children: make(map[string]*matcherNode)}
}

func newGrantMatcher(grants []Grant) *grantMatcher {
	m := &grantMatcher{root: newMatcherNode()}
	for i := range grants {
		g := grants[i]
		node := m.root
		for _, seg := range hostPathSegments(g.Path) {
			child, ok := node.children[seg]
			if !ok {
				child = newMatcherNode()
				node.children[seg] = child
			}
			node = child
		}
		node.grant = &g
	}
	return m
}

// longestPrefix walks the trie and remembers the deepest node that carries a
// grant, which yields longest-prefix semantics with child grants narrowing
// their parents.
func (m *grantMatcher) longestPrefix(hostPath string) (Grant, bool) {
	if m == nil || m.root == nil {
		return Grant{}, false
	}
	node := m.root
	var best *Grant
	if node.grant != nil {
		best = node.grant
	}
	for _, seg := range hostPathSegments(hostPath) {
		child, ok := node.children[seg]
		if !ok {
			break
		}
		node = child
		if node.grant != nil {
			best = node.grant
		}
	}
	if best == nil {
		return Grant{}, false
	}
	return *best, true
}

// hostPathSegments splits an absolute host path into its non-empty segments.
func hostPathSegments(p string) []string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

// snapshotHolder publishes AccessSnapshots by atomic swap. Refresh builds a
// brand-new snapshot and matcher, so readers never observe a half-updated one.
type snapshotHolder struct {
	ptr atomic.Pointer[AccessSnapshot]
}

func (h *snapshotHolder) load() *AccessSnapshot { return h.ptr.Load() }

func (h *snapshotHolder) store(s *AccessSnapshot) { h.ptr.Store(s) }
