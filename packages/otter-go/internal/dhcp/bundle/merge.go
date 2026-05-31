// Template merge — port of Python's
// services/dhcp_push.py:_option_key, _merge_options,
// merge_template_into_scope (PR 78). Pure functions; no DB calls.
package bundle

import (
	"encoding/json"
)

// optionKey returns a stable identity tuple for a Kea option-data
// entry. Code wins when present (wire ID Kea actually keys on);
// name is the fallback. The (code | name, space) pair handles
// vendor-options with separate code spaces.
// Mirrors services/dhcp_push.py:_option_key.
type optionKey struct {
	UseCode bool
	Code    int64
	Name    string
	Space   string
}

func keyOf(opt map[string]any) optionKey {
	space := stringOf(opt["space"])
	if c, ok := opt["code"]; ok && c != nil {
		return optionKey{UseCode: true, Code: intOf(c), Space: space}
	}
	return optionKey{UseCode: false, Name: stringOf(opt["name"]), Space: space}
}

// MergeOptions returns the union of template options + scope options,
// where scope entries win on conflicting keys and new scope entries
// append. Template entries come first (in template order) so
// operators reading the Kea config dump see template defaults at the
// top of the option-data array — easier to review. Matches
// services/dhcp_push.py:_merge_options.
func MergeOptions(templateOpts, scopeOpts json.RawMessage) json.RawMessage {
	var tList, sList []map[string]any
	if len(templateOpts) > 0 {
		_ = json.Unmarshal(templateOpts, &tList)
	}
	if len(scopeOpts) > 0 {
		_ = json.Unmarshal(scopeOpts, &sList)
	}
	byKey := make(map[optionKey]map[string]any, len(tList)+len(sList))
	order := make([]optionKey, 0, len(tList)+len(sList))
	for _, o := range tList {
		k := keyOf(o)
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = o
	}
	for _, o := range sList {
		k := keyOf(o)
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = o // scope overrides
	}
	merged := make([]map[string]any, 0, len(order))
	for _, k := range order {
		merged = append(merged, byKey[k])
	}
	out, _ := json.Marshal(merged)
	return out
}

// MergeTemplateIntoScope returns the effective scope the renderer
// should consume. With template==nil this is a value-copy of scope
// (callers don't need to branch). With a template:
//
//   - Timers (valid_lifetime / renew / rebind / preferred_lifetime):
//     scope value wins when non-nil; otherwise inherit template.
//   - OptionsJSON: merged by (code | name, space) via MergeOptions —
//     scope entries override template entries with the same key, new
//     scope entries append.
//   - Everything else (prefix, pools, pd_pools, reservations, ID,
//     IPFamily, DhcpServerID, KeaSubnetID, Enabled): from scope.
//
// Mirrors services/dhcp_push.py:merge_template_into_scope (line 249).
func MergeTemplateIntoScope(scope Scope, template *Template) Scope {
	if template == nil {
		// Value-copy. Same renderer-input shape, no template inheritance.
		return scope
	}
	out := scope
	out.OptionsJSON = MergeOptions(template.OptionsJSON, scope.OptionsJSON)
	if out.ValidLifetimeSeconds == nil {
		out.ValidLifetimeSeconds = template.ValidLifetimeSeconds
	}
	if out.RenewTimerSeconds == nil {
		out.RenewTimerSeconds = template.RenewTimerSeconds
	}
	if out.RebindTimerSeconds == nil {
		out.RebindTimerSeconds = template.RebindTimerSeconds
	}
	if out.PreferredLifetimeSeconds == nil {
		out.PreferredLifetimeSeconds = template.PreferredLifetimeSeconds
	}
	return out
}
