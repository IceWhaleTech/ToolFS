package toolfs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IceWhaleTech/ZimaOS-Common/v2/permission"
)

func TestEntriesGenerationDriftRecoversWithoutEventOrRestart(t *testing.T) {
	p, a := newAgentScopeProvider(t)
	identity := &ZimaIdentity{UserID: 7, Username: "alice"}
	if _, err := p.GrantsFor(t.Context(), identity, "photos"); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.generation++
	a.requests = nil
	a.mu.Unlock()
	// No SSE event: reproduce a missed/filtered generation in the live client.
	access, err := p.GrantsFor(t.Context(), identity, "photos")
	if err != nil {
		t.Fatal(err)
	}
	if access.Generation != 2 {
		t.Fatalf("generation=%d, want2", access.Generation)
	}
	a.mu.Lock()
	requests := append([]string(nil), a.requests...)
	a.mu.Unlock()
	if strings.Join(requests, ",") != "entries:application:7:photos,snapshot:application:7:photos,entries:application:7:photos" {
		t.Fatalf("repair did not fetch a fresh snapshot: %v", requests)
	}
}

func TestFailedRefreshInvalidatesLiveDecisionMemoAtSameGeneration(t *testing.T) {
	a, p, _, view := entriesFixture(t)
	entry := entryFor(t, view, "Home/alice")
	if !view.Allows(entry, "file", OpRead) {
		t.Fatal("initial grant missing")
	}
	generation := p.client.Generation()
	a.mu.Lock()
	a.snapshotStatus = 503
	a.mu.Unlock()
	if _, err := p.client.RefreshSnapshot(t.Context(), "user:7"); err == nil {
		t.Fatal("refresh hid outage")
	}
	if p.client.Generation() != generation {
		t.Fatal("refresh invented a server generation")
	}
	if view.Allows(entry, "file", OpRead) {
		t.Fatal("memoized allow survived failed refresh")
	}
	a.mu.Lock()
	a.snapshotStatus = 0
	a.mu.Unlock()
	if _, err := p.client.RefreshSnapshot(t.Context(), "user:7"); err != nil {
		t.Fatal(err)
	}
	if !view.Allows(entry, "file", OpRead) {
		t.Fatal("memoized refusal survived same-generation restoration")
	}
	if entryFor(t, view, "Home/alice") != entry {
		t.Fatal("refresh replaced the live view entry")
	}
}

func TestProductionProviderWatchesAllGlobalGenerations(t *testing.T) {
	var mu sync.Mutex
	generation := int64(1)
	events := make(chan string)
	ready := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/permissions/events":
			filter := r.URL.Query().Get("namespace")
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"generation\":1}\n\n")
			w.(http.Flusher).Flush()
			ready <- filter
			for {
				select {
				case <-r.Context().Done():
					return
				case namespace := <-events:
					if filter != "" && !strings.Contains(","+filter+",", ","+namespace+",") {
						continue
					}
					mu.Lock()
					gen := generation
					mu.Unlock()
					fmt.Fprintf(w, "data: {\"generation\":%d,\"namespaces\":[%q]}\n\n", gen, namespace)
					w.(http.Flusher).Flush()
				}
			}
		case "/v2/permissions/snapshot":
			mu.Lock()
			gen := generation
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"data": &permission.Snapshot{Subject: permission.Subject{Namespace: "user", ID: "7"}, Generation: gen, Namespaces: map[string]permission.NamespaceSnapshot{}}})
		case "/v2/permissions/entries":
			mu.Lock()
			gen := generation
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"data": &PermissionEntries{Subject: permission.Subject{Namespace: "user", ID: "7"}, Generation: gen, Entries: []PermissionEntry{}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	runtime := t.TempDir()
	if err := os.WriteFile(filepath.Join(runtime, "user-service.url"), []byte(server.URL), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	p := StartAuthorityPermissionProvider(ctx, runtime, "")
	select {
	case filter := <-ready:
		if filter != "" {
			t.Fatalf("global generation subscription filtered to %q", filter)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not connect")
	}
	for _, namespace := range []string{"app", "system", "group", "role", "application", "path"} {
		mu.Lock()
		generation++
		gen := generation
		mu.Unlock()
		select {
		case events <- namespace:
		case <-time.After(3 * time.Second):
			t.Fatal("watch stopped reading")
		}
		deadline := time.Now().Add(3 * time.Second)
		for p.client.Generation() != gen {
			if time.Now().After(deadline) {
				t.Fatalf("%s generation ignored", namespace)
			}
			time.Sleep(time.Millisecond)
		}
		if _, err := p.GrantsFor(ctx, &ZimaIdentity{UserID: 7, Username: "alice"}, ""); err != nil {
			t.Fatal(err)
		}
	}
}
