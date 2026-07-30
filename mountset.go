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
	"os"
	"path/filepath"
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

	mu     sync.Mutex
	mounts map[string]*liveMount
}

type liveMount struct {
	agentUserID string
	handle      MountHandle
	bindSource  string
}

// MountInfo describes a live mount for the control API.
type MountInfo struct {
	AgentUserID string `json:"agent_user_id"`
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
	}, nil
}

// Mount creates a mount for an agent account, or returns the existing one.
func (s *MountSet) Mount(ctx context.Context, agentUserID string, caps *AgentCapabilities) (MountInfo, error) {
	if agentUserID == "" {
		return MountInfo{}, errors.New("toolfs: agent user id is required")
	}

	s.mu.Lock()
	if existing, ok := s.mounts[agentUserID]; ok {
		info := s.infoFor(existing)
		s.mu.Unlock()
		return info, ErrMountExists
	}
	s.mu.Unlock()

	// Resolve identity first: the mount path is derived from the numeric ZimaOS
	// user id, never from the agent-supplied string.
	identity, err := s.resolver.Resolve(ctx, agentUserID)
	if err != nil {
		return MountInfo{}, err
	}

	effective := s.config.DefaultCaps
	if caps != nil {
		effective = *caps
	}

	bindSource := filepath.Join(s.config.Root, identity.mountDirName())
	mountPoint := filepath.Join(bindSource, "workspace")
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		return MountInfo{}, fmt.Errorf("toolfs: cannot create mount point: %w", err)
	}

	handle, err := MountForIdentity(ctx, s.resolver, s.provider, agentUserID,
		mountPoint, effective, s.config.MountOptions)
	if err != nil {
		return MountInfo{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Another caller may have raced us; keep theirs and drop ours.
	if existing, ok := s.mounts[agentUserID]; ok {
		_ = handle.Unmount()
		return s.infoFor(existing), ErrMountExists
	}
	live := &liveMount{agentUserID: agentUserID, handle: handle, bindSource: bindSource}
	s.mounts[agentUserID] = live
	return s.infoFor(live), nil
}

// Unmount tears down one mount.
//
// The mount point directory is intentionally left in place: it is below the
// stable bind source, and recreating it on the next mount is cheaper than
// coordinating removal with a consumer that may still have it bound.
func (s *MountSet) Unmount(agentUserID string) error {
	s.mu.Lock()
	live, ok := s.mounts[agentUserID]
	if ok {
		delete(s.mounts, agentUserID)
	}
	s.mu.Unlock()

	if !ok {
		return ErrMountNotFound
	}
	return live.handle.Unmount()
}

// List returns every live mount, ordered for stable output.
func (s *MountSet) List() []MountInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]MountInfo, 0, len(s.mounts))
	for _, live := range s.mounts {
		out = append(out, s.infoFor(live))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentUserID < out[j].AgentUserID })
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
	s.mounts = make(map[string]*liveMount)
	s.mu.Unlock()

	var errs []error
	for _, m := range live {
		if err := m.handle.Unmount(); err != nil {
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
		BindSource:  live.bindSource,
		MountPoint:  live.handle.MountPoint(),
	}
	if identity != nil {
		info.Username = identity.Username
		info.UserID = identity.UserID
	}
	return info
}
