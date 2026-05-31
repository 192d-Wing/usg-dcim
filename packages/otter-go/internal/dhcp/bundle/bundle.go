// Bundle assembler — port of Python's services/dhcp_bundle.py
// (the public render_kea_bundle entrypoint + the _assemble_subnets,
// _overlay_subnets, _compute_etag, _next_id helpers).
package bundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// RenderKeaBundle builds the full bundle for one server. Pure — no
// DB calls. Callers preload the scopes that belong to the server +
// any DhcpScopeTemplate rows referenced by those scopes; the bundle
// endpoint can fetch all three in one round-trip.
//
// `templatesByID==nil` is treated as "no templates" (scopes render
// with their stored values), matching Python's default.
//
// Disabled scopes are skipped entirely — they shouldn't appear in
// the running Kea config. Disabled servers still render normally;
// the bundle endpoint can refuse on its own if needed.
//
// Etag is the SHA256 of the canonical JSON serialization of the
// three rendered sections (ctrl-agent + dhcp4 + dhcp6) together.
// sort_keys=True equivalent is achieved by Go's encoding/json
// sorting map keys alphabetically — same digest as Python's
// json.dumps(..., sort_keys=True).
func RenderKeaBundle(server Server, scopes []Scope, templatesByID map[string]Template) (KeaBundle, error) {
	// Parse the operator-authored base config. Treat unset / null /
	// invalid as empty objects so the renderer is robust on a server
	// with no base_config yet — matches Python's dict(... or {}) idiom.
	var base map[string]any
	if len(server.BaseConfig) > 0 {
		_ = json.Unmarshal(server.BaseConfig, &base)
	}
	if base == nil {
		base = map[string]any{}
	}
	ctrlAgent := sectionOf(base, "ctrl-agent")
	dhcp4Base := sectionOf(base, "dhcp4")
	dhcp6Base := sectionOf(base, "dhcp6")

	subnet4, subnet6 := assembleSubnets(scopes, templatesByID)

	// Overlay the DCIM-rendered subnet arrays onto the operator's
	// dhcp4/dhcp6 sections. Anything else in those sections passes
	// through verbatim; if the operator's section already carries
	// a subnet4/subnet6 array, the DCIM-rendered one wins (DCIM is
	// the source of truth for subnet objects on a managed server).
	dhcp4 := copyMap(dhcp4Base)
	dhcp4["subnet4"] = anyOf(subnet4)
	dhcp6 := copyMap(dhcp6Base)
	dhcp6["subnet6"] = anyOf(subnet6)

	etag, err := computeEtag(ctrlAgent, dhcp4, dhcp6)
	if err != nil {
		return KeaBundle{}, err
	}
	return KeaBundle{
		ServerID:  server.ID,
		CtrlAgent: ctrlAgent,
		Dhcp4:     dhcp4,
		Dhcp6:     dhcp6,
		Etag:      etag,
	}, nil
}

// assembleSubnets renders every enabled scope into Kea subnet4/
// subnet6 objects. Two-pass: first pass claims every pinned
// kea_subnet_id so allocations don't collide; second pass fills in
// unpushed scopes (KeaSubnetID==nil) with bundle-local ids starting
// from 1. The DB-side allocation lives in push_scope; this is
// bundle-internal only. Mirrors
// services/dhcp_bundle.py:_assemble_subnets.
func assembleSubnets(scopes []Scope, templatesByID map[string]Template) ([]map[string]any, []map[string]any) {
	pinned4, deferred4, pinned6, deferred6 := partitionScopes(scopes)
	subnet4, used4 := renderPinned(pinned4, templatesByID, RenderKeaSubnet4)
	subnet6, used6 := renderPinned(pinned6, templatesByID, RenderKeaSubnet6)
	subnet4 = appendDeferred(subnet4, deferred4, used4, templatesByID, RenderKeaSubnet4)
	subnet6 = appendDeferred(subnet6, deferred6, used6, templatesByID, RenderKeaSubnet6)
	return subnet4, subnet6
}

// partitionScopes splits the enabled scopes into pinned (KeaSubnetID
// set) and deferred (KeaSubnetID == nil) per family. Disabled scopes
// are dropped entirely — they don't appear in the running Kea config.
func partitionScopes(scopes []Scope) (pinned4, deferred4, pinned6, deferred6 []Scope) {
	for _, s := range scopes {
		if !s.Enabled {
			continue
		}
		switch {
		case s.IPFamily == 4 && s.KeaSubnetID != nil:
			pinned4 = append(pinned4, s)
		case s.IPFamily == 4:
			deferred4 = append(deferred4, s)
		case s.KeaSubnetID != nil:
			pinned6 = append(pinned6, s)
		default:
			deferred6 = append(deferred6, s)
		}
	}
	return
}

// renderPinned walks pinned scopes (KeaSubnetID set) and returns the
// rendered subnet list plus the set of consumed ids — the second
// pass uses that set to avoid collisions when allocating ids for
// deferred scopes.
func renderPinned(
	scopes []Scope, templatesByID map[string]Template,
	render func(Scope, int64) map[string]any,
) ([]map[string]any, map[int64]struct{}) {
	out := []map[string]any{}
	used := map[int64]struct{}{}
	for _, s := range scopes {
		used[*s.KeaSubnetID] = struct{}{}
		out = append(out, render(effectiveScope(s, templatesByID), *s.KeaSubnetID))
	}
	return out, used
}

// appendDeferred walks unpushed scopes (KeaSubnetID == nil) and
// appends them to the pinned-rendered slice using bundle-local ids
// starting from the lowest free positive int.
func appendDeferred(
	out []map[string]any, scopes []Scope, used map[int64]struct{},
	templatesByID map[string]Template, render func(Scope, int64) map[string]any,
) []map[string]any {
	for _, s := range scopes {
		kid := nextID(used)
		used[kid] = struct{}{}
		out = append(out, render(effectiveScope(s, templatesByID), kid))
	}
	return out
}

// effectiveScope returns the template-merged scope a renderer should
// consume. Missing template (no TemplateID, no entry in the map, or
// nil map) yields a scope identical to the input — the renderer
// branches uniformly.
func effectiveScope(s Scope, templatesByID map[string]Template) Scope {
	if s.TemplateID == nil || templatesByID == nil {
		return MergeTemplateIntoScope(s, nil)
	}
	tpl, ok := templatesByID[*s.TemplateID]
	if !ok {
		return MergeTemplateIntoScope(s, nil)
	}
	return MergeTemplateIntoScope(s, &tpl)
}

// nextID returns the lowest free positive int64 not in `used`.
// Matches services/dhcp_bundle.py:_next_id — Kea reserves id=0 as
// "unspecified" in some commands so we start at 1.
func nextID(used map[int64]struct{}) int64 {
	candidate := int64(1)
	for {
		if _, taken := used[candidate]; !taken {
			return candidate
		}
		candidate++
	}
}

// computeEtag — SHA256 of the canonical JSON serialization of the
// three rendered sections together. Matches Python's
// json.dumps({...}, sort_keys=True, separators=(",", ":")):
//
//   - Map keys: Go's encoding/json sorts alphabetically by default
//     (matches sort_keys=True).
//   - Separators: encoder default is compact (matches (",",":")).
//   - HTML escaping: Go DEFAULT-escapes "<", ">", "&" to "<"
//     etc., but Python's json.dumps does not escape them by default.
//     SetEscapeHTML(false) suppresses the divergence so operator-
//     authored strings carrying "&" (hook-library URLs with query
//     strings are the realistic case) hash the same in both ports.
//     Without this, a Python→Go cutover causes every dhcp-site
//     puller to see a spurious etag flap for any base_config with
//     such a string — bundle bytes wouldn't have actually changed,
//     but the puller treats the etag delta as a reload signal.
//
// Encoder.Encode appends a trailing '\n'; TrimRight strips it
// before hashing so the digest matches the no-trailing-newline
// output of Python's json.dumps.
func computeEtag(ctrlAgent, dhcp4, dhcp6 map[string]any) (string, error) {
	canonical := map[string]any{
		"ctrl-agent": ctrlAgent,
		"dhcp4":      dhcp4,
		"dhcp6":      dhcp6,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(canonical); err != nil {
		return "", err
	}
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// sectionOf returns base[name] as a map, or an empty map if absent
// / non-map. The returned map is a SHALLOW copy of base[name] so
// the caller can safely overwrite top-level keys (the renderer
// replaces "subnet4"/"subnet6") without aliasing the parsed base.
// Nested values inside the returned map remain shared with the
// parsed base — mutating bundle.Dhcp4["interfaces-config"][...] is
// NOT safe. The renderer only overwrites top-level keys; downstream
// consumers must follow the same discipline. Mirrors Python's
// `dict(base.get(key) or {})` idiom in render_kea_bundle.
func sectionOf(base map[string]any, name string) map[string]any {
	v, ok := base[name]
	if !ok || v == nil {
		return map[string]any{}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return copyMap(m)
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// anyOf re-types []map[string]any to []any so downstream type
// assertions (`bundle.Dhcp4["subnet4"].([]any)`) match the shape
// produced by a json.Unmarshal into map[string]any. JSON marshaling
// itself doesn't need this — []map[string]any marshals identically.
func anyOf(s []map[string]any) []any {
	out := make([]any, len(s))
	for i, m := range s {
		out[i] = m
	}
	return out
}
