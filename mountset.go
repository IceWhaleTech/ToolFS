//go:build linux || darwin
// +build linux darwin

package toolfs

// MountSet owns every live per-user mount inside one daemon process.
//
// One process serves all mounts on purpose. The protection chain needs
// cross-mount mutual exclusion — version debouncing and per-file version counts
// have to be serialized when two users write the same shared directory — and an
// in-process mutex is the simplest thing that is actually correct. The cost is
// that a crash interrupts every mount at once; the daemon is expected to be
// restarted by its supervisor, and consumers must tolerate ENOTCONN and retry.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"sync"
)

// ErrMountExists reports a second mount request for an already-mounted user.
var ErrMountExists = errors.New("toolfs: user already has a live mount")

// ErrMountNotFound reports an operation against an unknown mount.
var ErrMountNotFound = errors.New("toolfs: no such mount")

// MountSetConfig configures the daemon's mount management.
type MountSetConfig struct {
	// Root is the directory under which per-user mount points are created.
	// Layout is <Root>/<u{uid}>/workspace: the per-user directory is a plain,
	// stable directory that is never unmounted, and the FUSE view lives one
	// level below it. That separation is what lets the daemon remount without
	// a consumer silently continuing to read the previous view.
	Root string
	// DefaultCaps applies to mounts that do not specify their own.
	DefaultCaps AgentCapabilities
	// MountOptions tunes the underlying FUSE mounts.
	MountOptions MountConfig
}

// MountSet tracks live mounts by agent account.
type MountSet struct {
	config   MountSetConfig
	resolver IdentityResolver
	provider PermissionProvider

	lifecycleMu sync.Mutex
	mu          sync.Mutex
	mounts      map[string]*liveMount
	mount       func(context.Context, IdentityResolver, PermissionProvider, string, string, string, AgentCapabilities, MountConfig) (MountHandle, error)
}

type liveMount struct {
	agentUserID string
	app         string
	handle      MountHandle
	bindSource  string
	unmounted   bool
}

// mountKey composes the map key for a live mount. Mounts are keyed per
// (agentUserID, app) so each app's agent instance gets its own view; the NUL
// separator cannot appear in either component, so the key is unambiguous.
func mountKey(agentUserID, app string) string { return agentUserID + "\x00" + app }

// MountInfo describes a live mount for the control API.
type MountInfo struct {
	AgentUserID string `json:"agent_user_id"`
	App         string `json:"app,omitempty"`
	Username    string `json:"username"`
	UserID      int    `json:"user_id"`
	// BindSource is the stable directory an orchestrator should bind into a
	// container. It is the parent of MountPoint and is never unmounted.
	BindSource string `json:"bind_source"`
	// MountPoint is where the FUSE view is actually served.
	MountPoint string `json:"mount_point"`
}

// NewMountSet builds a MountSet.
func NewMountSet(config MountSetConfig, resolver IdentityResolver, provider PermissionProvider) (*MountSet, error) {
	if config.Root == "" {
		return nil, errors.New("toolfs: mount root is required")
	}
	if resolver == nil || provider == nil {
		return nil, errors.New("toolfs: resolver and provider are required")
	}
	if err := os.MkdirAll(config.Root, 0o755); err != nil {
		return nil, fmt.Errorf("toolfs: cannot create mount root: %w", err)
	}
	return &MountSet{
		config:   config,
		resolver: resolver,
		provider: provider,
		mounts:   make(map[string]*liveMount),
		mount:    MountForIdentity,
	}, nil
}

// Mount creates a mount for an (agent account, app) pair, or returns the
// existing one. Each (agentUserID, app) gets its own mount and its own view.
func (s *MountSet) Mount(ctx context.Context, agentUserID, app string, identity *ZimaIdentity, caps *AgentCapabilities) (MountInfo, error) {
	if agentUserID == "" {
		return MountInfo{}, errors.New("toolfs: agent user id is required")
	}

	// Directory preparation and rollback share parents across apps. Serialize
	// management operations without holding the ledger lock over authority IO.
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if err := ctx.Err(); err != nil {
		return MountInfo{}, err
	}
	key := mountKey(agentUserID, app)

	s.mu.Lock()
	if existing, ok := s.mounts[key]; ok {
		info := s.infoFor(existing)
		s.mu.Unlock()
		return info, ErrMountExists
	}
	s.mu.Unlock()

	// Identity: prefer the one the caller supplied from the verified token —
	// the single source of truth — and resolve only when none is given. The
	// mount path is derived from the numeric ZimaOS user id, never from the
	// agent-supplied string. A supplied identity is wrapped in a one-shot
	// resolver so the view build below sees exactly the same subject.
	resolver := s.resolver
	resolved := identity
	if resolved == nil {
		var err error
		resolved, err = s.resolver.Resolve(ctx, agentUserID)
		if err != nil {
			return MountInfo{}, err
		}
	} else {
		resolver = NewStaticIdentityResolver(map[string]ZimaIdentity{agentUserID: *identity})
	}

	effective := s.config.DefaultCaps
	if caps != nil {
		effective = *caps
	}

	// The bind source differs per (user, app) so each app gets its own mount
	// point: blue and photos never share a bind source or a FUSE view.
	layout, err := mountLayoutFor(s.config.Root, resolved, app)
	if err != nil {
		return MountInfo{}, err
	}
	if err := s.checkMountLayout(layout); err != nil {
		return MountInfo{}, err
	}
	prepared, err := layout.prepare()
	if err != nil {
		return MountInfo{}, errors.Join(err, prepared.rollback())
	}

	handle, err := s.mount(ctx, resolver, s.provider, agentUserID, app,
		layout.point, effective, s.config.MountOptions)
	if err != nil {
		return MountInfo{}, errors.Join(err, prepared.rollback())
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	live := &liveMount{agentUserID: agentUserID, app: app, handle: handle, bindSource: layout.bind}
	s.mounts[key] = live
	return s.infoFor(live), nil
}

// Reproject rebuilds every live mount's view from the authority's current
// grants. Wired to the permission provider's change hook, so a grant added or
// revoked — including a share to this user that a different account changed —
// takes effect on the running mount without a remount. Each view is rebuilt
// only if its grant set actually changed, so an unrelated device-wide change
// costs one comparison per mount.
func (s *MountSet) Reproject(ctx context.Context) {
	s.mu.Lock()
	live := make([]*liveMount, 0, len(s.mounts))
	for _, m := range s.mounts {
		if !m.unmounted {
			live = append(live, m)
		}
	}
	s.mu.Unlock()

	for _, m := range live {
		view := m.handle.View()
		if view == nil {
			continue
		}
		fresh, err := BuildInstanceViewForIdentity(ctx, s.provider, view.Identity, m.app, view.Caps)
		if err != nil {
			log.Printf("toolfs: reproject %s/%s: build failed: %v", m.agentUserID, m.app, err)
			continue
		}
		// A release may have completed while the authority read was in flight.
		// Never reopen roots for a mount that has already been torn down.
		s.mu.Lock()
		if s.mounts[mountKey(m.agentUserID, m.app)] == m && !m.unmounted {
			if _, err := view.reproject(fresh); err != nil {
				log.Printf("toolfs: reproject %s/%s: swap failed: %v", m.agentUserID, m.app, err)
			}
		}
		s.mu.Unlock()
	}
}

// Unmount tears down one mount and removes its empty runtime directories. A
// failure stays in the ledger so a later release can finish the operation.
func (s *MountSet) Unmount(agentUserID, app string) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	key := mountKey(agentUserID, app)
	s.mu.Lock()
	defer s.mu.Unlock()
	live, ok := s.mounts[key]
	if !ok {
		return ErrMountNotFound
	}
	if !live.unmounted {
		if err := live.handle.Unmount(); err != nil {
			return err
		}
		live.unmounted = true
	}
	if err := s.removeMountDirectories(live); err != nil {
		return fmt.Errorf("toolfs: clean unmounted instance: %w", err)
	}
	delete(s.mounts, key)
	return nil
}

// List returns every live mount, ordered for stable output.
func (s *MountSet) List() []MountInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]MountInfo, 0, len(s.mounts))
	for _, live := range s.mounts {
		out = append(out, s.infoFor(live))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AgentUserID != out[j].AgentUserID {
			return out[i].AgentUserID < out[j].AgentUserID
		}
		return out[i].App < out[j].App
	})
	return out
}

// Close tears down every mount. Errors are collected rather than short-circuited
// so that one stuck mount cannot strand the rest.
func (s *MountSet) Close() error {
	s.mu.Lock()
	live := make([]*liveMount, 0, len(s.mounts))
	for _, m := range s.mounts {
		live = append(live, m)
	}
	s.mu.Unlock()

	var errs []error
	for _, m := range live {
		if err := s.Unmount(m.agentUserID, m.app); err != nil && !errors.Is(err, ErrMountNotFound) {
			errs = append(errs, fmt.Errorf("%s: %w", m.agentUserID, err))
		}
	}
	return errors.Join(errs...)
}

// infoFor renders a MountInfo. Callers must hold s.mu.
func (s *MountSet) infoFor(live *liveMount) MountInfo {
	identity := live.handle.Identity()
	info := MountInfo{
		AgentUserID: live.agentUserID,
		App:         live.app,
		BindSource:  live.bindSource,
		MountPoint:  live.handle.MountPoint(),
	}
	if identity != nil {
		info.Username = identity.Username
		info.UserID = identity.UserID
	}
	return info
}
