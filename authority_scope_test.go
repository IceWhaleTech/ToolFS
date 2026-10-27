package toolfs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	mu            sync.Mutex
	generation    int64
	root          string
	owner         bool
	pathDeclared  bool
	pathRelation  string
	tuplesDrift   bool
	read          bool
	complete      bool
	tuplesError   bool
	tupleRequests int
	events        chan agentScopeEvent
}

func newAgentScopeProvider(t *testing.T) (*AuthorityPermissionProvider, *agentScopeAuthority) {
	t.Helper()
	a := &agentScopeAuthority{generation: 1, root: "/media/Docs", owner: true, pathDeclared: true, pathRelation: "read", read: true, complete: true, events: make(chan agentScopeEvent, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/permissions/snapshot", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		subject := r.URL.Query().Get("subject")
		ns, id := "user", "7"
		roots := []permission.Grant{{Object: a.root, Relations: []string{"read", "write"}}}
		if subject != "user:7" {
			ns, id = "agent", "7:photos"
			roots = nil
			if subject == "agent:7:photos" && a.owner && a.read {
				roots = []permission.Grant{{Object: a.root + "/allowed", Relations: []string{"read"}}}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": permission.Snapshot{Subject: permission.Subject{Namespace: ns, ID: id}, Generation: a.generation, Namespaces: map[string]permission.NamespaceSnapshot{"path": {Allow: roots}}}})
	})
	mux.HandleFunc("/v2/permissions/tuples", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.tupleRequests++
		if a.tuplesError {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		list := permission.TupleList{Generation: a.generation, Complete: a.complete}
		namespace, relation := r.URL.Query().Get("namespace"), r.URL.Query().Get("relation")
		if namespace == "agent" && relation == "owner" && a.owner {
			list.Tuples = append(list.Tuples, permission.Tuple{Namespace: "agent", Object: "7:photos", Relation: "owner", Subject: "user:7", Effect: "allow"})
		}
		if namespace == "path" && relation == a.pathRelation && a.pathDeclared {
			effect := "deny"
			if a.read {
				effect = "allow"
			}
			list.Tuples = append(list.Tuples, permission.Tuple{Namespace: "path", Object: a.root + "/allowed", Relation: a.pathRelation, Subject: "agent:7:photos", Effect: effect})
		}
		if a.tuplesDrift && namespace == "path" && relation == "write" {
			list.Generation++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": list})
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
	p = NewAuthorityPermissionProvider(client)
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
	access, err := p.GrantsFor(context.Background(), &ZimaIdentity{Username: "alice", UserID: 7}, app)
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

func TestAgentScopeLastReadRevocationNeverFallsBackToUser(t *testing.T) {
	p, a := newAgentScopeProvider(t)
	initial := scopeAccess(t, p, "photos")
	if !initial.Allows("/media/Docs/allowed/file", OpRead, AgentCapabilities{}) || initial.Allows("/media/Docs/blocked/file", OpRead, AgentCapabilities{}) {
		t.Fatal("initial agent view does not match the declared scope")
	}
	a.change(t, p, "path", func() { a.read = false })
	access := scopeAccess(t, p, "photos")
	for _, path := range []string{"/media/Docs/allowed/file", "/media/Docs/blocked/file"} {
		if access.Allows(path, OpRead, AgentCapabilities{}) {
			t.Errorf("revoking last read promoted agent access to %s", path)
		}
	}
	if len(access.Grants) != 0 {
		t.Errorf("zero-read scoped agent projected user roots: %+v", access.Grants)
	}
}

func TestAgentScopeIsDurableWithoutAnyEffectiveRead(t *testing.T) {
	for _, tc := range []struct {
		name         string
		owner        bool
		pathDeclared bool
	}{
		{"owner-only", true, false},
		{"explicit-read-deny", true, true},
		{"owner-lost-path-statement-remains", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, a := newAgentScopeProvider(t)
			a.change(t, p, "agent", func() { a.owner, a.pathDeclared, a.read = tc.owner, tc.pathDeclared, false })
			access := scopeAccess(t, p, "photos")
			if len(access.Grants) != 0 || access.Allows("/media/Docs/blocked/file", OpRead, AgentCapabilities{}) {
				t.Fatal("empty scoped agent acquired the user view on its first projection")
			}
		})
	}
}

func TestAgentScopeClearLastPathGrantKeepsOwnerScope(t *testing.T) {
	p, a := newAgentScopeProvider(t)
	_ = scopeAccess(t, p, "photos")
	a.change(t, p, "path", func() { a.pathDeclared, a.read = false, false })
	if access := scopeAccess(t, p, "photos"); len(access.Grants) != 0 || access.Allows("/media/Docs/blocked/file", OpRead, AgentCapabilities{}) {
		t.Fatal("clearing the last path grant removed the agent's scope")
	}
}

func TestUnscopedAppKeepsUserViewButCannotOutliveScopeChange(t *testing.T) {
	p, a := newAgentScopeProvider(t)
	a.change(t, p, "agent", func() { a.owner, a.pathDeclared, a.read = false, false, false })
	legacy := scopeAccess(t, p, "photos")
	if !legacy.Allows("/media/Docs/blocked/file", OpRead, AgentCapabilities{}) {
		t.Fatal("a genuinely unscoped app lost its compatible user view")
	}
	a.change(t, p, "agent", func() { a.owner = true })
	_ = scopeAccess(t, p, "") // Even a re-warmed user snapshot must not revive the old fallback.
	if legacy.Allows("/media/Docs/blocked/file", OpRead, AgentCapabilities{}) {
		t.Fatal("old user fallback still permits reads after declaring an empty agent scope")
	}
	if access := scopeAccess(t, p, "photos"); len(access.Grants) != 0 {
		t.Fatal("agent ownership change did not narrow its namespace")
	}
}

func TestAgentScopeDeclarationReadFailsClosed(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		p, a := newAgentScopeProvider(t)
		a.change(t, p, "agent", func() {
			a.owner, a.pathDeclared, a.read = false, false, false
			a.complete = !incomplete
			a.tuplesError = !incomplete
		})
		if _, err := p.GrantsFor(context.Background(), &ZimaIdentity{Username: "alice", UserID: 7}, "photos"); err == nil {
			t.Fatal("unknown declaration state was treated as an unscoped user fallback")
		}
	}
}

func TestAgentScopeDeclarationCacheIsSharedAcrossApps(t *testing.T) {
	p, a := newAgentScopeProvider(t)
	_ = scopeAccess(t, p, "photos")
	a.mu.Lock()
	first := a.tupleRequests
	a.mu.Unlock()
	if first == 0 {
		t.Fatal("scope was inferred from effective read rather than authoritative declarations")
	}
	for _, app := range []string{"photos", "blue", "other"} {
		_ = scopeAccess(t, p, app)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tupleRequests != first {
		t.Errorf("scope declarations refetched without a generation change: %d -> %d", first, a.tupleRequests)
	}
}

func TestUnscopedAppReprojectionRefreshesUnchangedRoots(t *testing.T) {
	p, a := newAgentScopeProvider(t)
	a.change(t, p, "agent", func() { a.owner, a.pathDeclared, a.read = false, false, false })
	identity := &ZimaIdentity{Username: "alice", UserID: 7}
	access := scopeAccess(t, p, "photos")
	view, err := BuildInstanceView(identity, access, AgentCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	entry, _, err := view.Resolve("Docs")
	if err != nil || entry == nil || !view.Allows(entry, "marker", OpRead) {
		t.Fatal("legacy view did not initially allow the user grant")
	}
	// An unrelated change invalidates the old fallback, but the authority still
	// proves this app unscoped. Identical roots must refresh its decider too.
	a.change(t, p, "path", func() {})
	if view.Allows(entry, "marker", OpRead) {
		t.Fatal("old fallback did not fail closed before reprojection")
	}
	fresh, err := BuildInstanceView(identity, scopeAccess(t, p, "photos"), AgentCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := view.reproject(fresh)
	if err != nil || changed {
		t.Fatalf("unchanged roots were rebuilt: %v, %v", changed, err)
	}
	if !view.Allows(entry, "marker", OpRead) {
		t.Fatal("unchanged roots retained a stale fallback decider after reprojection")
	}
}

func TestAgentScopeNonReadStatementsStillDeclareScope(t *testing.T) {
	for _, relation := range []string{"write", "delete", "smb", "owner"} {
		t.Run(relation, func(t *testing.T) {
			p, a := newAgentScopeProvider(t)
			a.change(t, p, "path", func() { a.owner, a.read, a.pathRelation = false, false, relation })
			access := scopeAccess(t, p, "photos")
			if len(access.Grants) != 0 || access.Allows("/media/Docs/blocked/file", OpRead, AgentCapabilities{}) {
				t.Fatal("non-read declaration was treated as an unscoped user")
			}
		})
	}
}

func TestAgentScopeMixedDeclarationGenerationsFailClosed(t *testing.T) {
	p, a := newAgentScopeProvider(t)
	a.change(t, p, "path", func() { a.owner, a.pathDeclared, a.tuplesDrift = false, false, true })
	if _, err := p.GrantsFor(t.Context(), &ZimaIdentity{Username: "alice", UserID: 7}, "photos"); err == nil {
		t.Fatal("mixed declaration generations allowed a user fallback")
	}
}
