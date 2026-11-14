//go:build linux || darwin

package toolfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func unmountReply(t *testing.T, s *MountSet) (int, map[string]string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodDelete, "/v1/mounts/7?app=photos", nil)
	request.SetPathValue("agentUserID", "7")
	reply := httptest.NewRecorder()
	NewControlServer(s, "").handleUnmount(reply, request)
	body := map[string]string{}
	if reply.Body.Len() > 0 {
		if err := json.Unmarshal(reply.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
	}
	return reply.Code, body
}

func TestControl_UnmountDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		code    string
	}{
		{"busy", fmt.Errorf("fuse: %w", syscall.EBUSY), "busy"},
		{"permission", syscall.EACCES, "permission-denied"},
		{"operation", syscall.EPERM, "permission-denied"},
		{"text-only", errors.New("busy /private/fixture"), "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, h, _ := cleanupMountSet(t)
			h.err = tc.failure
			status, body := unmountReply(t, s)
			if status != 500 || body["stage"] != "fuse-unmount" || body["code"] != tc.code || body["error"] != tc.failure.Error() {
				t.Fatalf("reply = %d %v", status, body)
			}
			if len(s.mounts) != 1 {
				t.Fatal("failed unmount lost ledger")
			}
			if err := s.Unmount("7", "photos"); !errors.Is(err, tc.failure) {
				t.Fatalf("lost underlying error: %v", err)
			}
			h.err = nil
			if status, body := unmountReply(t, s); status != 204 || len(body) != 0 {
				t.Fatalf("retry = %d %v", status, body)
			}
			if status, body := unmountReply(t, s); status != 404 || body["stage"] != "" || body["code"] != "" {
				t.Fatalf("not found = %d %v", status, body)
			}
		})
	}
}

func TestControl_UnmountDirectoryDiagnostics(t *testing.T) {
	s, h, _ := cleanupMountSet(t)
	keep := filepath.Join(h.point, "keep")
	if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	status, body := unmountReply(t, s)
	if status != 500 || body["stage"] != "directory-cleanup" || body["code"] != "not-empty" {
		t.Fatalf("directory reply = %d %v", status, body)
	}
	if live := s.mounts[mountKey("7", "photos")]; live == nil || !live.unmounted {
		t.Fatal("directory failure lost unmounted retry state")
	}
	if content, err := os.ReadFile(keep); err != nil || string(content) != "keep" {
		t.Fatalf("unexpected contents were changed: %q, %v", content, err)
	}
	if err := os.Remove(keep); err != nil {
		t.Fatal(err)
	}
	if status, _ := unmountReply(t, s); status != 204 {
		t.Fatal(status)
	}
	if h.calls != 1 {
		t.Fatalf("completed FUSE unmount repeated: %d", h.calls)
	}
}

func TestControl_UnmountInvalidLayout(t *testing.T) {
	for _, kind := range []string{"symlink", "file", "mismatch"} {
		t.Run(kind, func(t *testing.T) {
			s, h, bind := cleanupMountSet(t)
			switch kind {
			case "symlink", "file":
				if err := os.Remove(h.point); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					if err := os.Symlink(t.TempDir(), h.point); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(h.point, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "mismatch":
				s.mounts[mountKey("7", "photos")].bindSource = filepath.Dir(bind)
			}
			status, body := unmountReply(t, s)
			if status != 500 || body["stage"] != "directory-cleanup" || body["code"] != "invalid-layout" {
				t.Fatalf("layout reply = %d %v", status, body)
			}
			if len(s.mounts) != 1 {
				t.Fatal("unsafe layout lost ledger")
			}
		})
	}
}

func TestUnmountDiagnosticsPreservesErrorChain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		code    string
	}{
		{"wrapped-busy", fmt.Errorf("fuse: %w", syscall.EBUSY), "busy"},
		{"wrapped-permission", &os.PathError{Op: "unmount", Path: "/private/fixture", Err: syscall.EACCES}, "permission-denied"},
		{"not-empty", syscall.ENOTEMPTY, "not-empty"},
		{"exists", syscall.EEXIST, "not-empty"},
		{"unknown", errors.New("busy permission denied directory not empty"), "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := &unmountError{stage: "fuse-unmount", err: tc.failure}
			wrapped := fmt.Errorf("release: %w", failure)
			if !errors.Is(wrapped, tc.failure) || failure.Error() != tc.failure.Error() {
				t.Fatalf("changed original error: %v", wrapped)
			}
			stage, code := unmountDiagnostics(wrapped)
			if stage != "fuse-unmount" || code != tc.code {
				t.Fatalf("diagnostics = %q %q", stage, code)
			}
		})
	}
	s, h, _ := cleanupMountSet(t)
	if err := os.WriteFile(filepath.Join(h.point, "keep"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	err := s.Unmount("7", "photos")
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Path != h.point || !errors.Is(err, syscall.ENOTEMPTY) {
		t.Fatalf("directory cleanup lost path/errno cause: %v", err)
	}
	stage, code := unmountDiagnostics(fmt.Errorf("release: %w", err))
	if stage != "directory-cleanup" || code != "not-empty" {
		t.Fatalf("directory diagnostics = %q %q", stage, code)
	}
}

func TestUnmountDiagnosticsAllowsOnlyKnownFields(t *testing.T) {
	for _, failure := range []error{
		nil,
		errors.New("fuse-unmount busy /private/fixture"),
		&unmountError{stage: "/private/fixture", err: errors.New("busy")},
	} {
		stage, code := unmountDiagnostics(failure)
		if stage != "unknown" || code != "unknown" {
			t.Fatalf("untrusted text became diagnostics: %q %q", stage, code)
		}
	}
	layout := mountLayoutError("busy /private/fixture")
	stage, code := unmountDiagnostics(&unmountError{stage: "directory-cleanup", err: fmt.Errorf("cleanup: %w", layout)})
	if stage != "directory-cleanup" || code != "invalid-layout" {
		t.Fatalf("typed layout failure lost classification: %q %q", stage, code)
	}
}
