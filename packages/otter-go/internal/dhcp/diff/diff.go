// Package diff is the Go port of Python's diff_scope orchestrator
// at services/dhcp_push.py:837-931. Compares what DCIM would render
// against what Kea currently has on disk, returning one of five
// terminal states:
//
//   - never_pushed     — scope has no kea_subnet_id; nothing to diff
//   - missing_from_kea — DCIM has an id but Kea returns result=3
//   - in_sync          — DCIM == Kea on every authored field
//   - drifted          — delta dict says what changed
//   - error            — transport failure or unexpected Kea response
//
// `missing_from_kea` is the cue to call push.PushScope; `drifted`
// surfaces a per-key delta the operator decides what to do with.
//
// Pure helpers (Normalize, DiffSubnetObjects, ExtractKeaSubnet) are
// exported because the bulk-diff endpoint in a follow-up PR runs them
// against pre-loaded scope+server sets without re-orchestrating the
// load. PersistDiffState writes back via the Querier, mirroring
// services/dhcp_push.py:934 persist_diff_state.
package diff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/bundle"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/kea"
)

// Status enumerates the five terminal states. Strings are byte-
// identical to Python's so audit log rows + UI labels travel
// through the migration unchanged.
type Status string

const (
	StatusNeverPushed    Status = "never_pushed"
	StatusMissingFromKea Status = "missing_from_kea"
	StatusInSync         Status = "in_sync"
	StatusDrifted        Status = "drifted"
	StatusError          Status = "error"
)

// unexpectedShapeFmt is the prefix the diff orchestrator emits when
// Kea's response shape isn't parseable. Truncated to 1024 bytes
// downstream so log readers see a useful tail instead of an arbitrary
// dump. Centralized so sonar's S1192 duplicate-literal check doesn't
// flag the three call sites that share it.
const unexpectedShapeFmt = "unexpected Kea response: %s"

// listFields are the keys compared as multisets — operators may have
// re-ordered without changing semantics. Matches services/dhcp_push.py:770.
var listFields = map[string]struct{}{
	"pools":        {},
	"pd-pools":     {},
	"option-data":  {},
	"reservations": {},
}

// Querier is the slim DB surface diff needs. *dbq.Queries
// satisfies it. The HTTP handler in a follow-up PR composes this
// with the larger ipam.Querier via embedding.
type Querier interface {
	GetDhcpScopeForPush(ctx context.Context, id uuid.UUID) (dbq.DhcpScopeForPushRow, error)
	GetDhcpServerForPush(ctx context.Context, id uuid.UUID) (dbq.DhcpServerForPushRow, error)
	GetDhcpScopeTemplateForPush(ctx context.Context, id uuid.UUID) (dbq.DhcpScopeTemplate, error)
	WriteDhcpScopeDiffState(ctx context.Context, arg dbq.WriteDhcpScopeDiffStateParams) error
}

// KeaClient is the slim view of *kea.Client diff uses — just the
// two subnet-get methods. Defined here (not imported from push) so
// the diff package has no compile-time dependency on push.
type KeaClient interface {
	Subnet4Get(ctx context.Context, subnetID int64) ([]byte, error)
	Subnet6Get(ctx context.Context, subnetID int64) ([]byte, error)
}

// KeaClientBuilder mirrors push.KeaClientBuilder. Production wires
// kea.New(...); tests inject a fake.
type KeaClientBuilder func(server dbq.DhcpServerForPushRow) KeaClient

// DefaultKeaClientBuilder wires production *kea.Client instances.
// Same shape as push.DefaultKeaClientBuilder; not shared because
// the KeaClient interface here is a strict subset.
func DefaultKeaClientBuilder(server dbq.DhcpServerForPushRow) KeaClient {
	user, pass := "", ""
	if server.AuthUsername != nil {
		user = *server.AuthUsername
	}
	if server.AuthPassword != nil {
		pass = *server.AuthPassword
	}
	return kea.New(server.KeaURL, user, pass)
}

// Result is the per-call return shape. Mirrors Python's DiffResult
// dataclass (services/dhcp_push.py:756). DCIMSubnet is populated
// for every status except NeverPushed (we render before calling
// Kea even on the error paths so the operator can see what DCIM
// would have shipped). KeaSubnet is populated only when Kea
// returned a parseable subnet object.
type Result struct {
	ScopeID     uuid.UUID
	KeaSubnetID *int32
	Status      Status
	DCIMSubnet  map[string]any
	KeaSubnet   map[string]any
	Delta       map[string]any // field-name → {"dcim": v, "kea": v}
	Error       string
}

// DiffScope is the orchestrator entry point. Loads the scope +
// server + (optional) template, renders the DCIM-side subnet,
// pulls the Kea-side subnet via Subnet{4,6}Get, computes the delta.
// Returns Result with one of five Status values; non-nil error is
// returned only for unexpected internal failures (DB unreachable,
// template fetch errored). Pre-call refusals and Kea transport
// failures surface as Result.Status="error".
func DiffScope(ctx context.Context, q Querier, build KeaClientBuilder, scopeID uuid.UUID) (Result, error) {
	scope, err := q.GetDhcpScopeForPush(ctx, scopeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{ScopeID: scopeID, Status: StatusError, Error: "scope not found"}, nil
		}
		return Result{}, fmt.Errorf("load scope: %w", err)
	}
	// Never-pushed short-circuit — no Kea state to compare against.
	if scope.KeaSubnetID == nil {
		return Result{ScopeID: scope.ID, Status: StatusNeverPushed}, nil
	}
	server, err := q.GetDhcpServerForPush(ctx, scope.DhcpServerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{ScopeID: scopeID, KeaSubnetID: scope.KeaSubnetID,
				Status: StatusError, Error: "parent dhcp server not found"}, nil
		}
		return Result{}, fmt.Errorf("load server: %w", err)
	}
	tpl, err := loadTemplate(ctx, q, scope.TemplateID)
	if err != nil {
		return Result{}, fmt.Errorf("load template: %w", err)
	}
	dcimSubnet := renderDCIMSubnet(scope, tpl)
	client := build(server)
	rawResp, rpcErr := fetchKeaSubnet(ctx, client, scope.IPFamily, int64(*scope.KeaSubnetID))
	if rpcErr != nil {
		return Result{
			ScopeID: scope.ID, KeaSubnetID: scope.KeaSubnetID,
			Status: StatusError, DCIMSubnet: dcimSubnet,
			Error: fmt.Sprintf("transport_error: %s", rpcErr.Error()),
		}, nil
	}
	keaSubnet, parseStatus, parseErr := ExtractKeaSubnet(rawResp, scope.IPFamily)
	if keaSubnet == nil {
		return Result{
			ScopeID: scope.ID, KeaSubnetID: scope.KeaSubnetID,
			Status: parseStatus, DCIMSubnet: dcimSubnet, Error: parseErr,
		}, nil
	}
	delta := DiffSubnetObjects(dcimSubnet, keaSubnet)
	status := StatusInSync
	if len(delta) > 0 {
		status = StatusDrifted
	}
	return Result{
		ScopeID: scope.ID, KeaSubnetID: scope.KeaSubnetID,
		Status: status, DCIMSubnet: dcimSubnet, KeaSubnet: keaSubnet, Delta: delta,
	}, nil
}

func renderDCIMSubnet(scope dbq.DhcpScopeForPushRow, tpl *dbq.DhcpScopeTemplate) map[string]any {
	bundleScope := bundle.FromDbqScopeForPush(scope)
	var bundleTpl *bundle.Template
	if tpl != nil {
		t := bundle.FromDbqTemplate(*tpl)
		bundleTpl = &t
	}
	effective := bundle.MergeTemplateIntoScope(bundleScope, bundleTpl)
	keaID := int64(*scope.KeaSubnetID)
	if scope.IPFamily == 4 {
		return bundle.RenderKeaSubnet4(effective, keaID)
	}
	return bundle.RenderKeaSubnet6(effective, keaID)
}

func fetchKeaSubnet(ctx context.Context, client KeaClient, ipFamily int32, subnetID int64) ([]byte, error) {
	if ipFamily == 4 {
		return client.Subnet4Get(ctx, subnetID)
	}
	return client.Subnet6Get(ctx, subnetID)
}

func loadTemplate(ctx context.Context, q Querier, templateID *uuid.UUID) (*dbq.DhcpScopeTemplate, error) {
	if templateID == nil {
		return nil, nil
	}
	tpl, err := q.GetDhcpScopeTemplateForPush(ctx, *templateID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &tpl, nil
}

// ExtractKeaSubnet plucks the subnet object out of Kea's
// subnet{4,6}-get response. Returns (nil, status, error) when the
// shape isn't a parseable subnet:
//   - status=missing_from_kea when result=3 (the operator-visible
//     "Kea forgot about us" case)
//   - status=error for malformed responses (truncates the offender
//     for log readability)
//
// Mirrors services/dhcp_push.py:815 _extract_kea_subnet exactly.
func ExtractKeaSubnet(raw []byte, ipFamily int32) (map[string]any, Status, string) {
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, StatusError, fmt.Sprintf("unexpected Kea response shape: %s", truncate(raw, 1024))
	}
	if len(entries) == 0 {
		return nil, StatusError, "unexpected Kea response: empty list"
	}
	first := entries[0]
	if code, ok := numericResultCode(first["result"]); ok && code == 3 {
		return nil, StatusMissingFromKea, ""
	}
	args, ok := first["arguments"].(map[string]any)
	if !ok {
		return nil, StatusError, fmt.Sprintf(unexpectedShapeFmt, truncate(raw, 1024))
	}
	listKey := "subnet4"
	if ipFamily == 6 {
		listKey = "subnet6"
	}
	subnets, ok := args[listKey].([]any)
	if !ok || len(subnets) == 0 {
		return nil, StatusError, fmt.Sprintf(unexpectedShapeFmt, truncate(raw, 1024))
	}
	subnet, ok := subnets[0].(map[string]any)
	if !ok {
		return nil, StatusError, fmt.Sprintf(unexpectedShapeFmt, truncate(raw, 1024))
	}
	return subnet, "", ""
}

// DiffSubnetObjects returns the per-key delta between DCIM's
// rendered subnet and Kea's reported subnet. Only keys DCIM
// authored appear in the delta — Kea-added fields (timestamps,
// internal counters, defaulted options) are ignored. A key
// present in DCIM but missing in Kea is reported as
// {"dcim": X, "kea": nil}. Empty return = no drift.
//
// List-shaped fields (pools, pd-pools, option-data, reservations)
// are compared as multisets so a reordered list with the same
// elements doesn't flag as drift. Numeric values are normalized
// before comparison so DCIM's int64-from-render and Kea's
// float64-from-JSON-unmarshal compare equal — Python's `==` does
// this implicitly; Go's reflect.DeepEqual needs the explicit
// coercion. Matches Python's behavior at
// services/dhcp_push.py:793 _diff_subnet_objects.
func DiffSubnetObjects(dcim, kea map[string]any) map[string]any {
	delta := map[string]any{}
	for key, dcimVal := range dcim {
		keaVal := kea[key]
		if _, isList := listFields[key]; isList {
			if !equalAsMultiset(dcimVal, keaVal) {
				delta[key] = map[string]any{"dcim": dcimVal, "kea": keaVal}
			}
			continue
		}
		if !valuesEqual(dcimVal, keaVal) {
			delta[key] = map[string]any{"dcim": dcimVal, "kea": keaVal}
		}
	}
	return delta
}

// valuesEqual compares two values with Python's `==` semantics:
// numeric types of different Go kinds (int64 vs float64) compare
// equal when they hold the same value. Pure DCIM renders use int64
// (from the typed Scope fields); Kea responses arrive through
// json.Unmarshal which yields float64 for every number. Without
// this normalization every numeric field flags as drift.
func valuesEqual(a, b any) bool {
	if na, ok := asFloat64(a); ok {
		if nb, ok := asFloat64(b); ok {
			return na == nb
		}
	}
	return reflect.DeepEqual(a, b)
}

// asFloat64 returns (n, true) for every Go numeric kind. json.Number
// is included so callers that opt into UseNumber decoding round-trip
// through diff cleanly even though the default Unmarshal path uses
// float64.
func asFloat64(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

// equalAsMultiset returns true when a and b are the same list-shaped
// values regardless of order. Recursively normalizes nested maps
// (by sorted-key tuples) and lists (by sorted normalized elements)
// to a comparable form. Both nil and empty list compare equal so a
// Kea-omitted optional list matches a DCIM-rendered empty one (the
// `[] or []` coercion Python does at services/dhcp_push.py:808).
// Numeric coercion (int64 ↔ float64) flows through the recursive
// Normalize so list-of-dict comparisons also normalize numbers.
func equalAsMultiset(a, b any) bool {
	if isEmptyList(a) && isEmptyList(b) {
		return true
	}
	na := Normalize(a)
	nb := Normalize(b)
	return reflect.DeepEqual(na, nb)
}

// isEmptyList returns true for nil, []any{}, []map[string]any{}, or
// any other empty slice. Python treats `None`, `[]`, and `pools_json or []`
// uniformly as empty for the purposes of diff; mirror that here so
// a Kea-omitted optional list matches a DCIM-rendered empty list.
func isEmptyList(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Slice && rv.Len() == 0
}

// Normalize recursively converts a value into a stable comparable
// form: nested maps become string keyed maps with sorted-string
// keys (compared via reflect.DeepEqual); lists become sorted slices
// of normalized elements. Numeric values normalize to float64 so
// DCIM's int64 and Kea's float64 of the same number compare equal.
// Mirrors Python's _normalize_for_diff at services/dhcp_push.py:773
// — translated to Go's lack of frozen dicts/tuples by serializing
// to JSON and sorting the JSON string (which is itself a comparable
// form and gives the same multiset semantics).
func Normalize(value any) any {
	if f, ok := asFloat64(value); ok {
		return f
	}
	switch v := value.(type) {
	case nil:
		return nil
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([][2]any, 0, len(keys))
		for _, k := range keys {
			out = append(out, [2]any{k, Normalize(v[k])})
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			b, _ := json.Marshal(Normalize(item))
			out = append(out, string(b))
		}
		sort.Strings(out)
		return out
	case []map[string]any:
		// Specifically for the renderer's []map[string]any (pools etc.).
		out := make([]string, 0, len(v))
		for _, item := range v {
			b, _ := json.Marshal(Normalize(map[string]any(item)))
			out = append(out, string(b))
		}
		sort.Strings(out)
		return out
	default:
		return v
	}
}

// PersistDiffState writes a Result's status + delta into the scope
// row's last_diff_* columns. Mirrors persist_diff_state at
// services/dhcp_push.py:934. delta_json carries a value ONLY on
// status='drifted' — every other terminal state clears it so a
// stale delta doesn't mislead operators reading the LIST endpoint.
func PersistDiffState(ctx context.Context, q Querier, r Result) error {
	var deltaJSON json.RawMessage
	if r.Status == StatusDrifted && len(r.Delta) > 0 {
		b, err := json.Marshal(r.Delta)
		if err != nil {
			return fmt.Errorf("marshal delta: %w", err)
		}
		deltaJSON = b
	}
	return q.WriteDhcpScopeDiffState(ctx, dbq.WriteDhcpScopeDiffStateParams{
		ID:                r.ScopeID,
		LastDiffStatus:    string(r.Status),
		LastDiffDeltaJSON: deltaJSON,
	})
}

// numericResultCode mirrors kea.numericResultCode but is duplicated
// here so the diff package has no internal dependency on kea —
// only the two Subnet*Get methods of *kea.Client.
func numericResultCode(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n), true
		}
	}
	return 0, false
}

func truncate(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
}

// Silence "imported and not used" if time falls out of a future
// refactor; the package itself doesn't reference it today.
var _ = time.Now
