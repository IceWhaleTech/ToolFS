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
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNoIdentity indicates a mount was requested without a resolved identity.
var ErrNoIdentity = errors.New("toolfs: mount requires a resolved ZimaOS identity")

// NamespaceEntry is one grant, placed in the mount view at its view path.
type NamespaceEntry struct {
	// GrantID ties the entry back to its share row.
	GrantID int
	// VirtualName is the entry's own name — the last segment of its view path.
	VirtualName string
	// ViewPath is where the grant appears in the mount view, after the alias
	// table has rewritten its host spelling (e.g. /DATA/.home/casaos/topics/Work
	// → /Home/casaos/topics/Work). Absolute, cleaned. The segments above the
	// last are synthetic, traversal-only directories that lead here.
	ViewPath string
	// HostPath is the host path the root handle is opened at. For a directory
	// grant it is the granted path; for a grant on a single file it is the
	// file's parent directory (a file cannot be a root), with fileName naming
	// the one file exposed under it.
	HostPath string
	// fileName is set only for a grant on a single file: the file's own name,
	// relative to HostPath (its parent). Empty for a directory grant.
	fileName string
	// ReadOnly mirrors the grant.
	ReadOnly bool

	// root is the constrained handle for this entry. Every IO under this
	// entry is issued through it, so the kernel enforces containment even if
	// a component of the path is swapped for a symlink mid-flight.
	root RootHandle
	// protection is this grant's trash and version store. It lives under the
	// grant root and is reached through the same handle, which is what keeps
	// trash renames free of both TOCTOU and EXDEV.
	protection *boundProtection
}

// InstanceView is an immutable namespace snapshot for one mounted user.
type InstanceView struct {
	Identity *ZimaIdentity
	Access   *AccessSnapshot
	Caps     AgentCapabilities

	// Generation mirrors the AccessSnapshot generation, so a permission
	// decision cache can be invalidated by comparing one integer.
	Generation uint64

	// Policy governs trash and version behaviour for this instance.
	Policy ProtectionPolicy

	// BuiltAt is when this view was projected. Virtual nodes — the mount root,
	// the control view — have no backing file to borrow a time from, and a
	// zero time reads as 1970; the projection moment is the honest stand-in.
	BuiltAt time.Time

	// CacheTimeout mirrors MountConfig.CacheTimeout, so the node handlers can
	// decide whether to ask the kernel to cache.
	CacheTimeout time.Duration

	// ZeroCopyRead mirrors MountConfig.ZeroCopyRead.
	ZeroCopyRead bool

	// SessionID and TaskID are agent-side labels stamped onto journal entries
	// so an undo can select "everything that task did". They are supplied by
	// the caller and are unrelated to the per-user mount granularity.
	SessionID string
	TaskID    string

	// decisions memoizes the permitted operation set per (entry, relative path).
	//
	// Every FUSE operation asks the same question, and answering it costs a
	// path join, alias normalization and a trie walk — several allocations on
	// a path that gets walked once per LOOKUP, OPEN and READ. The view is
	// immutable for one authorization generation, so memoizing is safe: a
	// permission change produces a new view and discards this cache with it.
	decisions sync.Map
	// decisionCount bounds the table. Past the cap new entries are simply not
	// stored — a bound without the complexity of eviction.
	decisionCount atomic.Int64

	// mu guards the projection — root, grants, Access and Generation — so it can
	// be rebuilt in place when the authority's grants change (reproject) while
	// FUSE reads walk it concurrently. Decisions self-invalidate on generation,
	// so they need no lock; the tree structure does.
	mu sync.RWMutex
	// root is the synthetic root of the view tree. Its children are the
	// top-level entries (the storage spaces and /Home); grants sit at their
	// view-path leaves, with synthetic traversal-only directories above them.
	root *viewNode
	// grants is every grant leaf, in view-path order. openRoots opens one host
	// handle per leaf; the control view walks these to list undo state.
	grants []*NamespaceEntry
	// aliases is the host→view rewrite table this projection was built with.
	// The FUSE layer reuses it to suppress a child whose real view is
	// elsewhere (e.g. .home under a /DATA grant belongs to /Home, not here).
	aliases []PathAlias
}

// viewNode is one node in the projected namespace tree. An interior node is
// synthetic — no host backing — and exists only to lead to grants beneath it.
// A node whose entry is non-nil is a grant mounted exactly at this view path;
// a grant node never carries synthetic children (a deeper grant under it is
// absorbed, its refinement applied by the host-path decision trie instead).
type viewNode struct {
	name     string
	children map[string]*viewNode
	order    []string
	entry    *NamespaceEntry
}

func newViewNode(name string) *viewNode {
	return &viewNode{name: name, children: map[string]*viewNode{}}
}

// child returns a node's child by segment name.
func (n *viewNode) child(name string) (*viewNode, bool) {
	c, ok := n.children[name]
	return c, ok
}

// childNames returns the node's children, sorted and stable.
func (n *viewNode) childNames() []string {
	out := make([]string, len(n.order))
	copy(out, n.order)
	return out
}

// finalize sorts every node's child order once the tree is fully built.
func (n *viewNode) finalize() {
	n.order = n.order[:0]
	for name := range n.children {
		n.order = append(n.order, name)
	}
	sort.Strings(n.order)
	for _, c := range n.children {
		c.finalize()
	}
}

// BuildInstanceView projects an authorization snapshot into a namespace tree.
//
// Each grant's host path is rewritten through the alias table into a view path
// (see viewpath.go), then placed at that path in the tree. The segments above
// the leaf become synthetic, traversal-only directories, shared between grants
// that live under the same space. A grant whose view path falls under another
// grant is absorbed — reached through its ancestor and refined by the decision
// trie — rather than mounted twice. Grants are inserted shallowest-first so an
// ancestor always claims its node before a descendant reaches it.
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

	aliases := caps.ViewAliases
	if aliases == nil {
		aliases = DefaultZimaViewAliases
	}

	view := &InstanceView{
		Identity:   identity,
		Access:     access,
		Caps:       caps,
		BuiltAt:    time.Now(),
		Generation: access.Generation,
		Policy:     DefaultProtectionPolicy(),
		aliases:    aliases,
		root:       newViewNode(""),
	}

	// Prepare entries with their view paths, then insert shallowest-first.
	pending := make([]*NamespaceEntry, 0, len(access.Grants))
	for _, grant := range access.Grants {
		vp := viewPath(grant.Path, aliases)
		if len(viewSegments(vp)) == 0 {
			// Maps to the view root itself — nothing to mount there. A grant on
			// "/" or a bare stripped prefix is degenerate; skip it rather than
			// shadow the whole tree.
			continue
		}
		pending = append(pending, &NamespaceEntry{
			GrantID:     grant.ID,
			VirtualName: path.Base(vp),
			ViewPath:    vp,
			HostPath:    grant.Path,
			ReadOnly:    grant.ReadOnly,
		})
	}
	sort.SliceStable(pending, func(i, j int) bool {
		di, dj := len(viewSegments(pending[i].ViewPath)), len(viewSegments(pending[j].ViewPath))
		if di != dj {
			return di < dj
		}
		return pending[i].ViewPath < pending[j].ViewPath
	})

	for _, entry := range pending {
		if view.insertGrant(entry) {
			view.grants = append(view.grants, entry)
		}
	}
	view.root.finalize()

	return view, nil
}

// insertGrant places one grant at its view path, creating synthetic ancestors.
// It returns false when the grant is absorbed (a shallower grant already owns a
// node on the way down) or its path is unusable, so it should not be mounted.
func (v *InstanceView) insertGrant(entry *NamespaceEntry) bool {
	segments := viewSegments(entry.ViewPath)
	node := v.root
	for _, segment := range segments {
		name := sanitizeEntryName(segment)
		if name == "" {
			return false
		}
		if node.entry != nil {
			// A grant sits above this one: absorbed. Its refinement still
			// applies through the host-path decision trie during traversal.
			return false
		}
		child, ok := node.children[name]
		if !ok {
			child = newViewNode(name)
			node.children[name] = child
		}
		node = child
	}
	if node.entry != nil {
		// Two grants at the same view path. Distinct host paths that map to the
		// same view are the same underlying data (the double-mounted system
		// disk) — keep the first and drop this one rather than shadow.
		return false
	}
	// A shallower grant landing where deeper grants already built synthetic
	// children absorbs them (defensive: shallowest-first insertion normally
	// prevents this).
	node.children = map[string]*viewNode{}
	node.entry = entry
	return true
}

// childDiverges reports whether a child of a grant belongs to a different view
// space than the one it would list under — the .home subtree under a /DATA
// grant is the case that matters: it is canonically /Home, so it must not also
// appear beneath /ZimaOS-HD. Detected generally by asking the alias table where
// the child's host path really projects.
func (v *InstanceView) childDiverges(entry *NamespaceEntry, rel, name string) bool {
	childHost := path.Join(entry.HostPath, rel, name)
	expected := path.Join(entry.ViewPath, rel, name)
	return viewPath(childHost, v.aliases) != expected
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

// Entries returns the top-level view names, sorted — the storage spaces and
// /Home, i.e. the children of the tree root. These are what the mount root
// lists, alongside the control view.
func (v *InstanceView) Entries() []string {
	if v == nil {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.root == nil {
		return nil
	}
	return v.root.childNames()
}

// rootChild returns a top-level view node by name.
func (v *InstanceView) rootChild(name string) (*viewNode, bool) {
	if v == nil {
		return nil, false
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.root == nil {
		return nil, false
	}
	return v.root.child(name)
}

// Grants returns every grant leaf, in view-path order. The control view walks
// these to list undo state under each grant's full view path.
func (v *InstanceView) Grants() []*NamespaceEntry {
	if v == nil {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]*NamespaceEntry, len(v.grants))
	copy(out, v.grants)
	return out
}

// Resolve maps a path relative to the mount root onto the grant that governs it
// plus the remainder beneath that grant.
//
// It walks the view tree segment by segment, descending synthetic directories
// until it reaches a grant; the unconsumed remainder is the grant-relative
// path. A path that ends on a synthetic directory (no grant to act on) or names
// an unknown segment is ErrNotAuthorized — callers here (undo/recovery) only
// ever act on real files under a grant. The input must already be sanitized;
// this function does not clean "..".
func (v *InstanceView) Resolve(relPath string) (*NamespaceEntry, string, error) {
	if v == nil {
		return nil, "", ErrNoIdentity
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.root == nil {
		return nil, "", ErrNoIdentity
	}
	trimmed := strings.Trim(relPath, "/")
	if trimmed == "" {
		return nil, "", nil // the mount root itself
	}
	segments := strings.Split(trimmed, "/")
	node := v.root
	for i, segment := range segments {
		if node.entry != nil {
			return node.entry, strings.Join(segments[i:], "/"), nil
		}
		child, ok := node.child(segment)
		if !ok {
			return nil, "", ErrNotAuthorized
		}
		node = child
	}
	if node.entry != nil {
		return node.entry, "", nil
	}
	return nil, "", ErrNotAuthorized
}

// ErrNotAuthorized is returned when a path is outside the granted namespace.
// FUSE maps it to ENOENT rather than EACCES so the namespace shape is not
// disclosed to an unauthorized caller.
var ErrNotAuthorized = errors.New("toolfs: path is not within any grant")

// maxCachedDecisions bounds the memoization table.
const maxCachedDecisions = 50000

// Allows checks an operation against a resolved entry and the current caps.
func (v *InstanceView) Allows(entry *NamespaceEntry, rel string, op AccessOp) bool {
	if v == nil || entry == nil {
		return false
	}
	return v.allowedOps(entry, rel)&op == op
}

// cachedDecision carries the generation the answer was computed under, so a
// changed authorization cannot be served from a stale entry.
type cachedDecision struct {
	generation uint64
	ops        AccessOp
}

// allowedOps returns the permitted operation set, memoized per path.
//
// The memo self-invalidates on generation change rather than relying on the
// view being replaced wholesale. Views normally are replaced, but an in-place
// swap of Access would otherwise silently keep serving the old answers — which
// for a revoked grant means continuing to allow access.
func (v *InstanceView) allowedOps(entry *NamespaceEntry, rel string) AccessOp {
	v.mu.RLock()
	access := v.Access
	v.mu.RUnlock()

	generation := access.CurrentGeneration()

	key := entry.VirtualName + "\x00" + rel
	if cached, ok := v.decisions.Load(key); ok {
		if decision := cached.(cachedDecision); decision.generation == generation {
			return decision.ops
		}
	}

	allowed := v.computeAllowedOps(access, entry, rel)
	decision := cachedDecision{generation: generation, ops: allowed}
	if _, loaded := v.decisions.Swap(key, decision); !loaded {
		if v.decisionCount.Add(1) > maxCachedDecisions {
			// Past the cap, stop growing: drop what we just added rather than
			// evicting, which keeps the bound without eviction machinery.
			v.decisions.Delete(key)
			v.decisionCount.Add(-1)
		}
	}
	return allowed
}

func (v *InstanceView) computeAllowedOps(access *AccessSnapshot, entry *NamespaceEntry, rel string) AccessOp {
	if v.isDenied(rel) {
		return 0
	}
	hostPath := entry.HostPath
	if rel != "" {
		hostPath = path.Join(hostPath, rel)
	}
	// Re-check against the authorization snapshot rather than trusting the
	// entry alone, so nested narrowing grants (parent rw, child ro) apply.
	return access.AllowedOps(hostPath, v.Caps)
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
	for _, entry := range v.grants {
		// A grant on a single file cannot be a root — a root must be a directory.
		// Open the file's parent instead and expose just that file; the entry
		// keeps its own view path, so nothing about the projection changes.
		if info, err := os.Stat(entry.HostPath); err == nil && !info.IsDir() {
			entry.fileName = path.Base(entry.HostPath)
			entry.HostPath = path.Dir(entry.HostPath)
		}
		root, err := backend.OpenRoot(entry.HostPath)
		if err != nil {
			v.closeRoots()
			return fmt.Errorf("toolfs: cannot open grant root %q: %w", entry.HostPath, err)
		}
		entry.root = root

		// Read-only grants get no protection area: nothing can be deleted or
		// overwritten through them, so there is nothing to preserve, and
		// creating state directories under a read-only grant would be the only
		// write ToolFS ever made there.
		if !entry.ReadOnly {
			protection, err := newBoundProtection(root, v.Policy)
			if err != nil {
				v.closeRoots()
				return fmt.Errorf("toolfs: cannot prepare protection for %q: %w", entry.HostPath, err)
			}
			entry.protection = protection
		}
	}
	return nil
}

// closeRoots releases every held descriptor. Safe to call more than once.
func (v *InstanceView) closeRoots() {
	if v == nil {
		return
	}
	for _, entry := range v.grants {
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
	app string,
	caps AgentCapabilities,
) (*InstanceView, error) {
	identity, err := resolver.Resolve(ctx, agentUserID)
	if err != nil {
		return nil, err
	}
	access, err := provider.GrantsFor(ctx, identity, app)
	if err != nil {
		return nil, err
	}
	return BuildInstanceView(identity, access, caps)
}

// BuildInstanceViewForIdentity projects a view for an already-resolved identity,
// skipping the resolver. reproject uses it so a rebuild reuses the identity the
// mount was created with — never re-resolving, which for an account passed in by
// the caller (not in the binding table) would differ or fail.
func BuildInstanceViewForIdentity(
	ctx context.Context,
	provider PermissionProvider,
	identity *ZimaIdentity,
	app string,
	caps AgentCapabilities,
) (*InstanceView, error) {
	access, err := provider.FreshGrantsFor(ctx, identity, app)
	if err != nil {
		return nil, err
	}
	return BuildInstanceView(identity, access, caps)
}

// closeGrace delays releasing a replaced projection's descriptors so an
// in-flight lookup opening through an old root is not cut off mid-operation.
const closeGrace = 5 * time.Second

// reproject rebuilds this view's tree and grants from `next` — a freshly built
// projection for the same identity — so a change to the authority's grants
// takes effect without remounting. The FUSE nodes keep their pointer to this
// InstanceView; only the guarded projection changes. It is a no-op when the
// grant set is unchanged, so a device-wide generation bump that does not touch
// this mount costs only the comparison. Reports whether the view changed.
func (v *InstanceView) reproject(next *InstanceView) (bool, error) {
	if v == nil || next == nil {
		return false, nil
	}
	if v.sameGrants(next) {
		return false, nil
	}
	// Open the new roots before publishing them; on failure keep the old
	// projection so a transient error never breaks a live mount.
	if err := next.openRoots(); err != nil {
		return false, err
	}
	v.mu.Lock()
	oldGrants := v.grants
	v.root = next.root
	v.grants = next.grants
	v.Access = next.Access
	v.Generation = next.Generation
	v.mu.Unlock()

	// Old keys may now point at replaced entries; clear rather than reason about
	// per-key validity. Answers self-invalidate on generation regardless.
	v.decisions.Range(func(k, _ any) bool { v.decisions.Delete(k); return true })
	v.decisionCount.Store(0)

	// Release the old descriptors after a grace period. Already-open files hold
	// their own fds and are unaffected; only an in-flight openat could race.
	time.AfterFunc(closeGrace, func() { closeGrants(oldGrants) })
	return true, nil
}

// sameGrants reports whether two projections grant the same paths at the same
// access, so an unchanged mount can skip the rebuild.
func (v *InstanceView) sameGrants(next *InstanceView) bool {
	v.mu.RLock()
	sig := grantSignature(v.grants)
	v.mu.RUnlock()
	return sig == grantSignature(next.grants)
}

// grantSignature keys a projection on its grants' view paths and read-only
// verdicts. View path is stable (openRoots rewrites HostPath for file grants but
// never ViewPath), so it identifies a grant across a rebuild; read-only is
// included so a read↔write change also triggers a reproject.
func grantSignature(grants []*NamespaceEntry) string {
	parts := make([]string, 0, len(grants))
	for _, g := range grants {
		ro := "rw"
		if g.ReadOnly {
			ro = "ro"
		}
		parts = append(parts, g.ViewPath+"\x00"+ro)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x01")
}

// closeGrants releases the descriptors held by a set of grant entries.
func closeGrants(grants []*NamespaceEntry) {
	for _, entry := range grants {
		if entry.root != nil {
			_ = entry.root.Close()
			entry.root = nil
		}
	}
}
