package kea

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureServer is the all-purpose Kea stub: records the inbound
// request body + method + auth header, replays a programmed response.
// Tests instantiate one per case so the captured state stays scoped.
type captureServer struct {
	t           *testing.T
	gotBody     map[string]any
	gotMethod   string
	gotPath     string
	gotAuth     string
	respStatus  int
	respBody    string
}

func newCaptureServer(t *testing.T) (*captureServer, *httptest.Server) {
	t.Helper()
	cs := &captureServer{t: t, respStatus: 200, respBody: `[{"result":0,"text":"ok"}]`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.gotMethod = r.Method
		cs.gotPath = r.URL.Path
		cs.gotAuth = r.Header.Get("Authorization")
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &cs.gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cs.respStatus)
		_, _ = io.WriteString(w, cs.respBody)
	}))
	t.Cleanup(srv.Close)
	return cs, srv
}

func newTestClient(t *testing.T, srv *httptest.Server, user, pass string) *Client {
	t.Helper()
	c := New(srv.URL, user, pass)
	return c.WithHTTPClient(srv.Client())
}

func TestPost_ShapeAndMethod(t *testing.T) {
	cs, srv := newCaptureServer(t)
	c := newTestClient(t, srv, "", "")
	if _, err := c.Subnet4Add(context.Background(), map[string]any{"id": 7, "subnet": "10.0.0.0/24"}); err != nil {
		t.Fatalf("Subnet4Add: %v", err)
	}
	if cs.gotMethod != http.MethodPost {
		t.Errorf("method: got %q, want POST", cs.gotMethod)
	}
	if cs.gotPath != "/" {
		t.Errorf("path: got %q, want /", cs.gotPath)
	}
	if cs.gotBody["command"] != "subnet4-add" {
		t.Errorf("command: got %v, want subnet4-add", cs.gotBody["command"])
	}
	svc, _ := cs.gotBody["service"].([]any)
	if len(svc) != 1 || svc[0] != "dhcp4" {
		t.Errorf("service: got %v, want [dhcp4]", svc)
	}
	args, _ := cs.gotBody["arguments"].(map[string]any)
	s4, _ := args["subnet4"].([]any)
	if len(s4) != 1 {
		t.Fatalf("arguments.subnet4 should hold one entry, got %v", args["subnet4"])
	}
	if s4[0].(map[string]any)["subnet"] != "10.0.0.0/24" {
		t.Errorf("subnet payload mistransmitted: %v", s4[0])
	}
}

func TestPost_BasicAuthWhenCredsSet(t *testing.T) {
	cs, srv := newCaptureServer(t)
	c := newTestClient(t, srv, "kea-user", "secret")
	_, _ = c.ConfigWrite(context.Background(), []string{"dhcp4"})
	if !strings.HasPrefix(cs.gotAuth, "Basic ") {
		t.Errorf("Authorization header missing or wrong: %q", cs.gotAuth)
	}
}

func TestPost_NoAuthHeaderWhenCredsEmpty(t *testing.T) {
	cs, srv := newCaptureServer(t)
	c := newTestClient(t, srv, "", "")
	_, _ = c.ConfigWrite(context.Background(), []string{"dhcp4"})
	if cs.gotAuth != "" {
		t.Errorf("Authorization header should be absent; got %q", cs.gotAuth)
	}
}

func TestPost_TrailingSlashStrippedFromBaseURL(t *testing.T) {
	cs, srv := newCaptureServer(t)
	// Reconstruct the client with a base URL that ends in `/`. The
	// .post helper appends `/`, so without trim we'd POST to `//`.
	c := New(srv.URL+"/", "", "").WithHTTPClient(srv.Client())
	_, _ = c.Subnet4Get(context.Background(), 1)
	if cs.gotPath != "/" {
		t.Errorf("path: got %q, want / (no double-slash)", cs.gotPath)
	}
}

func TestPost_HTTPErrorSurfacedToCaller(t *testing.T) {
	cs, srv := newCaptureServer(t)
	cs.respStatus = http.StatusBadGateway
	cs.respBody = `kea control agent down`
	c := newTestClient(t, srv, "", "")
	// Use a non-nil subnet so we exercise the HTTP-error path
	// rather than the boundary nil-guard on Subnet4Add.
	_, err := c.Subnet4Add(context.Background(), map[string]any{"id": 1})
	if err == nil {
		t.Fatal("expected error for HTTP 502")
	}
	if !strings.Contains(err.Error(), "kea HTTP 502") {
		t.Errorf("error should mention status code; got %v", err)
	}
}

func TestPost_ContextCancellationCancelsRequest(t *testing.T) {
	_, srv := newCaptureServer(t)
	c := newTestClient(t, srv, "", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	_, err := c.ListLeases4(ctx)
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
}

// ---- per-command request shape ----

func TestSubnet4DelSendsIDInArguments(t *testing.T) {
	cs, srv := newCaptureServer(t)
	c := newTestClient(t, srv, "", "")
	_, _ = c.Subnet4Del(context.Background(), 42)
	args, _ := cs.gotBody["arguments"].(map[string]any)
	// JSON unmarshal yields float64 for numbers.
	if id, _ := args["id"].(float64); int64(id) != 42 {
		t.Errorf("arguments.id: got %v, want 42", args["id"])
	}
	if cs.gotBody["command"] != "subnet4-del" {
		t.Errorf("command: got %v, want subnet4-del", cs.gotBody["command"])
	}
}

func TestSubnet6CommandsTargetDhcp6Service(t *testing.T) {
	cs, srv := newCaptureServer(t)
	c := newTestClient(t, srv, "", "")
	for _, fn := range []func(context.Context) ([]byte, error){
		func(ctx context.Context) ([]byte, error) { return c.Subnet6Add(ctx, map[string]any{"id": 1}) },
		func(ctx context.Context) ([]byte, error) { return c.Subnet6Update(ctx, map[string]any{"id": 1}) },
		func(ctx context.Context) ([]byte, error) { return c.Subnet6Del(ctx, 1) },
		func(ctx context.Context) ([]byte, error) { return c.Subnet6Get(ctx, 1) },
	} {
		_, _ = fn(context.Background())
		svc, _ := cs.gotBody["service"].([]any)
		if len(svc) != 1 || svc[0] != "dhcp6" {
			t.Errorf("v6 command should target [dhcp6]; got %v for command %v", svc, cs.gotBody["command"])
		}
	}
}

func TestListLeases4UsesLeaseGetAll(t *testing.T) {
	cs, srv := newCaptureServer(t)
	c := newTestClient(t, srv, "", "")
	_, _ = c.ListLeases4(context.Background())
	if cs.gotBody["command"] != "lease4-get-all" {
		t.Errorf("lease command: got %v, want lease4-get-all", cs.gotBody["command"])
	}
	// Lease list takes no arguments.
	if _, hasArgs := cs.gotBody["arguments"]; hasArgs {
		t.Errorf("lease4-get-all should have no arguments field; got %v", cs.gotBody["arguments"])
	}
}

func TestConfigWritePassesAllServicesInOneCall(t *testing.T) {
	cs, srv := newCaptureServer(t)
	c := newTestClient(t, srv, "", "")
	_, _ = c.ConfigWrite(context.Background(), []string{"dhcp4", "dhcp6"})
	if cs.gotBody["command"] != "config-write" {
		t.Errorf("command: got %v, want config-write", cs.gotBody["command"])
	}
	svc, _ := cs.gotBody["service"].([]any)
	if len(svc) != 2 || svc[0] != "dhcp4" || svc[1] != "dhcp6" {
		t.Errorf("service: got %v, want [dhcp4 dhcp6]", svc)
	}
}
