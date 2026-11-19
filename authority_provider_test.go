package toolfs

// Authority-backed provider tests.
//
// A fake authority stands in for ZimaOS-UserService: the same snapshot
// envelope, the same event stream. What is under test is the projection
// (authority snapshot → namespace entries) and the property the integration
// exists for — a revocation reaching an already-built view without a remount.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IceWhaleTech/ZimaOS-Common/v2/permission"
)

// fakeAuthority serves /v2/permissions/snapshot and /events from mutable state.
type fakeAuthority struct {
	mu            sync.Mutex
	generation    int64
	allow         map[string][]string // object → relations
	deny          map[string][]string
	events        chan int64
	pathAliases   []permission.PathAlias
	entries       []PermissionEntry
	entriesStatus int
}

func newFakeAuthority() *fakeAuthority {
	return &fakeAuthority{
		generation: 1,
		allow:      map[string][]string{},
		deny:       map[string][]string{},
		events:     make(chan int64, 8),
	}
}

func (a *fakeAuthority) serve(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/v2/permissions/snapshot", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()

		type grant struct {
			Object    string   `json:"object"`
			Relations []string `json:"relations"`
		}
		var allow, deny []grant
		canonical := &permission.Snapshot{PathAliases: a.pathAliases}
		for object, relations := range a.allow {
			allow = append(allow, grant{Object: canonical.CanonicalPath(object), Relations: relations})
		}
		for object, relations := range a.deny {
			deny = append(deny, grant{Object: canonical.CanonicalPath(object), Relations: relations})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"subject":      map[string]string{"namespace": "user", "id": "7"},
			"generation":   a.generation,
			"path_aliases": a.pathAliases,
			"namespaces": map[string]any{
				"path": map[string]any{"allow": allow, "deny": deny},
			},
		}})
	})

	mux.HandleFunc("/v2/permissions/entries", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.entriesStatus != 0 {
			http.Error(w, "unavailable", a.entriesStatus)
			return
		}
		entries := a.entries
		if entries == nil {
			entries = fixturePermissionEntries(a.allow, a.pathAliases)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": PermissionEntries{Subject: permission.Subject{Namespace: "user", ID: "7"}, Generation: a.generation, Entries: entries}})
	})

	mux.HandleFunc("/v2/permissions/events", func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		a.mu.Lock()
		fmt.Fprintf(w, "data: {\"generation\":%d}\n\n", a.generation)
		a.mu.Unlock()
		flusher.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case generation := <-a.events:
				fmt.Fprintf(w, "data: {\"generation\":%d,\"namespaces\":[\"path\"]}\n\n", generation)
				flusher.Flush()
			}
		}
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// change mutates the state, bumps the generation and announces the event.
func (a *fakeAuthority) change(mutate func(allow, deny map[string][]string)) {
	a.mu.Lock()
	mutate(a.allow, a.deny)
	a.generation++
	generation := a.generation
	a.mu.Unlock()
	a.events <- generation
}

// startProvider wires a provider against the fake, with the watch running.
func startProvider(t *testing.T, authority *fakeAuthority) *AuthorityPermissionProvider {
	t.Helper()
	server := authority.serve(t)

	var provider *AuthorityPermissionProvider
	client := permission.New(
		func() string { return server.URL },
		permission.WithWatchNamespaces(permission.NamespacePath),
		permission.WithChangeHandler(func(generation int64) { provider.handleChange(generation) }),
	)
	provider = NewAuthorityPermissionProvider(client, newEntriesReader(func() string { return server.URL }))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); client.Watch(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	return provider
}

func TestAuthorityProjectionAndDecisions(t *testing.T) {
	authority := newFakeAuthority()
	authority.allow["/media/Docs"] = []string{"read", "write", "delete", "smb"}
	authority.allow["/media/Photos"] = []string{"read"}
	authority.deny["/media/Docs/keep"] = []string{"write", "delete"}

	provider := startProvider(t, authority)
	identity := &ZimaIdentity{Username: "alice", UserID: 7}

	access, err := provider.GrantsFor(context.Background(), identity, "")
	if err != nil {
		t.Fatalf("GrantsFor: %v", err)
	}

	if len(access.Grants) != 2 {
		t.Fatalf("projected %d roots, want 2: %+v", len(access.Grants), access.Grants)
	}
	byPath := map[string]Grant{}
	for _, grant := range access.Grants {
		byPath[grant.Path] = grant
	}
	if byPath["/media/Docs"].ReadOnly {
		t.Error("a writable root projected as read-only")
	}
	if !byPath["/media/Photos"].ReadOnly {
		t.Error("a read-only root projected as writable")
	}

	caps := AgentCapabilities{}
	capsDelete := AgentCapabilities{AllowDelete: true}
	for _, c := range []struct {
		path    string
		op      AccessOp
		caps    AgentCapabilities
		allowed bool
		reason  string
	}{
		{"/media/Docs/f.txt", OpModify, caps, true, "write is held on the root"},
		{"/media/Docs/f.txt", OpDelete, caps, false, "delete stays withheld without the capability"},
		{"/media/Docs/f.txt", OpDelete, capsDelete, true, "the capability releases delete the authority grants"},
		{"/media/Docs/keep/f", OpModify, caps, false, "the nested deny narrows write below the root"},
		{"/media/Docs/keep/f", OpDelete, capsDelete, false, "the nested deny withholds delete too"},
		{"/media/Docs/keep/f", OpRead, caps, true, "the nested deny does not touch read"},
		{"/media/Photos/p.jpg", OpRead, caps, true, "a read-only root still reads"},
		{"/media/Photos/p.jpg", OpModify, caps, false, "a read-only root refuses writes"},
		{"/media/Elsewhere", OpRead, caps, false, "an ungranted path is refused"},
	} {
		if got := access.Allows(c.path, c.op, c.caps); got != c.allowed {
			t.Errorf("Allows(%q, %v) = %v, want %v — %s", c.path, c.op, got, c.allowed, c.reason)
		}
	}
}

// The property the integration exists for: a revocation reaches a view that
// was built before the change, without anyone rebuilding or remounting it.
func TestRevocationReachesABuiltViewWithoutRemount(t *testing.T) {
	authority := newFakeAuthority()
	authority.allow["/media/Docs"] = []string{"read", "write"}

	provider := startProvider(t, authority)
	identity := &ZimaIdentity{Username: "alice", UserID: 7}

	access, err := provider.GrantsFor(context.Background(), identity, "")
	if err != nil {
		t.Fatalf("GrantsFor: %v", err)
	}
	view, err := BuildInstanceView(identity, access, AgentCapabilities{})
	if err != nil {
		t.Fatalf("BuildInstanceView: %v", err)
	}
	// /media/Docs projects to the top-level space "Docs" (the /media prefix is
	// stripped by the default alias table).
	entry, _, err := view.Resolve("Docs")
	if err != nil || entry == nil {
		t.Fatalf("the granted root is missing from the view; entries = %v", view.Entries())
	}
	if !view.Allows(entry, "f.txt", OpModify) {
		t.Fatal("write is refused before the change — the premise of the test is broken")
	}

	// The authority narrows the grant to read-only and announces it.
	authority.change(func(allow, _ map[string][]string) {
		allow["/media/Docs"] = []string{"read"}
	})

	// The event has to travel: SSE frame → cache invalidation → re-warm. The
	// deadline is generous; the point is the arrow of time, not its length.
	deadline := time.After(5 * time.Second)
	for view.Allows(entry, "f.txt", OpModify) {
		select {
		case <-deadline:
			t.Fatal("the revocation never reached the built view")
		case <-time.After(10 * time.Millisecond):
		}
	}

	if !view.Allows(entry, "f.txt", OpRead) {
		t.Error("read was lost along with write — the narrowing over-shot")
	}
}

// A provider error must never be read as "no restrictions".
func TestAuthorityProviderFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	provider := NewAuthorityPermissionProvider(permission.New(func() string { return server.URL }))
	if _, err := provider.GrantsFor(context.Background(), &ZimaIdentity{Username: "alice", UserID: 7}, ""); err == nil {
		t.Fatal("an authority error produced a snapshot instead of an error")
	}
}

func fixturePermissionEntries(allow map[string][]string, aliases []permission.PathAlias) []PermissionEntry {
	entries := []PermissionEntry{}
	seen := map[string]bool{}
	snapshot := &permission.Snapshot{PathAliases: aliases}
	home := snapshot.CanonicalPath("/media/Home")
	for raw, relations := range allow {
		readable := false
		for _, r := range relations {
			if r == "read" {
				readable = true
			}
		}
		if !readable {
			continue
		}
		host := snapshot.CanonicalPath(raw)
		var entry PermissionEntry
		if isPathUnder(host, home) {
			entry = PermissionEntry{Kind: "home", Name: "Home", Path: "/media/Home", ViewPath: "/Home", DefaultPath: "/media/Home/alice"}
		} else {
			if !strings.HasPrefix(host, "/media/") {
				continue
			}
			name := strings.Split(strings.TrimPrefix(host, "/media/"), "/")[0]
			if name == "" || name == "Home" {
				continue
			}
			entry = PermissionEntry{Kind: "storage", Name: name, Path: "/media/" + name, ViewPath: "/" + name, DefaultPath: "/media/" + name}
		}
		if seen[entry.Path] {
			continue
		}
		seen[entry.Path] = true
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ViewPath < entries[j].ViewPath })
	return entries
}

func TestAgentDeleteRequiresExplicitRelationAndRevokesLive(t *testing.T) {
	p, a := newAgentScopeProvider(t)
	a.change(t, p, "agent", func() {
		a.owner = true
		a.grants["/media/Docs/A"] = []string{"read", "write"}
	})
	access := scopeAccess(t, p, "photos")
	view, err := BuildInstanceView(&ZimaIdentity{Username: "alice", UserID: 7}, access, AgentCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	entry, rel, err := view.Resolve("Docs/A/file")
	if err != nil {
		t.Fatal(err)
	}
	path := "/media/Docs/A/file"
	assertOps := func(want AccessOp) {
		t.Helper()
		for _, caps := range []AgentCapabilities{{}, {AllowDelete: false}, {AllowDelete: true}} {
			if got := access.AllowedOps(path, caps); got != want {
				t.Errorf("old snapshot with AllowDelete=%v: ops = %s, want %s", caps.AllowDelete, got, want)
			}
		}
		if got := view.allowedOps(entry, rel); got != want {
			t.Errorf("old view: ops = %s, want %s", got, want)
		}
	}
	rw := OpRead | OpList | OpCreate | OpModify
	assertOps(rw)
	changed := make(chan int64, 4)
	p.OnChange(func(generation int64) { changed <- generation })
	change := func(mutate func()) {
		t.Helper()
		a.change(t, p, "path", mutate)
		a.mu.Lock()
		generation := a.generation
		a.mu.Unlock()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case got := <-changed:
				if got == generation {
					return
				}
			case <-deadline:
				t.Fatal("permission generation was not re-warmed")
			}
		}
	}
	change(func() { a.grants["/media/Docs/A"] = []string{"read", "write", "delete"} })
	assertOps(rw | OpDelete)
	change(func() { a.grants["/media/Docs/A"] = []string{"read", "write"} })
	assertOps(rw)
	change(func() { a.grants["/media/Docs/A"] = []string{"read", "write", "delete"} })
	assertOps(rw | OpDelete)
	change(func() { a.ownerGrants["/media/Docs"] = []string{"read", "write"} })
	assertOps(rw)
	change(func() { a.grants = map[string][]string{} })
	assertOps(0)
	change(func() {
		a.ownerGrants["/media/Docs"] = []string{"read", "write", "delete"}
		a.grants["/media/Docs/A"] = []string{"read", "write", "delete"}
	})
	assertOps(rw | OpDelete)
	change(func() { a.snapshotStatus = http.StatusServiceUnavailable })
	assertOps(0)
}

func TestAgentDeleteHonorsEffectiveRelations(t *testing.T) {
	for _, tc := range []struct {
		name      string
		relations []string
		deny      []string
		owner     []string
		allowed   bool
	}{
		{name: "explicit", relations: []string{"read", "write", "delete"}, allowed: true},
		{name: "read-write", relations: []string{"read", "write"}},
		{name: "read-delete", relations: []string{"read", "delete"}},
		{name: "write-delete", relations: []string{"write", "delete"}},
		{name: "delete-only", relations: []string{"delete"}},
		{name: "empty"},
		{name: "delete-deny", relations: []string{"read", "write", "delete"}, deny: []string{"delete"}},
		{name: "write-deny", relations: []string{"read", "write", "delete"}, deny: []string{"write"}},
		{name: "read-deny", relations: []string{"read", "write", "delete"}, deny: []string{"read"}},
		{name: "owner-cap", relations: []string{"read", "write", "delete"}, owner: []string{"read", "write"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, a := newAgentScopeProvider(t)
			a.mu.Lock()
			a.owner = true
			a.grants["/media/Docs"] = tc.relations
			a.denied["/media/Docs/nested"] = tc.deny
			if tc.owner != nil {
				a.ownerGrants["/media/Docs"] = tc.owner
			}
			a.mu.Unlock()
			access := scopeAccess(t, p, "photos")
			for _, caps := range []AgentCapabilities{{}, {AllowDelete: true}} {
				if got := access.Allows("/media/Docs/nested/file", OpDelete, caps); got != tc.allowed {
					t.Errorf("delete with AllowDelete=%v: got %v, want %v", caps.AllowDelete, got, tc.allowed)
				}
			}
			if tc.allowed {
				view, err := BuildInstanceView(&ZimaIdentity{Username: "alice", UserID: 7}, access,
					AgentCapabilities{DenyPatterns: []string{"blocked"}})
				if err != nil {
					t.Fatal(err)
				}
				entry, _, err := view.Resolve("Docs")
				if err != nil {
					t.Fatal(err)
				}
				if !view.Allows(entry, "nested/file", OpDelete) || view.Allows(entry, "blocked/file", OpDelete) {
					t.Fatal("authority deletion bypassed DenyPatterns or lost its explicit relation")
				}
			}
		})
	}
}
