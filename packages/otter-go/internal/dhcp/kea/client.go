// Package kea is the Go port of Python's services/kea.py KeaClient
// (PR 74 in the original DHCP push work). The package wraps the
// Kea Control Agent's JSON-RPC over HTTP — POST to the control
// agent's port with `{command, service: [...], arguments?: {...}}`
// and get back `[{result: N, text: "..."}]` per service.
//
// This is PR 1 of the multi-PR DHCP push port. Pure plumbing: the
// client knows how to talk to Kea and how to map Kea's tri-state
// response codes (0=ok, 1=err, 2=unsupported, 3=empty) onto a
// status string. The orchestration (push_scope + diff_scope +
// delete_scope_from_kea), the DB writes, the bulk endpoints, and
// the cron jobs land in later PRs that compose this client.
//
// Surface mirrors services/kea.py:115 (KeaClient class) and
// services/dhcp_push.py:353 (_interpret_kea_response):
//
//   - ListLeases4 / ListLeases6 — for the dhcp_sync cron port
//   - Subnet4Add / Subnet4Update / Subnet4Del / Subnet4Get
//   - Subnet6Add / Subnet6Update / Subnet6Del / Subnet6Get
//   - ConfigWrite — persist running config to disk after a mutation
//   - InterpretResponse — tri-state parse of the response list
//
// Tests run against an httptest.Server so no real Kea is required;
// the request shape + the response parsing are both pinned.
package kea

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a thin wrapper around the Kea Control Agent's HTTP API.
// Zero-value isn't useful — construct via New.
type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client
}

// New returns a Client that talks to the Kea Control Agent at
// baseURL (no trailing slash required — it's stripped). Pass empty
// username/password for an unauthenticated CA. The default timeout
// is 30s, matching Python's httpx.AsyncClient(timeout=30.0) at
// services/kea.py:131.
func New(baseURL, username, password string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

// WithHTTPClient swaps the underlying *http.Client. Tests use it to
// point at httptest.Server.Client(); production rarely needs it.
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	c.http = h
	return c
}

// post is the single point where every command goes through. The
// body shape is `{command, service, arguments?}` — Kea returns a
// list of per-service responses on success and the same list with
// non-zero result codes on partial/total failure. All callers parse
// the returned []byte themselves so they can decide whether to
// surface the raw shape (push.History records the raw response for
// audit) or just the interpreted tri-state.
func (c *Client) post(ctx context.Context, command string, services []string, arguments any) ([]byte, error) {
	body := map[string]any{
		"command": command,
		"service": services,
	}
	if arguments != nil {
		body["arguments"] = arguments
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal kea request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("build kea request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.username != "" && c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call kea %q: %w", command, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read kea response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return out, fmt.Errorf("kea HTTP %d for %q: %s", resp.StatusCode, command, truncate(out, 512))
	}
	return out, nil
}

// ---- lease commands ----

// ListLeases4 returns Kea's `lease4-get-all` response (per-service
// list of leases). Caller drills into `[0].arguments.leases` if a
// slice of just-the-leases is desired; this preserves the raw shape
// so error codes per service are surfaceable.
func (c *Client) ListLeases4(ctx context.Context) ([]byte, error) {
	return c.post(ctx, "lease4-get-all", []string{"dhcp4"}, nil)
}

func (c *Client) ListLeases6(ctx context.Context) ([]byte, error) {
	return c.post(ctx, "lease6-get-all", []string{"dhcp6"}, nil)
}

// ---- subnet commands (push) ----
// All four subnetN-{add,update,del,get} commands require Kea's
// `subnet_cmds` hook library loaded on the target server (ships with
// Kea ISC Premium / OSS-from-source builds; verify with `config-get`
// → look for libdhcp_subnet_cmds.so in hooks-libraries). InterpretResponse
// surfaces a `result=2` (unsupported) as a distinct status so
// operators can wire a friendly "hook not loaded" error rather than
// generic "kea call failed."

// Subnet4Add posts `subnet4-add` with `arguments.subnet4: [subnet]`.
// The `subnet` map is the Kea-shape subnet object (already rendered
// from a DhcpScope via internal/dhcp/bundle.RenderKeaSubnet4 in
// PR #216). A nil map is rejected at the boundary so a bug in the
// caller doesn't ship `{subnet4:[null]}` to Kea — Kea would respond
// with result=1 'invalid subnet definition' and the operator would
// chase a phantom remote failure for a programmer mistake.
func (c *Client) Subnet4Add(ctx context.Context, subnet map[string]any) ([]byte, error) {
	if subnet == nil {
		return nil, errors.New("kea: Subnet4Add subnet is nil")
	}
	return c.post(ctx, "subnet4-add", []string{"dhcp4"}, map[string]any{"subnet4": []any{subnet}})
}

func (c *Client) Subnet4Update(ctx context.Context, subnet map[string]any) ([]byte, error) {
	if subnet == nil {
		return nil, errors.New("kea: Subnet4Update subnet is nil")
	}
	return c.post(ctx, "subnet4-update", []string{"dhcp4"}, map[string]any{"subnet4": []any{subnet}})
}

func (c *Client) Subnet4Del(ctx context.Context, subnetID int64) ([]byte, error) {
	return c.post(ctx, "subnet4-del", []string{"dhcp4"}, map[string]any{"id": subnetID})
}

// Subnet4Get fetches the live subnet4 object from Kea. Response
// carries the full subnet definition under `arguments.subnet4[0]`;
// result=3 means the subnet isn't in Kea even though DCIM has a
// kea_subnet_id for it (drifted away). PR 75's diff_scope uses this
// to compute drift; will be ported in a later PR.
func (c *Client) Subnet4Get(ctx context.Context, subnetID int64) ([]byte, error) {
	return c.post(ctx, "subnet4-get", []string{"dhcp4"}, map[string]any{"id": subnetID})
}

func (c *Client) Subnet6Add(ctx context.Context, subnet map[string]any) ([]byte, error) {
	if subnet == nil {
		return nil, errors.New("kea: Subnet6Add subnet is nil")
	}
	return c.post(ctx, "subnet6-add", []string{"dhcp6"}, map[string]any{"subnet6": []any{subnet}})
}

func (c *Client) Subnet6Update(ctx context.Context, subnet map[string]any) ([]byte, error) {
	if subnet == nil {
		return nil, errors.New("kea: Subnet6Update subnet is nil")
	}
	return c.post(ctx, "subnet6-update", []string{"dhcp6"}, map[string]any{"subnet6": []any{subnet}})
}

func (c *Client) Subnet6Del(ctx context.Context, subnetID int64) ([]byte, error) {
	return c.post(ctx, "subnet6-del", []string{"dhcp6"}, map[string]any{"id": subnetID})
}

func (c *Client) Subnet6Get(ctx context.Context, subnetID int64) ([]byte, error) {
	return c.post(ctx, "subnet6-get", []string{"dhcp6"}, map[string]any{"id": subnetID})
}

// ConfigWrite persists Kea's running config to disk so the change
// survives a Kea restart. The push orchestrator (later PR) calls
// this after a successful subnetN-add/update so the change isn't
// volatile. `services` is the list of Kea daemons to write — usually
// `{"dhcp4"}` or `{"dhcp6"}` matching whichever subnetN command just
// ran; a multi-service push can pass both at once.
func (c *Client) ConfigWrite(ctx context.Context, services []string) ([]byte, error) {
	return c.post(ctx, "config-write", services, nil)
}

// ---- response shape helpers ----

// ErrBadResponseShape is returned by InterpretResponse when Kea's
// response is not the expected JSON list of per-service dicts.
// Surfaces to the operator as "unexpected Kea response shape" with
// the offending bytes truncated for log readability.
var ErrBadResponseShape = errors.New("unexpected kea response shape")

func truncate(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
}
