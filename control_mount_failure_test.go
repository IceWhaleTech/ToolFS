//go:build linux || darwin

package toolfs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A permission failure precedes FUSE, so this API regression must run even on
// hosts where the successful-mount control fixture calls requireFUSE and skips.
func TestControl_MountFailureRollsBackDirectoriesWithoutLedger(t *testing.T) {
	failure := errors.New("permission declarations changed while being read")
	s := failingMountSet(t, func(context.Context) (*AccessSnapshot, error) { return nil, failure })
	control := NewControlServer(s, "")
	request := httptest.NewRequest(http.MethodPost, "/v1/mounts", strings.NewReader(`{"agent_user_id":"7","app":"photos"}`))
	created := httptest.NewRecorder()
	control.handleMount(created, request)
	if created.Code != http.StatusInternalServerError || !strings.Contains(created.Body.String(), failure.Error()) {
		t.Fatalf("permission failure changed API semantics: %d %s", created.Code, created.Body.String())
	}
	listed := httptest.NewRecorder()
	control.handleList(listed, httptest.NewRequest(http.MethodGet, "/v1/mounts", nil))
	var body struct {
		Mounts []MountInfo `json:"mounts"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if listed.Code != http.StatusOK || len(body.Mounts) != 0 {
		t.Fatalf("failed mount was recorded: %d %s", listed.Code, listed.Body.String())
	}
	request = httptest.NewRequest(http.MethodDelete, "/v1/mounts/7?app=photos", nil)
	request.SetPathValue("agentUserID", "7")
	removed := httptest.NewRecorder()
	control.handleUnmount(removed, request)
	if removed.Code != http.StatusNotFound {
		t.Fatalf("failed mount unexpectedly became releasable: %d", removed.Code)
	}
	if _, err := os.Stat(filepath.Join(s.config.Root, "u7")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("POST failure left directories for DELETE 404 to strand: %v", err)
	}
	if _, err := os.Stat(s.config.Root); err != nil {
		t.Fatalf("configured root removed: %v", err)
	}
}
