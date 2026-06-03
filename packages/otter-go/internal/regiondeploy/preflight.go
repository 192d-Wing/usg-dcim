// Pre-flight checks ported from Python's dcim.regiondeploy.preflight.
// Same key strings, same labels, same order — finch persists "which
// checks were dismissed" by key, so renaming or reordering breaks
// previously-saved wizard state. Only the seven pure checks land here;
// external (network-hitting) checks register from their owning modules
// when those land on otter-go.
package regiondeploy

import (
	"encoding/json"
	"strings"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// preflightOutcome mirrors Python's CheckOutcome. fix_hint is the
// operator-facing remediation string; the UI renders it next to the
// failed check. Nil hint serializes as JSON null (matching Python's
// `fix_hint: str | None = None`).
type preflightOutcome struct {
	Key     string  `json:"key"`
	Label   string  `json:"label"`
	Passed  bool    `json:"passed"`
	FixHint *string `json:"fix_hint"`
}

// preflightResponse mirrors Python's PreflightResponse: ready is the
// hard-gate boolean (true iff every check passed); the finch wizard's
// Start button binds to this value.
type preflightResponse struct {
	Ready  bool               `json:"ready"`
	Checks []preflightOutcome `json:"checks"`
}

// preflightContext is the Go equivalent of Python's preflight.Context —
// the per-deployment inputs the checks read. Nodes is the post-create
// roster (mac/hostname/role columns); config is the deployment's
// JSONB blob decoded into a generic map so the lb_pool_v6 / bgp_peers
// /etc. lookups don't need a Go struct mirror of the Pydantic schema.
type preflightContext struct {
	Nodes  []dbq.RegionDeploymentNode
	Config map[string]any
}

// runPreflight runs every registered check in registration order and
// returns the outcomes. Each check is a small function — same shape as
// Python's `Check.fn` callbacks. Adding a new check here keeps the wire
// shape stable; renaming a key does not (don't rename once shipped).
func runPreflight(ctx preflightContext) preflightResponse {
	checks := []struct {
		key, label string
		fn         func(preflightContext) (bool, string)
	}{
		{"nodes.distinct_macs", "Node MACs are unique within the deployment", checkDistinctMacs},
		{"nodes.distinct_hostnames", "Node hostnames are unique within the deployment", checkDistinctHostnames},
		{"nodes.has_control_plane", "At least one control_plane node is selected", checkHasControlPlane},
		{"site.has_v6_pod_prefix", "Site has IPv6 pod prefix allocated", checkConfigKey("pod_cidr_v6", "config.pod_cidr_v6 is unset (e.g. fd00:site:42:1000::/56)")},
		{"site.has_v6_svc_prefix", "Site has IPv6 service prefix allocated", checkConfigKey("svc_cidr_v6", "config.svc_cidr_v6 is unset")},
		{"site.has_v6_lb_pool", "Site has IPv6 LB pool allocated", checkConfigKey("lb_pool_v6", "config.lb_pool_v6 is unset (the v6 LB-IP pool Cilium advertises)")},
		{"bgp.peers_configured", "At least one BGP peer is configured", checkBgpPeersConfigured},
	}
	outcomes := make([]preflightOutcome, 0, len(checks))
	allPass := true
	for _, c := range checks {
		passed, hint := c.fn(ctx)
		o := preflightOutcome{Key: c.key, Label: c.label, Passed: passed}
		if !passed {
			h := hint
			o.FixHint = &h
			allPass = false
		}
		outcomes = append(outcomes, o)
	}
	return preflightResponse{Ready: allPass, Checks: outcomes}
}

// checkDistinctMacs walks the node list reporting the first duplicate
// MAC found. Python normalises case via .lower() — same here so that
// "AA:BB:CC:01:02:03" and "aa:bb:cc:01:02:03" collide. The hint names
// both hostnames so the operator can decide which to re-MAC.
func checkDistinctMacs(ctx preflightContext) (bool, string) {
	seen := map[string]string{}
	for _, n := range ctx.Nodes {
		mac := strings.ToLower(n.Mac)
		if prev, ok := seen[mac]; ok {
			return false, "MAC " + mac + " is assigned to both " + prev + " and " + n.Hostname
		}
		seen[mac] = n.Hostname
	}
	return true, ""
}

// checkDistinctHostnames catches the dual-hostname case Python flags
// for kubeadm uniqueness + Tinkerbell CR naming.
func checkDistinctHostnames(ctx preflightContext) (bool, string) {
	seen := map[string]struct{}{}
	for _, n := range ctx.Nodes {
		if _, ok := seen[n.Hostname]; ok {
			return false, "hostname " + n.Hostname + " appears more than once"
		}
		seen[n.Hostname] = struct{}{}
	}
	return true, ""
}

// checkHasControlPlane gates the kubeadm-init step: with no control
// plane, the orchestrator's `joining` stage has nowhere to attach.
func checkHasControlPlane(ctx preflightContext) (bool, string) {
	for _, n := range ctx.Nodes {
		if n.Role == "control_plane" {
			return true, ""
		}
	}
	return false, "no control_plane node selected; add at least one"
}

// checkConfigKey returns a check that reports a non-empty value for
// key in ctx.Config. Truthiness mirrors Python's `if not (ctx.config
// or {}).get(key)` — missing key, empty string, empty list, and false
// all fail.
func checkConfigKey(key, hint string) func(preflightContext) (bool, string) {
	return func(ctx preflightContext) (bool, string) {
		if isTruthy(ctx.Config[key]) {
			return true, ""
		}
		return false, hint
	}
}

func checkBgpPeersConfigured(ctx preflightContext) (bool, string) {
	peers, _ := ctx.Config["bgp_peers"].([]any)
	if len(peers) == 0 {
		return false, "config.bgp_peers is empty — Cilium needs at least one peer"
	}
	return true, ""
}

// isTruthy mirrors Python's `if not value:` falsy set for the value
// types JSONB can decode into Go via encoding/json: nil, "", false,
// 0, empty slice, empty map. Numbers come back as json.Number when
// the decoder is configured for it; we treat the unconfigured float64
// path here since the deployment config decoder is the default
// json.Decoder.
func isTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case bool:
		return t
	case float64:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		// json.Number etc. — treat as truthy unless explicitly handled
		// above. The seven shipped checks only query string/list keys.
		return true
	}
}

// decodeConfig parses the deployment.config JSONB blob into a generic
// map. An empty / nil blob decodes to nil (which preflight reads as
// "no keys set" → every site.* check fails — same as Python).
func decodeConfig(raw json.RawMessage) map[string]any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}
