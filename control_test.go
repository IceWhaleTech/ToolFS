//go:build linux || darwin
// +build linux darwin

package toolfs

// Control API tests. These exercise the real unix socket, so they need FUSE.

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// newControlFixture starts a daemon-equivalent control server on a socket.
func newControlFixture(t *testing.T) (client *http.Client, baseURL string, mounts *MountSet) {
	t.Helper()
	requireFUSE(t)

	base, resolver, provider := twoUserFixture(t)

	root := filepath.Join(t.TempDir(), "mnt")
	mounts, err := NewMountSet(MountSetConfig{Root: root, DefaultCaps: capsBase(base)}, resolver, provider)
	if err != nil {
		t.Fatalf("建 MountSet: %v", err)
	}
	t.Cleanup(func() { _ = mounts.Close() })

	socketPath := filepath.Join(t.TempDir(), "toolfsd.sock")
	server := NewControlServer(mounts, socketPath)
	if err := server.Listen(); err != nil {
		t.Fatalf("监听控制 socket: %v", err)
	}
	go func() { _ = server.Serve() }()
	t.Cleanup(func() { _ = server.Close() })

	client = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		},
	}
	return client, "http://toolfsd", mounts
}

func TestControl_MountAndUnmountLifecycle(t *testing.T) {
	client, baseURL, _ := newControlFixture(t)

	// 建挂载
	var info MountInfo
	status := doJSON(t, client, http.MethodPost, baseURL+"/v1/mounts",
		map[string]any{"agent_user_id": "agent-user-1"}, &info)
	if status != http.StatusCreated {
		t.Fatalf("建挂载返回 %d，期望 201", status)
	}
	if info.Username != "alice" {
		t.Errorf("解析出的 ZimaOS 用户为 %q，期望 alice", info.Username)
	}
	// bind 源必须是挂载点的父级——这是让 toolfsd 能重挂而消费者不会静默沿用旧视图的前提
	if filepath.Dir(info.MountPoint) != info.BindSource {
		t.Errorf("bind 源 %q 不是挂载点 %q 的父级", info.BindSource, info.MountPoint)
	}
	if filepath.Base(info.MountPoint) != "workspace" {
		t.Errorf("挂载点为 %q，期望以 workspace 结尾", info.MountPoint)
	}

	// 重复建挂载是幂等的，返回既有挂载
	var again MountInfo
	status = doJSON(t, client, http.MethodPost, baseURL+"/v1/mounts",
		map[string]any{"agent_user_id": "agent-user-1"}, &again)
	if status != http.StatusOK {
		t.Errorf("重复建挂载返回 %d，期望 200（幂等）", status)
	}
	if again.MountPoint != info.MountPoint {
		t.Errorf("重复请求返回了不同的挂载点 %q vs %q", again.MountPoint, info.MountPoint)
	}

	// 列表
	var listed struct {
		Mounts []MountInfo `json:"mounts"`
	}
	status = doJSON(t, client, http.MethodGet, baseURL+"/v1/mounts", nil, &listed)
	if status != http.StatusOK || len(listed.Mounts) != 1 {
		t.Fatalf("列表返回 %d，%d 个挂载，期望 200 与 1 个", status, len(listed.Mounts))
	}

	// 卸载
	status = doJSON(t, client, http.MethodDelete, baseURL+"/v1/mounts/agent-user-1", nil, nil)
	if status != http.StatusNoContent {
		t.Errorf("卸载返回 %d，期望 204", status)
	}

	// 再次卸载应为 404
	status = doJSON(t, client, http.MethodDelete, baseURL+"/v1/mounts/agent-user-1", nil, nil)
	if status != http.StatusNotFound {
		t.Errorf("卸载不存在的挂载返回 %d，期望 404", status)
	}
}

// 未绑定的 agent 账号必须被拒，且不能因为重试而变成成功。
func TestControl_UnboundAccountIsRefused(t *testing.T) {
	client, baseURL, _ := newControlFixture(t)

	var resp errorResponse
	status := doJSON(t, client, http.MethodPost, baseURL+"/v1/mounts",
		map[string]any{"agent_user_id": "nobody"}, &resp)
	if status != http.StatusForbidden {
		t.Fatalf("未绑定账号返回 %d，期望 403", status)
	}
	if resp.Error == "" {
		t.Error("未返回错误说明")
	}
}

// 两个用户各自挂载后，挂载点互不相同且视图互不包含。
func TestControl_TwoUsersGetDistinctMounts(t *testing.T) {
	client, baseURL, _ := newControlFixture(t)

	var alice, bob MountInfo
	if s := doJSON(t, client, http.MethodPost, baseURL+"/v1/mounts",
		map[string]any{"agent_user_id": "agent-user-1"}, &alice); s != http.StatusCreated {
		t.Fatalf("alice 建挂载返回 %d", s)
	}
	if s := doJSON(t, client, http.MethodPost, baseURL+"/v1/mounts",
		map[string]any{"agent_user_id": "agent-user-2"}, &bob); s != http.StatusCreated {
		t.Fatalf("bob 建挂载返回 %d", s)
	}

	if alice.MountPoint == bob.MountPoint {
		t.Fatal("两个用户拿到了同一个挂载点")
	}
	if alice.UserID == bob.UserID {
		t.Error("两个用户解析到了同一个 ZimaOS uid")
	}

	assertDirNames(t, "alice", alice.MountPoint, []string{"Documents", "Shared"})
	assertDirNames(t, "bob", bob.MountPoint, []string{"Media", "Shared"})
}

func TestControl_HealthReportsMountCount(t *testing.T) {
	client, baseURL, _ := newControlFixture(t)

	var health struct {
		Status string `json:"status"`
		Mounts int    `json:"mounts"`
	}
	if s := doJSON(t, client, http.MethodGet, baseURL+"/healthz", nil, &health); s != http.StatusOK {
		t.Fatalf("健康检查返回 %d", s)
	}
	if health.Status != "ok" || health.Mounts != 0 {
		t.Errorf("健康检查返回 %+v，期望 status=ok mounts=0", health)
	}
}

func TestControl_MalformedRequestIsRejected(t *testing.T) {
	client, baseURL, _ := newControlFixture(t)

	resp, err := client.Post(baseURL+"/v1/mounts", "application/json", bytes.NewBufferString("{not json"))
	if err != nil {
		t.Fatalf("发请求: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("畸形请求体返回 %d，期望 400", resp.StatusCode)
	}

	var missing errorResponse
	if s := doJSON(t, client, http.MethodPost, baseURL+"/v1/mounts",
		map[string]any{}, &missing); s != http.StatusBadRequest {
		t.Errorf("缺 agent_user_id 返回 %d，期望 400", s)
	}
}

// doJSON issues a request and decodes the response, returning the status code.
func doJSON(t *testing.T, client *http.Client, method, url string, body any, out any) int {
	t.Helper()

	var reader *bytes.Buffer
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求: %v", err)
		}
		reader = bytes.NewBuffer(data)
	} else {
		reader = bytes.NewBuffer(nil)
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("构造请求: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()

	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("解码 %s %s 的响应: %v", method, url, err)
		}
	}
	return resp.StatusCode
}

// 保证 socket 只对属主可读写——挂载请求等同于投射某人的授权，不能对本机所有用户开放。
func TestControl_SocketIsOwnerOnly(t *testing.T) {
	requireFUSE(t)
	_, resolver, provider := twoUserFixture(t)

	mounts, err := NewMountSet(MountSetConfig{Root: filepath.Join(t.TempDir(), "mnt")}, resolver, provider)
	if err != nil {
		t.Fatalf("建 MountSet: %v", err)
	}
	defer func() { _ = mounts.Close() }()

	socketPath := filepath.Join(t.TempDir(), "toolfsd.sock")
	server := NewControlServer(mounts, socketPath)
	if err := server.Listen(); err != nil {
		t.Fatalf("监听: %v", err)
	}
	defer func() { _ = server.Close() }()

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket 权限为 %o，期望 600", perm)
	}
}
