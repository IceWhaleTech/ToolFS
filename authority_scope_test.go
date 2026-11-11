package toolfs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IceWhaleTech/ZimaOS-Common/permission"
)

type agentScopeEvent struct {
	generation int64
	namespace  string
}

type agentScopeAuthority struct {
	mu             sync.Mutex
	generation     int64
	root           string
	owner          bool
	grants         map[string][]string
	denied         map[string][]string
	ownerGrants    map[string][]string
	snapshotStatus int
	entriesStatus  int
	entriesSubject string
	entriesDrift   bool
	requests       []string
	events         chan agentScopeEvent
}

func (a *agentScopeAuthority) snapshot(subject string) *permission.Snapshot {
	ns, id, _ := strings.Cut(subject, ":")
	result := &permission.Snapshot{Subject: permission.Subject{Namespace: ns, ID: id}, Generation: a.generation}
	if a.root != "/media/Docs" {
		result.PathAliases = []permission.PathAlias{{Canonical: "/media/Docs", Alias: a.root}}
	}
	path := permission.NamespaceSnapshot{}
	owner := &permission.Snapshot{Namespaces: map[string]permission.NamespaceSnapshot{"path": {}}}
	for object, relations := range a.ownerGrants {
		entry := owner.Namespaces["path"]
		entry.Allow = append(entry.Allow, permission.Grant{Object: object, Relations: relations})
		owner.Namespaces["path"] = entry
	}
	if subject == "user:7" {
		path = owner.Namespaces["path"]
	} else if subject == "agent:7:photos" && a.owner {
		for object, relations := range a.grants {
			var effective []string
			for _, relation := range relations {
				if owner.Allows("path", object, relation) {
					effective = append(effective, relation)
				}
			}
			if len(effective) != 0 {
				path.Allow = append(path.Allow, permission.Grant{Object: object, Relations: effective})
			}
		}
		for object, relations := range a.denied {
			path.Deny = append(path.Deny, permission.Grant{Object: object, Relations: relations})
		}
	}
	result.Namespaces = map[string]permission.NamespaceSnapshot{"path": path}
	return result
}

func newAgentScopeProvider(t *testing.T) (*AuthorityPermissionProvider, *agentScopeAuthority) {
	t.Helper()
	a := &agentScopeAuthority{
		generation: 1, root: "/media/Docs", grants: map[string][]string{}, denied: map[string][]string{},
		ownerGrants: map[string][]string{"/media/Docs": {"read", "write", "delete"}},
		events:      make(chan agentScopeEvent, 16),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/permissions/snapshot", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		subject := r.URL.Query().Get("subject")
		a.requests = append(a.requests, "snapshot:"+subject)
		if a.snapshotStatus != 0 && subject != "user:7" {
			http.Error(w, "unavailable", a.snapshotStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": a.snapshot(subject)})
	})
	mux.HandleFunc("/v2/permissions/entries", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		subject := r.URL.Query().Get("subject")
		a.requests = append(a.requests, "entries:"+subject)
		if a.entriesStatus != 0 {
			http.Error(w, "unavailable", a.entriesStatus)
			return
		}
		snapshot := a.snapshot(subject)
		allow := map[string][]string{}
		for _, grant := range snapshot.Namespaces["path"].Allow {
			if snapshot.Allows("path", grant.Object, "read") {
				allow[grant.Object] = grant.Relations
			}
		}
		if a.entriesSubject != "" {
			snapshot = a.snapshot(a.entriesSubject)
		}
		generation := a.generation
		if a.entriesDrift {
			generation++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": PermissionEntries{
			Subject: snapshot.Subject, Generation: generation, Entries: fixturePermissionEntries(allow, nil),
		}})
	})
	mux.HandleFunc("/v2/permissions/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		a.mu.Lock()
		fmt.Fprintf(w, "data: {\"generation\":%d}\n\n", a.generation)
		a.mu.Unlock()
		w.(http.Flusher).Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case event := <-a.events:
				fmt.Fprintf(w, "data: {\"generation\":%d,\"namespaces\":[%q]}\n\n", event.generation, event.namespace)
				w.(http.Flusher).Flush()
			}
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	var p *AuthorityPermissionProvider
	client := permission.New(func() string { return server.URL }, permission.WithWatchNamespaces(permission.NamespacePath, permission.NamespaceAgent), permission.WithChangeHandler(func(g int64) { p.handleChange(g) }))
	p = NewAuthorityPermissionProvider(client, newEntriesReader(func() string { return server.URL }))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); client.Watch(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(5 * time.Second)
	for client.Generation() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if client.Generation() == 0 {
		t.Fatal("authority watch did not connect")
	}
	return p, a
}

func scopeAccess(t *testing.T, p *AuthorityPermissionProvider, app string) *AccessSnapshot {
	t.Helper()
	access, err := p.GrantsFor(t.Context(), &ZimaIdentity{Username: "alice", UserID: 7}, app)
	if err != nil {
		t.Fatal(err)
	}
	return access
}

func (a *agentScopeAuthority) change(t *testing.T, p *AuthorityPermissionProvider, namespace string, mutate func()) {
	t.Helper()
	a.mu.Lock()
	mutate()
	a.generation++
	generation := a.generation
	a.mu.Unlock()
	a.events <- agentScopeEvent{generation: generation, namespace: namespace}
	deadline := time.Now().Add(5 * time.Second)
	for p.client.Generation() < generation && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if p.client.Generation() < generation {
		t.Fatal("scope change did not reach the client")
	}
}

func TestAgentDefaultsToNoDirectoryAccess(t *testing.T) {
	p, a := newAgentScopeProvider(t)
	for _, app := range []string{"photos", "blue", "other"} {
		access := scopeAccess(t, p, app)
		if len(access.Grants) != 0 || len(access.entries) != 0 || access.Allows("/media/Docs/file", OpRead, AgentCapabilities{}) {
			t.Fatalf("undeclared app %s acquired user access", app)
		}
	}
	a.mu.Lock()
	for _, request := range a.requests {
		if request == "snapshot:user:7" || request == "entries:user:7" {
			t.Errorf("app authorization fetched the user view: %s", request)
		}
	}
	a.mu.Unlock()
	if !scopeAccess(t, p, "").Allows("/media/Docs/file", OpRead, AgentCapabilities{}) {
		t.Fatal("direct user view lost its own permission")
	}
}

func TestAgentAuthorizationLifecycle(t *testing.T) {
	p, a := newAgentScopeProvider(t)
	assertPaths := func(wantA, wantB bool) *AccessSnapshot {
		t.Helper()
		access := scopeAccess(t, p, "photos")
		for path, want := range map[string]bool{"/media/Docs/A/file": wantA, "/media/Docs/B/file": wantB, "/media/Docs/other/file": false} {
			if got := access.Allows(path, OpRead, AgentCapabilities{}); got != want {
				t.Errorf("%s read = %v, want %v", path, got, want)
			}
		}
		if !wantA && !wantB && (len(access.Grants) != 0 || len(access.entries) != 0) {
			t.Fatal("empty agent projected data entries")
		}
		return access
	}
	assertPaths(false, false)
	a.change(t, p, "agent", func() { a.owner = true })
	assertPaths(false, false)
	a.change(t, p, "path", func() { a.grants["/media/Docs/A"] = []string{"read"} })
	old := assertPaths(true, false)
	a.change(t, p, "path", func() { a.grants["/media/Docs/B"] = []string{"read"} })
	assertPaths(true, true)
	a.change(t, p, "path", func() { delete(a.grants, "/media/Docs/A") })
	assertPaths(false, true)
	if old.Allows("/media/Docs/A/file", OpRead, AgentCapabilities{}) {
		t.Fatal("old access snapshot retained revoked authorization")
	}
	a.change(t, p, "path", func() { delete(a.grants, "/media/Docs/B") })
	assertPaths(false, false)
	a.change(t, p, "agent", func() { a.owner = false })
	assertPaths(false, false)
	// Fresh provider and first projection must not promote a fully erased agent.
	restarted := NewAuthorityPermissionProvider(p.client, p.entries)
	if access := scopeAccess(t, restarted, "photos"); len(access.Grants) != 0 {
		t.Fatal("provider restart restored the user view")
	}
}

func TestAgentOwnerIsOnlyACeiling(t *testing.T) {
	p, a := newAgentScopeProvider(t)
	a.change(t, p, "path", func() { a.grants["/media/Docs/A"] = []string{"read", "write"} })
	if access := scopeAccess(t, p, "photos"); len(access.Grants) != 0 {
		t.Fatal("unowned agent received path permissions")
	}
	a.change(t, p, "agent", func() { a.owner = true })
	access := scopeAccess(t, p, "photos")
	if !access.Allows("/media/Docs/A/file", OpModify, AgentCapabilities{}) {
		t.Fatal("explicit grant within owner ceiling was lost")
	}
	a.change(t, p, "path", func() { a.ownerGrants = map[string][]string{} })
	if scopeAccess(t, p, "photos").Allows("/media/Docs/A/file", OpRead, AgentCapabilities{}) {
		t.Fatal("agent exceeded its owner's current permissions")
	}
	a.change(t, p, "path", func() { a.ownerGrants["/media/Docs"] = []string{"read"} })
	if !scopeAccess(t, p, "photos").Allows("/media/Docs/A/file", OpRead, AgentCapabilities{}) {
		t.Fatal("still-explicit read was not restored with its owner ceiling")
	}
	if scopeAccess(t, p, "photos").Allows("/media/Docs/A/file", OpModify, AgentCapabilities{}) {
		t.Fatal("read-only owner conferred agent write access")
	}
	a.change(t, p, "agent", func() { a.owner = false })
	if scopeAccess(t, p, "photos").Allows("/media/Docs/A/file", OpRead, AgentCapabilities{}) {
		t.Fatal("owner removal revived user permissions")
	}
}

func TestAgentAuthorityFailuresNeverSelectUser(t *testing.T) {
	for _, failure := range []string{"snapshot", "entries", "subject", "generation"} {
		t.Run(failure, func(t *testing.T) {
			p, a := newAgentScopeProvider(t)
			a.change(t, p, "agent", func() {
				switch failure {
				case "snapshot":
					a.snapshotStatus = http.StatusServiceUnavailable
				case "entries":
					a.entriesStatus = http.StatusServiceUnavailable
				case "subject":
					a.entriesSubject = "user:7"
				case "generation":
					a.entriesDrift = true
				}
			})
			if _, err := p.GrantsFor(t.Context(), &ZimaIdentity{Username: "alice", UserID: 7}, "photos"); err == nil {
				t.Fatal("invalid agent authority response produced an access snapshot")
			}
		})
	}
}

func TestAgentRevocationFailsClosedWhenRewarmingFails(t *testing.T) {
	p, a := newAgentScopeProvider(t)
	a.change(t, p, "agent", func() {
		a.owner = true
		a.grants["/media/Docs/A"] = []string{"read"}
	})
	access := scopeAccess(t, p, "photos")
	a.change(t, p, "path", func() {
		delete(a.grants, "/media/Docs/A")
		a.snapshotStatus = http.StatusServiceUnavailable
	})
	if access.Allows("/media/Docs/A/file", OpRead, AgentCapabilities{}) {
		t.Fatal("failed re-warm retained a revoked grant")
	}
}

func TestAgentSubjectsDoNotShareUserOrOtherAppPermissions(t *testing.T) {
	p, a := newAgentScopeProvider(t)
	a.change(t, p, "agent", func() {
		a.owner = true
		a.grants["/media/Docs/A"] = []string{"read"}
	})
	photos := scopeAccess(t, p, "photos")
	if !photos.Allows("/media/Docs/A/file", OpRead, AgentCapabilities{}) {
		t.Fatal("explicitly authorized agent lost its read grant")
	}
	for _, tc := range []struct {
		userID int
		app    string
	}{{7, "blue"}, {8, "photos"}} {
		access, err := p.GrantsFor(t.Context(), &ZimaIdentity{UserID: tc.userID, Username: "other"}, tc.app)
		if err != nil {
			t.Fatal(err)
		}
		if len(access.Grants) != 0 || len(access.entries) != 0 || access.Allows("/media/Docs/A/file", OpRead, AgentCapabilities{}) {
			t.Fatalf("user %d app %s acquired another agent's permissions", tc.userID, tc.app)
		}
	}
}

func TestAgentWithoutEffectiveReadHasNoDirectoryEntries(t *testing.T) {
	for _, relation := range []string{"read-deny", "write", "delete", "smb", "owner"} {
		t.Run(relation, func(t *testing.T) {
			p, a := newAgentScopeProvider(t)
			a.change(t, p, "agent", func() {
				a.owner = true
				if relation == "read-deny" {
					a.denied["/media/Docs/A"] = []string{"read", "write", "delete"}
				} else {
					a.grants["/media/Docs/A"] = []string{relation}
				}
			})
			access := scopeAccess(t, p, "photos")
			if len(access.Grants) != 0 || len(access.entries) != 0 || access.Allows("/media/Docs/private/file", OpRead, AgentCapabilities{}) {
				t.Fatal("non-readable agent acquired user directory entries")
			}
		})
	}
}
