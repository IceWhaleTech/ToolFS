package toolfs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IceWhaleTech/ZimaOS-Common/v2/permission"
)

// Measures control-plane invalidation and re-warming, not FUSE throughput or
// device authority/database latency. Every event models an unrelated domain.
func BenchmarkGlobalGenerationRewarm(b *testing.B) {
	for _, subjects := range []int{1, 8} {
		b.Run(fmt.Sprintf("subjects=%d", subjects), func(b *testing.B) {
			var generation atomic.Int64
			generation.Store(1)
			var reads atomic.Int64
			events := make(chan int64)
			done := make(chan int64, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/permissions/events" {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"generation\":1}\n\n")
					w.(http.Flusher).Flush()
					for {
						select {
						case <-r.Context().Done():
							return
						case gen := <-events:
							fmt.Fprintf(w, "data: {\"generation\":%d,\"namespaces\":[\"app\"]}\n\n", gen)
							w.(http.Flusher).Flush()
						}
					}
				}
				reads.Add(1)
				ns, id, _ := strings.Cut(r.URL.Query().Get("subject"), ":")
				json.NewEncoder(w).Encode(map[string]any{"data": &permission.Snapshot{Subject: permission.Subject{Namespace: ns, ID: id}, Generation: generation.Load(), Namespaces: map[string]permission.NamespaceSnapshot{}}})
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var provider *AuthorityPermissionProvider
			client := permission.New(func() string { return server.URL }, permission.WithChangeHandler(func(gen int64) { provider.handleChange(gen); done <- gen }))
			provider = NewAuthorityPermissionProvider(client)
			go client.Watch(ctx)
			<-done
			for i := range subjects {
				subject := fmt.Sprintf("user:%d", i+1)
				if _, err := client.Snapshot(ctx, subject); err != nil {
					b.Fatal(err)
				}
				provider.served[subject] = &ZimaIdentity{UserID: i + 1, Username: subject}
			}
			reads.Store(0)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				gen := generation.Add(1)
				events <- gen
				if got := <-done; got != gen {
					b.Fatalf("generation=%d, want%d", got, gen)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(reads.Load())/float64(b.N), "HTTP/event")
		})
	}
}

func BenchmarkLiveDecisionCache(b *testing.B) {
	var reads atomic.Int64
	base := b.TempDir()
	snapshot := &permission.Snapshot{Subject: permission.Subject{Namespace: "user", ID: "7"}, Generation: 1,
		PathAliases: []permission.PathAlias{{Canonical: "/media/Docs", Alias: base}},
		Namespaces:  map[string]permission.NamespaceSnapshot{"path": {Allow: []permission.Grant{{Object: "/media/Docs", Relations: []string{"read"}}}}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"data": snapshot})
	}))
	defer server.Close()
	client := permission.New(func() string { return server.URL })
	provider := NewAuthorityPermissionProvider(client, entriesReaderFunc(func(context.Context, string) (*PermissionEntries, error) {
		return &PermissionEntries{Subject: snapshot.Subject, Generation: 1, Entries: []PermissionEntry{{Kind: "storage", Name: "Docs", Path: "/media/Docs", ViewPath: "/Docs", DefaultPath: "/media/Docs"}}}, nil
	}))
	view, err := BuildInstanceViewForIdentity(context.Background(), provider, &ZimaIdentity{UserID: 7, Username: "bench"}, "", AgentCapabilities{})
	if err != nil {
		b.Fatal(err)
	}
	entry, _, err := view.Resolve("Docs")
	if err != nil || !view.Allows(entry, "file", OpRead) {
		b.Fatalf("view setup failed: %v", err)
	}
	reads.Store(0)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !view.Allows(entry, "file", OpRead) {
			b.Fatal("cached decision refused")
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(reads.Load())/float64(b.N), "HTTP/op")
}
