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
	"sync"
	"testing"
	"time"

	"github.com/IceWhaleTech/ZimaOS-Common/permission"
)

// fakeAuthority serves /v2/permissions/snapshot and /events from mutable state.
type fakeAuthority struct {
	mu         sync.Mutex
	generation int64
	allow      map[string][]string // object → relations
	deny       map[string][]string
	events     chan int64
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
		for object, relations := range a.allow {
			allow = append(allow, grant{Object: object, Relations: relations})
		}
		for object, relations := range a.deny {
			deny = append(deny, grant{Object: object, Relations: relations})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"subject":    map[string]string{"namespace": "user", "id": "7"},
			"generation": a.generation,
			"namespaces": map[string]any{
				"path": map[string]any{"allow": allow, "deny": deny},
			},
		}})
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
	provider = NewAuthorityPermissionProvider(client)

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
