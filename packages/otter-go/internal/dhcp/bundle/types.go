// Package bundle is the Go port of Python's services/dhcp_bundle.py
// (PR 76 + the PR 78 template-merge extension). It assembles a full
// Kea config bundle for a DhcpServer by overlaying DCIM-rendered
// subnet4/subnet6 arrays onto an operator-authored base config
// stored in DhcpServer.base_config.
//
// This file declares the input shapes the renderer reads. They are
// package-local on purpose: db/generated/models.go doesn't carry
// DhcpScope or DhcpScopeTemplate yet (the cron port in PR #212 wrote
// against the dhcp_scopes table directly without a model struct
// because it only needed a DELETE). When the SQL queries land in a
// follow-up PR, the dbq.DhcpScope row will be mapped to bundle.Scope
// at the call site so the renderer stays decoupled from the DB type.
//
// The shapes intentionally mirror the Python columns 1:1 (camelCase
// → snake_case → CamelCase on Go) so a future maintainer cross-
// referencing services/dhcp_push.py:169 sees the same field names.
//
// Pointers vs values: timer fields are *int64 because the renderer's
// behavior splits on "set vs unset" — Python uses None for unset,
// and the bundle output suppresses the matching Kea key when nil.
package bundle

import "encoding/json"

// Scope is the per-subnet input shape. ip_family=4 or 6 picks the
// renderer; the renderer that consumes it also reads only the subset
// of fields it needs (e.g. PdPoolsJSON is v6-only).
type Scope struct {
	ID                       string
	DhcpServerID             string
	IPFamily                 int
	Prefix                   string
	PoolsJSON                json.RawMessage
	PdPoolsJSON              json.RawMessage
	OptionsJSON              json.RawMessage
	ReservationsJSON         json.RawMessage
	ValidLifetimeSeconds     *int64
	RenewTimerSeconds        *int64
	RebindTimerSeconds       *int64
	PreferredLifetimeSeconds *int64
	TemplateID               *string
	KeaSubnetID              *int64
	Enabled                  bool
}

// Template carries the defaults a Scope inherits when its own value
// for a timer is nil, plus an OptionsJSON list whose entries the
// scope can override per option-key.
type Template struct {
	ID                       string
	OptionsJSON              json.RawMessage
	ValidLifetimeSeconds     *int64
	RenewTimerSeconds        *int64
	RebindTimerSeconds       *int64
	PreferredLifetimeSeconds *int64
}

// Server carries the operator-authored base config that DCIM
// overlays subnet arrays onto. BaseConfig is the raw JSON blob
// stored on the DhcpServer row — the renderer parses it once,
// extracts ctrl-agent / dhcp4 / dhcp6 sections, replaces subnet4 /
// subnet6 with the DCIM-rendered arrays, and recomputes the etag.
type Server struct {
	ID         string
	BaseConfig json.RawMessage
}

// KeaBundle is the rendered output, identical in shape to Python's
// services.dhcp_bundle.KeaBundle so the puller on the dhcp-site
// chart parses both servers' responses with the same code.
type KeaBundle struct {
	ServerID  string                 `json:"server_id"`
	CtrlAgent map[string]any         `json:"ctrl_agent"`
	Dhcp4     map[string]any         `json:"dhcp4"`
	Dhcp6     map[string]any         `json:"dhcp6"`
	Etag      string                 `json:"etag"`
}

// DefaultValidLifetime mirrors Python's _DEFAULT_VALID_LIFETIME at
// services/dhcp_push.py:166 — the renderer fallback when both the
// scope's and the merged template's valid_lifetime_seconds are nil.
// 3600 (not Kea's stock 7200) matches the column default DhcpScope
// used pre-PR 78.
const DefaultValidLifetime int64 = 3600
