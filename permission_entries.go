package toolfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/IceWhaleTech/ZimaOS-Common/v2/permission"
)

// PermissionEntry is navigation metadata, never a grant. Paths use the public
// /media spelling; the snapshot remains the authority for every operation.
type PermissionEntry struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	ViewPath    string `json:"view_path"`
	DefaultPath string `json:"default_path"`
	Permissions struct {
		Read   bool `json:"read"`
		Write  bool `json:"write"`
		Delete bool `json:"delete"`
	} `json:"permissions"`
	TraverseOnly bool `json:"traverse_only"`
}

type PermissionEntries struct {
	Subject    permission.Subject `json:"subject"`
	Generation int64              `json:"generation"`
	Entries    []PermissionEntry  `json:"entries"`
}

type EntriesReader interface {
	Entries(context.Context, string) (*PermissionEntries, error)
}

type authorityEntriesReader struct{ address permission.AddressSource }

func newEntriesReader(address permission.AddressSource) EntriesReader {
	return &authorityEntriesReader{address: address}
}

func (r *authorityEntriesReader) Entries(ctx context.Context, subject string) (*PermissionEntries, error) {
	address := strings.TrimSpace(r.address())
	if address == "" {
		return nil, errors.New("toolfs: entries authority address is missing")
	}
	base, socket := address, ""
	switch {
	case strings.HasPrefix(address, "unix://"):
		socket = strings.TrimPrefix(address, "unix://")
	case strings.HasPrefix(address, "/"):
		socket = address
	case !strings.Contains(address, "://"):
		base = "http://" + address
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	if socket != "" {
		if !path.IsAbs(socket) {
			return nil, errors.New("toolfs: invalid entries socket")
		}
		base = "http://127.0.0.1"
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}
	}
	endpoint, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("toolfs: invalid entries authority address")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v2/permissions/entries"
	endpoint.RawQuery = url.Values{"subject": []string{subject}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("toolfs: entries request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("toolfs: entries status %d", response.StatusCode)
	}
	const maxEntriesResponse = 4 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxEntriesResponse+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxEntriesResponse {
		return nil, errors.New("toolfs: entries response too large")
	}
	var envelope struct {
		Data *PermissionEntries `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("toolfs: decode entries: %w", err)
	}
	if envelope.Data == nil || envelope.Data.Entries == nil {
		return nil, errors.New("toolfs: incomplete entries response")
	}
	return envelope.Data, nil
}

var errEntriesGenerationMismatch = errors.New("toolfs: entries generation differs from snapshot")

func validateEntries(entries *PermissionEntries, snapshot *permission.Snapshot, subject string) error {
	if entries == nil || snapshot == nil || entries.Subject.String() != subject || snapshot.Subject != entries.Subject {
		return errors.New("toolfs: entries subject differs from snapshot")
	}
	if entries.Generation != snapshot.Generation {
		return fmt.Errorf("%w (snapshot=%d entries=%d)", errEntriesGenerationMismatch, snapshot.Generation, entries.Generation)
	}
	seen := map[string]bool{}
	for _, entry := range entries.Entries {
		if !cleanPublicPath(entry.Path) || !cleanPublicPath(entry.DefaultPath) || !isPathUnder(entry.DefaultPath, entry.Path) {
			return errors.New("toolfs: invalid public entry path")
		}
		name := strings.TrimPrefix(entry.Path, "/media/")
		switch entry.Kind {
		case "home":
			if entry.Path != "/media/Home" || entry.ViewPath != "/Home" {
				return errors.New("toolfs: invalid Home entry")
			}
		case "storage":
			if !strings.HasPrefix(entry.Path, "/media/") || strings.Contains(name, "/") || name == "Home" || strings.HasPrefix(name, ".") || sanitizeEntryName(name) != name || entry.ViewPath != "/"+name || entry.DefaultPath != entry.Path {
				return errors.New("toolfs: invalid storage entry")
			}
		default:
			return errors.New("toolfs: unknown entry kind")
		}
		if seen[entry.ViewPath] {
			return errors.New("toolfs: duplicate entry mapping")
		}
		seen[entry.ViewPath] = true
	}
	return nil
}

func cleanPublicPath(p string) bool {
	return strings.HasPrefix(p, "/media/") && path.Clean(p) == p && !strings.ContainsAny(p, `\?#`) && !strings.ContainsRune(p, 0)
}

func isPathUnder(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimRight(root, "/")+"/")
}
