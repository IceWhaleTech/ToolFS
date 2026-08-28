package toolfs

// Authorization ingestion.
//
// The single source of truth is the ZimaOS-UserService permission authority,
// reached through the shared ZimaOS-Common permission client: a cached
// snapshot per subject, kept honest by the authority's generation counter and
// its event stream. ToolFS consumes that state and only ever *narrows* it,
// never widens.
//
// History: this used to read the IceWhale-Files share table over an interim
// HTTP endpoint, with a TTL standing in for revocation. Both problems that
// design carried — a widening bug in the share listing, and revocation
// latency bounded only by the TTL — are what the authority exists to remove:
// the snapshot is the authority's own projection, and a change event arrives
// the moment a grant moves.

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/IceWhaleTech/ZimaOS-Common/permission"
)

// ErrNoGrants indicates the user holds no usable authorization at all.
// A zero-grant user sees an empty namespace; this is not an error condition
// for admins either, since ToolFS grants admins no bypass.
var ErrNoGrants = errors.New("toolfs: no active grants for user")

// PermissionProvider supplies per-user authorization snapshots.
type PermissionProvider interface {
	// GrantsFor returns a snapshot for the resolved identity, possibly served
	// from cache. When app is non-empty and the (user, app) agent is scoped,
	// the snapshot is the agent subject's, already capped by the owner user.
	GrantsFor(ctx context.Context, identity *ZimaIdentity, app string) (*AccessSnapshot, error)
	// FreshGrantsFor is GrantsFor with the strongest freshness the provider
	// can offer. Destructive operations use this so an irreversible action is
	// never taken on authorization staler than it has to be.
	FreshGrantsFor(ctx context.Context, identity *ZimaIdentity, app string) (*AccessSnapshot, error)
	// Invalidate drops any provider-side cache for the user.
	Invalidate(username string)
}

// AuthorityPermissionProvider reads authorization from ZimaOS-UserService.
//
// Decisions are live: the AccessSnapshots it hands out delegate every per-path
// answer to the client's cached authority snapshot, so a revocation reaches an
// already-mounted view the moment the change event lands — no remount, no TTL.
// What is fixed at GrantsFor time is only the namespace projection: which
// grant roots exist as top-level directories. A root granted after the mount
// appears on the next remount; a root revoked merely turns dark immediately
// (every operation under it answers ENOENT).
type AuthorityPermissionProvider struct {
	client *permission.Client

	mu sync.Mutex
	// served maps subject → identity for every snapshot handed out, so a
	// change event can re-warm exactly the subjects that have live mounts.
	served map[string]*ZimaIdentity

	// onChange, when set, runs after a change event has been absorbed. The
	// daemon uses it to notice that a remount may be due.
	onChange func(generation int64)
}

// NewAuthorityPermissionProvider wraps an already-configured client.
//
// The caller owns the client's lifecycle (Load, Watch). Most callers want
// StartAuthorityPermissionProvider instead.
func NewAuthorityPermissionProvider(client *permission.Client) *AuthorityPermissionProvider {
	return &AuthorityPermissionProvider{
		client: client,
		served: make(map[string]*ZimaIdentity),
	}
}

// StartAuthorityPermissionProvider builds the client, loads the disk cache and
// starts following the authority's event stream.
//
// runtimePath is where the authority publishes its address (the same directory
// every ZimaOS service consults). cachePath persists snapshots so a restart
// answers from what it last knew instead of refusing everyone until the
// authority is up. The subscription is narrowed to the path namespace: every
// question this package asks is about paths, and the option's contract is that
// reads stay inside the subscribed namespaces.
func StartAuthorityPermissionProvider(ctx context.Context, runtimePath, cachePath string) *AuthorityPermissionProvider {
	provider := &AuthorityPermissionProvider{served: make(map[string]*ZimaIdentity)}

	options := []permission.Option{
		permission.WithWatchNamespaces(permission.NamespacePath),
		permission.WithChangeHandler(provider.handleChange),
	}
	if cachePath != "" {
		options = append(options, permission.WithCache(cachePath))
	}
	provider.client = permission.New(permission.AddressIn(runtimePath), options...)

	if err := provider.client.Load(); err != nil {
		// A cache that will not load is not fatal: the first fetch replaces it.
		// Worth a line, because until then this provider refuses everyone.
		log.Printf("toolfs: permission cache load failed, refusing until the first fetch: %v", err)
	}
	go provider.client.Watch(ctx)

	return provider
}

// OnChange registers a hook run after each absorbed change event.
func (p *AuthorityPermissionProvider) OnChange(hook func(generation int64)) {
	p.mu.Lock()
	p.onChange = hook
	p.mu.Unlock()
}

// handleChange re-warms every served subject.
//
// The event has already emptied the client's cache; until a subject's snapshot
// is fetched again, its decisions answer "unknown" and the decider fails
// closed. Re-warming immediately keeps that window at one round trip, and in
// the fail-closed direction — a revoked grant is dark from the event on, never
// the other way around.
func (p *AuthorityPermissionProvider) handleChange(generation int64) {
	p.mu.Lock()
	subjects := make([]string, 0, len(p.served))
	for subject := range p.served {
		subjects = append(subjects, subject)
	}
	hook := p.onChange
	p.mu.Unlock()

	for _, subject := range subjects {
		if _, err := p.client.Snapshot(context.Background(), subject); err != nil {
			log.Printf("toolfs: re-warming %s after generation %d failed: %v", subject, generation, err)
		}
	}
	if hook != nil {
		hook(generation)
	}
}

// homeRootPath is where UserService creates account homes; must match its
// HomeRootPath. The one spelling the authority writes home rows under.
const homeRootPath = "/DATA/.home"

// homeEntryName is the fixed virtual name of the account's own home entry.
const homeEntryName = "Home"

// GrantsFor projects the authority's snapshot for one identity.
//
// When app is non-empty the agent sub-scope is consulted: the agent subject is
// "agent:<uid>:<app>". The agent is "scoped" iff it holds any path-read grant
// of its own. Because the engine already intersects an owned agent with its
// owner user, a scoped agent snapshot is guaranteed <= the user's, so the agent
// subject can be projected directly. An UNSCOPED agent (no grants of its own)
// falls back to the full user view for backward compatibility. A Snapshot error
// on the agent subject fails closed — it never falls through to the user view.
func (p *AuthorityPermissionProvider) GrantsFor(ctx context.Context, identity *ZimaIdentity, app string) (*AccessSnapshot, error) {
	if identity == nil {
		return nil, ErrNoIdentity
	}
	userSubject := permission.NamespaceUser + ":" + itoa(identity.UserID)
	subject := userSubject

	snapshot, err := p.client.Snapshot(ctx, subject)
	if err != nil {
		// Fail closed. A provider error must never be read as "no restrictions".
		return nil, err
	}

	if app != "" {
		agentSubject := permission.NamespaceAgent + ":" + itoa(identity.UserID) + ":" + app
		agentSnap, err := p.client.Snapshot(ctx, agentSubject)
		if err != nil {
			// Fail closed, never fall through to the full user view on error.
			return nil, err
		}
		// A scoped agent has at least one path-read grant of its own; project
		// the agent subject in that case. An unscoped agent keeps the user view.
		if len(agentSnap.Objects(permission.NamespacePath, permission.RelRead)) > 0 {
			subject = agentSubject
			snapshot = agentSnap
		}
	}

	p.mu.Lock()
	p.served[subject] = identity
	p.mu.Unlock()

	// The namespace projection: every root the subject may read becomes a
	// top-level entry. The paths keep the authority's spelling — they are both
	// the key later decisions are asked under and the host path the root
	// handle opens — so none of the alias folding the static path applies here.
	roots := snapshot.Objects(permission.NamespacePath, permission.RelRead)
	grants := make([]Grant, 0, len(roots))
	homePath := homeRootPath + "/" + identity.Username
	for i, root := range roots {
		grant := Grant{
			// The authority has no share-row id; the index keeps entry naming
			// deterministic for one snapshot, which is all the id is for.
			ID:       i + 1,
			Path:     root,
			ReadOnly: !snapshot.Allows(permission.NamespacePath, root, permission.RelWrite),
		}
		if root == homePath {
			// The account's own home gets a fixed name and first position:
			// dispatchers on the other side of the mount translate host home
			// paths into the view, and that only works when the entry name
			// cannot drift with grant order or basename collisions. A share
			// that happens to be called Home is the one that gets suffixed.
			grant.Name = homeEntryName
			grants = append([]Grant{grant}, grants...)
			continue
		}
		grants = append(grants, grant)
	}

	return &AccessSnapshot{
		Username:   identity.Username,
		UserID:     identity.UserID,
		Role:       identity.Role,
		Grants:     grants,
		Generation: uint64(snapshot.Generation),
		authority:  &authorityDecider{client: p.client, subject: subject},
	}, nil
}

// FreshGrantsFor is GrantsFor: freshness is event-driven here, so the cached
// answer is already as fresh as a forced round trip would be — the cache is
// emptied the moment the authority announces a change.
func (p *AuthorityPermissionProvider) FreshGrantsFor(ctx context.Context, identity *ZimaIdentity, app string) (*AccessSnapshot, error) {
	return p.GrantsFor(ctx, identity, app)
}

// Invalidate implements PermissionProvider. The client's cache is governed by
// the authority's generation, not by per-user hints; there is nothing local to
// drop.
func (p *AuthorityPermissionProvider) Invalidate(string) {}

// authorityDecider answers per-path questions from the live client cache.
type authorityDecider struct {
	client  *permission.Client
	subject string
}

// generation exposes the client's live generation for decision-memo keying.
func (d *authorityDecider) generation() int64 {
	return d.client.Generation()
}

// allowedOps maps the authority's relations onto ToolFS operations.
//
//	read            → OpRead | OpList
//	write           → OpCreate | OpModify
//	delete          → OpDelete, and only when the capability releases it
//
// Narrowing rules carried over from the static path: delete is withheld unless
// AgentCapabilities.AllowDelete is set, and never released where write is not
// held — a read-only grant can never be promoted to deletable.
//
// An unknown answer — the client holds no snapshot for this subject, typically
// the one-round-trip window between a change event and the re-warm — is a
// refusal. Fail closed: the alternative reads a cache miss as "no
// restrictions".
func (d *authorityDecider) allowedOps(hostPath string, caps AgentCapabilities) AccessOp {
	read, known := d.client.Cached(d.subject, permission.NamespacePath, hostPath, permission.RelRead)
	if !known || !read {
		return 0
	}
	ops := OpRead | OpList

	if write, _ := d.client.Cached(d.subject, permission.NamespacePath, hostPath, permission.RelWrite); write {
		ops |= OpCreate | OpModify
		if caps.AllowDelete {
			if del, _ := d.client.Cached(d.subject, permission.NamespacePath, hostPath, permission.RelDelete); del {
				ops |= OpDelete
			}
		}
	}
	return ops
}

// StaticPermissionProvider serves a fixed grant set. It is what tests use, and
// it keeps the provider seam honest: integration stays a provider swap.
type StaticPermissionProvider struct {
	mu     sync.Mutex
	grants map[string][]Grant
	gen    uint64
	// Calls counts fetches, letting tests assert cache behaviour.
	Calls int
}

// NewStaticPermissionProvider builds a provider from a username to grant map.
func NewStaticPermissionProvider(grants map[string][]Grant) *StaticPermissionProvider {
	if grants == nil {
		grants = make(map[string][]Grant)
	}
	return &StaticPermissionProvider{grants: grants}
}

// SetGrants replaces one user's grants, simulating an authorization change.
func (p *StaticPermissionProvider) SetGrants(username string, grants []Grant) {
	p.mu.Lock()
	p.grants[username] = grants
	p.mu.Unlock()
}

// GrantsFor implements PermissionProvider.
func (p *StaticPermissionProvider) GrantsFor(ctx context.Context, identity *ZimaIdentity, app string) (*AccessSnapshot, error) {
	return p.FreshGrantsFor(ctx, identity, app)
}

// FreshGrantsFor implements PermissionProvider.
func (p *StaticPermissionProvider) FreshGrantsFor(_ context.Context, identity *ZimaIdentity, _ string) (*AccessSnapshot, error) {
	if identity == nil {
		return nil, ErrNoIdentity
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Calls++
	p.gen++
	return NewAccessSnapshot(identity.Username, identity.UserID, identity.Role, p.grants[identity.Username], p.gen), nil
}

// Invalidate implements PermissionProvider. The static provider has no cache.
func (p *StaticPermissionProvider) Invalidate(string) {}
