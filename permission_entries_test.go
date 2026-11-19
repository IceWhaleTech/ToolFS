package toolfs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IceWhaleTech/ZimaOS-Common/v2/permission"
)

func TestAuthorityRequiresEntriesAPI(t *testing.T) {
	authority := newFakeAuthority()
	authority.entriesStatus = 404
	authority.allow["/media/Docs"] = []string{"read"}
	provider := startProvider(t, authority)
	// The fixture intentionally has no entries endpoint yet. Snapshot rules alone
	// must not invent user navigation entries.
	if _, err := provider.GrantsFor(context.Background(), &ZimaIdentity{Username: "alice", UserID: 7}, ""); err == nil {
		t.Fatal("authority without entries API was accepted")
	}
}

func entriesFixture(t *testing.T) (*fakeAuthority, *AuthorityPermissionProvider, string, *InstanceView) {
	t.Helper()
	base := t.TempDir()
	for _, dir := range []string{"Backup/Projects/A", "Backup/Projects/hidden", "System/.home/alice", "System/.home/bob/Photos", "System/.home/bob/private", "System/.home/carol"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	a := newFakeAuthority()
	a.pathAliases = []permission.PathAlias{
		{Canonical: "/media/Backup", Alias: filepath.Join(base, "Backup")},
		{Canonical: "/media/System", Alias: filepath.Join(base, "System")},
		{Canonical: "/media/System/.home", Alias: "/media/Home"},
	}
	a.allow["/media/Home/alice"] = []string{"read", "write"}
	a.allow["/media/Home/bob/Photos"] = []string{"read"}
	a.allow["/media/Backup/Projects/A"] = []string{"read"}
	a.allow["/var/log"] = []string{"read", "write"}
	a.entries = []PermissionEntry{
		{Kind: "home", Name: "Home", Path: "/media/Home", ViewPath: "/Home", DefaultPath: "/media/Home/alice", TraverseOnly: true},
		{Kind: "storage", Name: "Backup", Path: "/media/Backup", ViewPath: "/Backup", DefaultPath: "/media/Backup", TraverseOnly: true},
	}
	p := startProvider(t, a)
	view, err := BuildInstanceViewForIdentity(t.Context(), p, &ZimaIdentity{Username: "alice", UserID: 7}, "", AgentCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	return a, p, base, view
}

func TestEntriesBoundProjectionAndRetainPublicAncestry(t *testing.T) {
	_, _, _, view := entriesFixture(t)
	assertEntries(t, "entries", view.Entries(), []string{"Backup", "Home"})
	for _, vp := range []string{"Home/alice", "Home/bob/Photos", "Backup/Projects/A"} {
		if _, _, err := view.Resolve(vp); err != nil {
			t.Fatalf("missing %s: %v", vp, err)
		}
	}
	for _, vp := range []string{"var/log", "System", "Home/bob/private", "Home/carol"} {
		if hasEntry(view, vp) {
			t.Fatalf("unlisted/ungranted path exposed: %s", vp)
		}
	}
	if err := view.openRoots(); err != nil {
		t.Fatal(err)
	}
	defer view.closeRoots()
	for _, entry := range view.Grants() {
		if entry.ViewPath == "/Home" || entry.ViewPath == "/Backup" {
			t.Fatal("traversal-only collection opened as backing")
		}
	}
}

func TestEntriesMetadataFailureCannotKeepRevokedNamesVisible(t *testing.T) {
	a, p, _, view := entriesFixture(t)
	node := view.nodeAt("/Home/bob")
	if !view.nodeVisible(node) {
		t.Fatal("shared owner missing")
	}
	a.mu.Lock()
	a.entriesStatus = http.StatusServiceUnavailable
	a.mu.Unlock()
	a.change(func(allow, deny map[string][]string) { delete(allow, "/media/Home/bob/Photos") })
	deadline := time.After(5 * time.Second)
	for view.nodeVisible(node) {
		select {
		case <-deadline:
			t.Fatal("stale navigation witness after revoke")
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := BuildInstanceViewForIdentity(t.Context(), p, view.Identity, "", view.Caps); err == nil {
		t.Fatal("metadata failure accepted")
	}
	assertEntries(t, "last tree, live permissions", view.Entries(), []string{"Backup", "Home"})
	if view.currentSynthetic(node) != nil {
		t.Fatal("old synthetic inode exposed revoked owner")
	}
}

func TestEntriesRejectMismatchesAndMalformedMappings(t *testing.T) {
	snapshot := &permission.Snapshot{Subject: permission.Subject{Namespace: "user", ID: "7"}, Generation: 3}
	good := PermissionEntry{Kind: "storage", Path: "/media/Backup", ViewPath: "/Backup", DefaultPath: "/media/Backup"}
	tests := []struct {
		name   string
		modify func(*PermissionEntries)
	}{
		{"subject", func(e *PermissionEntries) { e.Subject.ID = "8" }},
		{"generation", func(e *PermissionEntries) { e.Generation++ }},
		{"kind", func(e *PermissionEntries) { e.Entries[0].Kind = "shared" }},
		{"deep storage", func(e *PermissionEntries) { e.Entries[0].Path += "/A" }},
		{"system directory", func(e *PermissionEntries) { e.Entries[0].Path = "/var/log" }},
		{"view", func(e *PermissionEntries) { e.Entries[0].ViewPath = "/Elsewhere" }},
		{"default escape", func(e *PermissionEntries) { e.Entries[0].DefaultPath = "/media/Elsewhere" }},
		{"duplicate", func(e *PermissionEntries) { e.Entries = append(e.Entries, e.Entries[0]) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &PermissionEntries{Subject: snapshot.Subject, Generation: 3, Entries: []PermissionEntry{good}}
			tc.modify(e)
			if validateEntries(e, snapshot, "user:7") == nil {
				t.Fatal("invalid entries accepted")
			}
		})
	}
}

func TestEntriesReaderHTTPUnixAndFailures(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/permissions/entries" || r.URL.Query().Get("subject") != "application:7:photos" {
			t.Errorf("request: %s", r.URL)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": PermissionEntries{Subject: permission.Subject{Namespace: "agent", ID: "7:photos"}, Generation: 1, Entries: []PermissionEntry{}}})
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	socketDir, err := os.MkdirTemp("/tmp", "entries-socket.")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)
	socket := filepath.Join(socketDir, "authority.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	unixServer := &http.Server{Handler: handler}
	defer unixServer.Close()
	go func() { _ = unixServer.Serve(listener) }()
	for _, address := range []string{server.URL, strings.TrimPrefix(server.URL, "http://"), socket, "unix://" + socket} {
		if _, err := newEntriesReader(func() string { return address }).Entries(t.Context(), "application:7:photos"); err != nil {
			t.Errorf("transport %s: %v", address, err)
		}
	}
	for _, body := range []string{`{}`, `{"data":{}}`, `{"data":{"entries":null}}`, `not-json`, strings.Repeat("x", (4<<20)+1)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
			defer s.Close()
			if _, err := newEntriesReader(func() string { return s.URL }).Entries(t.Context(), "user:7"); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

type entriesReaderFunc func(context.Context, string) (*PermissionEntries, error)

func (f entriesReaderFunc) Entries(ctx context.Context, subject string) (*PermissionEntries, error) {
	return f(ctx, subject)
}

func TestEntriesProviderPairsSelectedSubjectAndGeneration(t *testing.T) {
	for _, mismatch := range []string{"subject", "generation", "relation"} {
		t.Run(mismatch, func(t *testing.T) {
			a := newFakeAuthority()
			a.allow["/media/Docs"] = []string{"read"}
			p := startProvider(t, a)
			var requests int
			p.entries = entriesReaderFunc(func(_ context.Context, subject string) (*PermissionEntries, error) {
				requests++
				if subject != "user:7" {
					t.Fatalf("wrong subject: %s", subject)
				}
				result := &PermissionEntries{Subject: permission.Subject{Namespace: "user", ID: "7"}, Generation: 1, Entries: []PermissionEntry{}}
				switch mismatch {
				case "subject":
					result.Subject.ID = "8"
				case "generation":
					result.Generation++
				case "relation":
					result.Subject.Relation = "member"
				}
				return result, nil
			})
			if _, err := p.GrantsFor(t.Context(), &ZimaIdentity{Username: "alice", UserID: 7}, ""); err == nil {
				t.Fatal("mismatched response published")
			}
			if requests != 3 {
				t.Fatalf("unbounded/absent retry: %d", requests)
			}
		})
	}
}

func TestEntriesMetadataRecoveryKeepsLastRoots(t *testing.T) {
	a, p, _, view := entriesFixture(t)
	if err := view.openRoots(); err != nil {
		t.Fatal(err)
	}
	defer view.closeRoots()
	old := entryFor(t, view, "Home/alice")
	root := old.root
	a.mu.Lock()
	a.entriesStatus = http.StatusServiceUnavailable
	a.mu.Unlock()
	if _, err := BuildInstanceViewForIdentity(t.Context(), p, view.Identity, "", view.Caps); err == nil {
		t.Fatal("metadata error accepted")
	}
	if entryFor(t, view, "Home/alice") != old || !view.Allows(old, "", OpRead) {
		t.Fatal("metadata error changed published root")
	}
	a.mu.Lock()
	a.entriesStatus = 0
	a.mu.Unlock()
	next, err := BuildInstanceViewForIdentity(t.Context(), p, view.Identity, "", view.Caps)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := view.reproject(next); err != nil || changed {
		t.Fatalf("recovery replaced unchanged roots: %v %v", changed, err)
	}
	if entryFor(t, view, "Home/alice").root != root {
		t.Fatal("recovery detached unchanged backing")
	}
}

func TestEntriesPublicWireContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/public-entries.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Snapshot permission.Snapshot `json:"snapshot"`
		Data     PermissionEntries   `json:"data"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("subject") != "user:10001" {
			t.Errorf("wrong query: %s", r.URL)
		}
		switch r.URL.Path {
		case "/v2/permissions/snapshot":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": &fixture.Snapshot})
		case "/v2/permissions/entries":
			_, _ = w.Write(raw)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider := NewAuthorityPermissionProvider(permission.New(func() string { return server.URL }), newEntriesReader(func() string { return server.URL }))
	identity := &ZimaIdentity{Username: "alice", UserID: 10001}
	access, err := provider.GrantsFor(t.Context(), identity, "")
	if err != nil {
		t.Fatal(err)
	}
	view, err := BuildInstanceView(identity, access, AgentCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	assertEntries(t, "public contract", view.Entries(), []string{"Backup", "Home"})
	for _, vp := range []string{"Home/alice", "Home/bob/Photos", "Backup/Projects/A"} {
		if _, _, err := view.Resolve(vp); err != nil {
			t.Fatalf("missing public ancestry %s: %v", vp, err)
		}
	}
	if hasEntry(view, "var/log") || hasEntry(view, "ZimaOS-HD") {
		t.Fatal("Home-only or system grant became storage entry")
	}
	if access.entries[1].Name != "备份" || entryFor(t, view, "Backup/Projects/A").ViewPath != "/Backup/Projects/A" {
		t.Fatal("display label changed backing mapping")
	}
}
