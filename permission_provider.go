package toolfs

// Authorization ingestion from IceWhale-Files.
//
// Interface choice matters here. The obvious endpoint,
// GET /v2_1/files/share/permission?username=X, does not filter is_deleted, so
// disabled shares come back as live permissions — a privilege *widening* bug.
// On the reference device 8 of 14 share rows carried is_deleted=1, so this is
// not hypothetical. ToolFS therefore filters explicitly on its own side and
// treats anything it cannot positively confirm as denied.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultGrantTTL bounds how stale an authorization snapshot may be.
// ZimaOS publishes no share-change event, so this is the revocation latency
// for reads. Destructive operations do not rely on it — see FreshGrantsFor.
const DefaultGrantTTL = 30 * time.Second

// ErrNoGrants indicates the user holds no usable authorization at all.
// A zero-grant user sees an empty namespace; this is not an error condition
// for admins either, since ToolFS grants admins no bypass.
var ErrNoGrants = errors.New("toolfs: no active grants for user")

// PermissionProvider supplies per-user authorization snapshots.
type PermissionProvider interface {
	// GrantsFor returns a snapshot, possibly served from cache.
	GrantsFor(ctx context.Context, username string) (*AccessSnapshot, error)
	// FreshGrantsFor bypasses the cache. Destructive operations must use this
	// so an irreversible action is never taken on stale authorization.
	FreshGrantsFor(ctx context.Context, username string) (*AccessSnapshot, error)
	// Invalidate drops any cached snapshot for the user.
	Invalidate(username string)
}

// shareRow mirrors one row of the IceWhale-Files share listing.
type shareRow struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Username string `json:"username"`
	ReadOnly bool   `json:"read_only"`
	// Deleted and Status are the two ways upstream marks a share inactive.
	// Both are honoured; anything not positively active is dropped.
	Deleted bool   `json:"is_deleted"`
	Status  string `json:"status"`
}

// active reports whether the row should produce a grant.
func (r shareRow) active() bool {
	if r.Deleted {
		return false
	}
	// An empty status means the endpoint does not report one; only an explicit
	// non-enable value disqualifies the row.
	return r.Status == "" || strings.EqualFold(r.Status, "enable")
}

// FilesServiceProvider reads authorization from the IceWhale-Files HTTP API.
type FilesServiceProvider struct {
	baseURL string
	client  *http.Client
	ttl     time.Duration

	mu    sync.Mutex
	cache map[string]*cachedSnapshot
	// generation increases on every successful refresh so that consumers can
	// invalidate derived state (namespace views, permission decision caches)
	// by comparing a single integer.
	generation uint64
}

type cachedSnapshot struct {
	snapshot *AccessSnapshot
	fetched  time.Time
}

// FilesRuntimeURLPath is where IceWhale-Files publishes its listen address.
const FilesRuntimeURLPath = "/run/casaos/icewhale-files.url"

// NewFilesServiceProvider builds a provider against the local Files service.
// baseURL may be empty, in which case the runtime file is consulted and
// http://127.0.0.1 is used as a last resort.
func NewFilesServiceProvider(baseURL string, ttl time.Duration) *FilesServiceProvider {
	if baseURL == "" {
		baseURL = discoverFilesBaseURL()
	}
	if ttl <= 0 {
		ttl = DefaultGrantTTL
	}
	return &FilesServiceProvider{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client:  &http.Client{Timeout: 10 * time.Second},
		ttl:     ttl,
		cache:   make(map[string]*cachedSnapshot),
	}
}

// discoverFilesBaseURL reads the runtime URL file, falling back to loopback.
func discoverFilesBaseURL() string {
	if data, err := os.ReadFile(FilesRuntimeURLPath); err == nil {
		if url := strings.TrimSpace(string(data)); url != "" {
			return url
		}
	}
	return "http://127.0.0.1"
}

// GrantsFor returns a cached snapshot when it is younger than the TTL.
func (p *FilesServiceProvider) GrantsFor(ctx context.Context, username string) (*AccessSnapshot, error) {
	p.mu.Lock()
	if entry, ok := p.cache[username]; ok && time.Since(entry.fetched) < p.ttl {
		snapshot := entry.snapshot
		p.mu.Unlock()
		return snapshot, nil
	}
	p.mu.Unlock()
	return p.FreshGrantsFor(ctx, username)
}

// FreshGrantsFor always performs a round trip.
func (p *FilesServiceProvider) FreshGrantsFor(ctx context.Context, username string) (*AccessSnapshot, error) {
	rows, err := p.fetchShares(ctx, username)
	if err != nil {
		// Fail closed. A provider error must never be read as "no restrictions".
		return nil, err
	}

	grants := make([]Grant, 0, len(rows))
	for _, row := range rows {
		if !row.active() || row.Path == "" {
			continue
		}
		if row.Username != "" && !strings.EqualFold(row.Username, username) {
			continue
		}
		grants = append(grants, Grant{
			ID:       row.ID,
			Name:     row.Name,
			Path:     row.Path,
			ReadOnly: row.ReadOnly,
		})
	}

	p.mu.Lock()
	p.generation++
	generation := p.generation
	snapshot := NewAccessSnapshot(username, 0, "", grants, generation)
	p.cache[username] = &cachedSnapshot{snapshot: snapshot, fetched: time.Now()}
	p.mu.Unlock()

	return snapshot, nil
}

// Invalidate drops the cached snapshot so the next read refetches.
func (p *FilesServiceProvider) Invalidate(username string) {
	p.mu.Lock()
	delete(p.cache, username)
	p.mu.Unlock()
}

// fetchShares calls the per-user grant endpoint.
//
// Z-Skip-Auth plus a loopback source address is the service-to-service channel
// IceWhale-Files exposes; ZimaOS-UserService uses the same convention.
func (p *FilesServiceProvider) fetchShares(ctx context.Context, username string) ([]shareRow, error) {
	endpoint := fmt.Sprintf("%s/v2_1/files/users/%s/grants", p.baseURL, username)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Z-Skip-Auth", "true")
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("toolfs: grant fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("toolfs: grant endpoint returned %s", resp.Status)
	}

	var payload struct {
		Data []shareRow `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("toolfs: malformed grant response: %w", err)
	}
	return payload.Data, nil
}

// StaticPermissionProvider serves a fixed grant set.
//
// This exists because the upstream per-row grant endpoint is not shipped yet
// (the current /share/permission endpoint is unsafe, see the file header).
// It keeps the interface stable so integration is a provider swap, and it is
// also what tests use.
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
func (p *StaticPermissionProvider) GrantsFor(ctx context.Context, username string) (*AccessSnapshot, error) {
	return p.FreshGrantsFor(ctx, username)
}

// FreshGrantsFor implements PermissionProvider.
func (p *StaticPermissionProvider) FreshGrantsFor(_ context.Context, username string) (*AccessSnapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Calls++
	p.gen++
	return NewAccessSnapshot(username, 0, "", p.grants[username], p.gen), nil
}

// Invalidate implements PermissionProvider. The static provider has no cache.
func (p *StaticPermissionProvider) Invalidate(string) {}
