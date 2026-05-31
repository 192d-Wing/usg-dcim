// Pure renderers — port of Python's services/dhcp_push.py:85-210
// (the `_render_*` family + render_kea_subnet4/6). No DB calls, no
// network calls; the bundle endpoint preloads everything and feeds
// it in.
package bundle

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// RenderPools projects a pool list ([{first,last}, …]) onto Kea's
// shape ([{pool: "first - last"}, …]). Entries missing either
// endpoint are dropped silently — they would error at Kea load time
// anyway and the bundle shouldn't ship them. Matches
// services/dhcp_push.py:_render_pools.
func RenderPools(raw json.RawMessage) []map[string]any {
	out := []map[string]any{}
	if len(raw) == 0 {
		return out
	}
	var pools []map[string]any
	if err := json.Unmarshal(raw, &pools); err != nil {
		return out
	}
	for _, p := range pools {
		first, _ := p["first"].(string)
		last, _ := p["last"].(string)
		if first == "" || last == "" {
			continue
		}
		out = append(out, map[string]any{"pool": first + " - " + last})
	}
	return out
}

// RenderPDPools projects a pd-pool list ([{prefix,"delegated_len":N}, …])
// onto Kea's shape ([{prefix, prefix-len, delegated-len}, …]). The
// {prefix,len} input is split on "/" — entries without a "/" or
// with a zero delegated_len are dropped. Matches
// services/dhcp_push.py:_render_pd_pools.
func RenderPDPools(raw json.RawMessage) []map[string]any {
	out := []map[string]any{}
	if len(raw) == 0 {
		return out
	}
	var pools []map[string]any
	if err := json.Unmarshal(raw, &pools); err != nil {
		return out
	}
	for _, p := range pools {
		prefix := stringOf(p["prefix"])
		delegatedLen := intOf(p["delegated_len"])
		if prefix == "" || delegatedLen == 0 {
			continue
		}
		addr, plenStr, ok := strings.Cut(prefix, "/")
		if !ok || plenStr == "" {
			continue
		}
		plen, err := strconv.Atoi(plenStr)
		if err != nil {
			continue
		}
		out = append(out, map[string]any{
			"prefix":        addr,
			"prefix-len":    plen,
			"delegated-len": delegatedLen,
		})
	}
	return out
}

// RenderOptions projects a Kea option-data list. Pass-through with
// type normalization: data is always a string, code (if set) is an
// int, name/space pass verbatim when truthy. Matches
// services/dhcp_push.py:_render_options.
func RenderOptions(raw json.RawMessage) []map[string]any {
	out := []map[string]any{}
	if len(raw) == 0 {
		return out
	}
	var opts []map[string]any
	if err := json.Unmarshal(raw, &opts); err != nil {
		return out
	}
	for _, o := range opts {
		entry := map[string]any{"data": stringOf(o["data"])}
		if name := stringOf(o["name"]); name != "" {
			entry["name"] = name
		}
		if code, ok := o["code"]; ok && code != nil {
			entry["code"] = intOf(code)
		}
		if space := stringOf(o["space"]); space != "" {
			entry["space"] = space
		}
		out = append(out, entry)
	}
	return out
}

// RenderReservationsV4 projects DCIM-side reservation entries
// ({mac, ip, hostname?}) onto Kea v4's reservation shape
// ({hw-address, ip-address, hostname?}). Entries missing either
// MAC or IP are dropped. Matches
// services/dhcp_push.py:_render_reservations_v4.
func RenderReservationsV4(raw json.RawMessage) []map[string]any {
	out := []map[string]any{}
	if len(raw) == 0 {
		return out
	}
	var rs []map[string]any
	if err := json.Unmarshal(raw, &rs); err != nil {
		return out
	}
	for _, r := range rs {
		mac := stringOf(r["mac"])
		ip := stringOf(r["ip"])
		if mac == "" || ip == "" {
			continue
		}
		entry := map[string]any{"hw-address": mac, "ip-address": ip}
		if h := stringOf(r["hostname"]); h != "" {
			entry["hostname"] = h
		}
		out = append(out, entry)
	}
	return out
}

// RenderReservationsV6 projects DCIM-side v6 reservations
// ({duid, ip, hostname?}) onto Kea v6's shape ({duid, ip-addresses,
// hostname?}). DCIM exposes a single ip per reservation; Kea v6
// expects a list, so each entry wraps its ip in a single-element
// array. Matches services/dhcp_push.py:_render_reservations_v6.
func RenderReservationsV6(raw json.RawMessage) []map[string]any {
	out := []map[string]any{}
	if len(raw) == 0 {
		return out
	}
	var rs []map[string]any
	if err := json.Unmarshal(raw, &rs); err != nil {
		return out
	}
	for _, r := range rs {
		duid := stringOf(r["duid"])
		ip := stringOf(r["ip"])
		if duid == "" || ip == "" {
			continue
		}
		entry := map[string]any{"duid": duid, "ip-addresses": []any{ip}}
		if h := stringOf(r["hostname"]); h != "" {
			entry["hostname"] = h
		}
		out = append(out, entry)
	}
	return out
}

// RenderKeaSubnet4 projects a v4 scope (possibly template-merged via
// MergeTemplateIntoScope) onto Kea's subnet4 object. The renderer
// stays duck-typed on the field set so a future Scope shape with
// extra columns doesn't need a second renderer. Matches
// services/dhcp_push.py:render_kea_subnet4 (line 169).
func RenderKeaSubnet4(s Scope, keaID int64) map[string]any {
	out := map[string]any{
		"id":             keaID,
		"subnet":         s.Prefix,
		"pools":          RenderPools(s.PoolsJSON),
		"option-data":    RenderOptions(s.OptionsJSON),
		"reservations":   RenderReservationsV4(s.ReservationsJSON),
		"valid-lifetime": defaultIfNil(s.ValidLifetimeSeconds, DefaultValidLifetime),
	}
	if s.RenewTimerSeconds != nil {
		out["renew-timer"] = *s.RenewTimerSeconds
	}
	if s.RebindTimerSeconds != nil {
		out["rebind-timer"] = *s.RebindTimerSeconds
	}
	return out
}

// RenderKeaSubnet6 projects a v6 scope onto Kea's subnet6 object.
// Matches services/dhcp_push.py:render_kea_subnet6 (line 189),
// including the v6-only preferred-lifetime + pd-pools handling.
func RenderKeaSubnet6(s Scope, keaID int64) map[string]any {
	out := map[string]any{
		"id":             keaID,
		"subnet":         s.Prefix,
		"pools":          RenderPools(s.PoolsJSON),
		"option-data":    RenderOptions(s.OptionsJSON),
		"reservations":   RenderReservationsV6(s.ReservationsJSON),
		"valid-lifetime": defaultIfNil(s.ValidLifetimeSeconds, DefaultValidLifetime),
	}
	if s.PreferredLifetimeSeconds != nil {
		out["preferred-lifetime"] = *s.PreferredLifetimeSeconds
	}
	if s.RenewTimerSeconds != nil {
		out["renew-timer"] = *s.RenewTimerSeconds
	}
	if s.RebindTimerSeconds != nil {
		out["rebind-timer"] = *s.RebindTimerSeconds
	}
	pd := RenderPDPools(s.PdPoolsJSON)
	if len(pd) > 0 {
		out["pd-pools"] = pd
	}
	return out
}

// ---- helpers ----

func stringOf(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}

func intOf(v any) int64 {
	if v == nil {
		return 0
	}
	switch t := v.(type) {
	case int:
		return int64(t)
	case int64:
		return t
	case float64:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	}
	return 0
}

func defaultIfNil(p *int64, def int64) int64 {
	if p == nil {
		return def
	}
	return *p
}
