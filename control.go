//go:build linux || darwin
// +build linux darwin

package toolfs

// Control API for the toolfsd daemon.
//
// It listens on a unix socket rather than TCP. The socket file's directory
// permissions are the access control: only a process that can open the socket
// can ask for a mount, and no port is exposed to the network. That matters
// because a mount request is effectively a request to project someone's
// authorization onto the filesystem.

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ControlServer serves the daemon's HTTP API over a unix socket.
type ControlServer struct {
	mounts     *MountSet
	socketPath string
	listener   net.Listener
	server     *http.Server
}

// NewControlServer prepares a control server bound to socketPath.
func NewControlServer(mounts *MountSet, socketPath string) *ControlServer {
	return &ControlServer{mounts: mounts, socketPath: socketPath}
}

// Listen creates the unix socket. A stale socket from a previous run is
// removed first, which is safe because a live daemon holds a flock on its
// pidfile; without that removal a crashed daemon would block every restart.
func (c *ControlServer) Listen() error {
	if err := os.MkdirAll(filepath.Dir(c.socketPath), 0o755); err != nil {
		return err
	}
	if err := os.Remove(c.socketPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	listener, err := net.Listen("unix", c.socketPath)
	if err != nil {
		return err
	}
	// Owner-only: a mount request grants filesystem reach, so it must not be
	// available to every local user.
	if err := os.Chmod(c.socketPath, 0o600); err != nil {
		_ = listener.Close()
		return err
	}
	c.listener = listener

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", c.handleHealth)
	mux.HandleFunc("GET /v1/mounts", c.handleList)
	mux.HandleFunc("POST /v1/mounts", c.handleMount)
	mux.HandleFunc("DELETE /v1/mounts/{agentUserID}", c.handleUnmount)

	c.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return nil
}

// Serve blocks until the server is shut down.
func (c *ControlServer) Serve() error {
	if c.listener == nil {
		return errors.New("toolfs: control server not listening")
	}
	err := c.server.Serve(c.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the server and removes the socket file.
func (c *ControlServer) Close() error {
	if c.server != nil {
		_ = c.server.Close()
	}
	if c.socketPath != "" {
		_ = os.Remove(c.socketPath)
	}
	return nil
}

// SocketPath reports where the server is listening.
func (c *ControlServer) SocketPath() string { return c.socketPath }

type mountRequest struct {
	AgentUserID  string   `json:"agent_user_id"`
	App          string   `json:"app,omitempty"`
	AllowDelete  *bool    `json:"allow_delete,omitempty"`
	DenyPatterns []string `json:"deny_patterns,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func (c *ControlServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"mounts": len(c.mounts.List()),
	})
}

func (c *ControlServer) handleList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"mounts": c.mounts.List()})
}

func (c *ControlServer) handleMount(w http.ResponseWriter, r *http.Request) {
	var req mountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "malformed request body"})
		return
	}
	if req.AgentUserID == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "agent_user_id is required"})
		return
	}

	var caps *AgentCapabilities
	if req.AllowDelete != nil || len(req.DenyPatterns) > 0 {
		effective := c.mounts.config.DefaultCaps
		if req.AllowDelete != nil {
			effective.AllowDelete = *req.AllowDelete
		}
		if len(req.DenyPatterns) > 0 {
			effective.DenyPatterns = req.DenyPatterns
		}
		caps = &effective
	}

	info, err := c.mounts.Mount(r.Context(), req.AgentUserID, req.App, caps)
	switch {
	case errors.Is(err, ErrMountExists):
		// Idempotent from the caller's perspective: report the live mount.
		writeJSON(w, http.StatusOK, info)
	case errors.Is(err, ErrIdentityNotBound):
		// The agent account has no ZimaOS counterpart. This is "no access",
		// never "unrestricted", so it must not be retried into success.
		writeJSON(w, http.StatusForbidden, errorResponse{Error: err.Error()})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
	default:
		writeJSON(w, http.StatusCreated, info)
	}
}

func (c *ControlServer) handleUnmount(w http.ResponseWriter, r *http.Request) {
	agentUserID := r.PathValue("agentUserID")
	app := r.URL.Query().Get("app")
	err := c.mounts.Unmount(agentUserID, app)
	switch {
	case errors.Is(err, ErrMountNotFound):
		writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
