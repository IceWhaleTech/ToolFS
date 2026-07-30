package toolfs

// Identity resolution between the agent's account system and ZimaOS.
//
// These are two independent account systems. The agent web has its own login
// and its requests carry no ZimaOS credential, so ToolFS cannot verify a
// ZimaOS token to learn who the caller is. Routing through the ZimaOS gateway
// does not help either — the gateway is a reverse proxy and each application
// implements its own authentication.
//
// An explicit binding must therefore exist. IdentityResolver isolates that
// choice behind one interface so the binding scheme (admin-maintained mapping
// table today, a lightweight ZimaOS authorization flow later) does not block
// implementation of everything else.

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// ErrIdentityNotBound means the agent account has no ZimaOS counterpart.
// It must be treated as "no access", never as "unrestricted".
var ErrIdentityNotBound = errors.New("toolfs: agent account is not bound to a ZimaOS user")

// ZimaIdentity is the ZimaOS-side authorization subject.
type ZimaIdentity struct {
	// Username is the samba_user value the share table keys on.
	Username string
	// UserID is the ZimaOS numeric user id, used for mount paths and audit.
	UserID int
	// Role is admin or user. It gates administrative operations only
	// (purge, snapshot, rollback) and never participates in path decisions.
	Role string
}

// IdentityResolver maps an agent-side account to a ZimaOS authorization subject.
type IdentityResolver interface {
	Resolve(ctx context.Context, agentUserID string) (*ZimaIdentity, error)
}

// StaticIdentityResolver resolves from an explicit mapping table.
//
// This is the short-term binding scheme: an administrator maintains the
// mapping. It needs no protocol design and works today, at the cost of manual
// upkeep. Swapping in a resolver backed by a ZimaOS authorization flow later
// changes nothing outside this file.
type StaticIdentityResolver struct {
	mu       sync.RWMutex
	bindings map[string]ZimaIdentity
	// fallback, when set, is used for every unbound agent account. It supports
	// the transition mode where all instances share one dedicated ZimaOS
	// account: full accidental-deletion protection, no per-user isolation yet.
	fallback *ZimaIdentity
}

// NewStaticIdentityResolver builds a resolver from an agent-account keyed map.
func NewStaticIdentityResolver(bindings map[string]ZimaIdentity) *StaticIdentityResolver {
	if bindings == nil {
		bindings = make(map[string]ZimaIdentity)
	}
	return &StaticIdentityResolver{bindings: bindings}
}

// Bind records or replaces one binding.
func (r *StaticIdentityResolver) Bind(agentUserID string, identity ZimaIdentity) {
	r.mu.Lock()
	r.bindings[agentUserID] = identity
	r.mu.Unlock()
}

// Unbind removes a binding, after which the account resolves to no access
// unless a fallback is configured.
func (r *StaticIdentityResolver) Unbind(agentUserID string) {
	r.mu.Lock()
	delete(r.bindings, agentUserID)
	r.mu.Unlock()
}

// SetFallback configures the shared dedicated account used during transition.
//
// Prefer a purpose-made ZimaOS account over admin: admin's share rows are
// written automatically for every storage root with read-write access, which
// amounts to no restriction at all.
func (r *StaticIdentityResolver) SetFallback(identity *ZimaIdentity) {
	r.mu.Lock()
	r.fallback = identity
	r.mu.Unlock()
}

// Resolve implements IdentityResolver.
func (r *StaticIdentityResolver) Resolve(_ context.Context, agentUserID string) (*ZimaIdentity, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if identity, ok := r.bindings[agentUserID]; ok {
		return &identity, nil
	}
	if r.fallback != nil {
		fallback := *r.fallback
		return &fallback, nil
	}
	return nil, ErrIdentityNotBound
}

// mountDirName returns the per-user directory component for a mount path.
//
// Deliberately built from the numeric user id rather than the username: the
// username is attacker-influenced data in the general case, and must never be
// interpolated into a filesystem path unchecked.
func (id *ZimaIdentity) mountDirName() string {
	if id == nil {
		return ""
	}
	return "u" + itoa(id.UserID)
}

// itoa avoids pulling strconv into this file for one call site.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	negative := v < 0
	if negative {
		v = -v
	}
	var digits [20]byte
	i := len(digits)
	for v > 0 {
		i--
		digits[i] = byte('0' + v%10)
		v /= 10
	}
	if negative {
		i--
		digits[i] = '-'
	}
	return string(digits[i:])
}

// IsAdmin reports whether the identity may perform administrative operations.
// This is the only place Role is consulted; file visibility never uses it.
func (id *ZimaIdentity) IsAdmin() bool {
	return id != nil && strings.EqualFold(id.Role, "admin")
}
