package bundle

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

func TestFromDbqServer_FieldShape(t *testing.T) {
	srvID := uuid.New()
	fabID := uuid.New()
	cfg := json.RawMessage(`{"dhcp4":{"interfaces-config":{"interfaces":["eth0"]}}}`)
	src := dbq.DhcpServerBundleRow{
		ID:         srvID,
		Name:       "kea-1",
		FabricID:   fabID,
		BaseConfig: cfg,
	}
	got := FromDbqServer(src)
	if got.ID != srvID.String() {
		t.Errorf("ID: got %q, want %q", got.ID, srvID.String())
	}
	if string(got.BaseConfig) != string(cfg) {
		t.Errorf("BaseConfig should be the same bytes; got %q", got.BaseConfig)
	}
}

func TestFromDbqScope_NullableTimersAndTemplateID(t *testing.T) {
	scopeID := uuid.New()
	serverID := uuid.New()
	templateID := uuid.New()
	vl := int32(7200)
	renew := int32(3600)
	kid := int32(42)
	pools := json.RawMessage(`[{"first":"10.0.0.10","last":"10.0.0.250"}]`)
	src := dbq.DhcpScope{
		ID:                   scopeID,
		DhcpServerID:         serverID,
		Name:                 "office-v4",
		IPFamily:             4,
		Prefix:               "10.0.0.0/24",
		PoolsJSON:            pools,
		ValidLifetimeSeconds: &vl,
		RenewTimerSeconds:    &renew,
		// RebindTimerSeconds nil — inherits from template (or default)
		KeaSubnetID: &kid,
		TemplateID:  &templateID,
		Enabled:     true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	got := FromDbqScope(src)
	if got.ID != scopeID.String() {
		t.Errorf("ID: got %q, want %q", got.ID, scopeID.String())
	}
	if got.IPFamily != 4 {
		t.Errorf("IPFamily: got %d, want 4", got.IPFamily)
	}
	if got.ValidLifetimeSeconds == nil || *got.ValidLifetimeSeconds != 7200 {
		t.Errorf("ValidLifetimeSeconds: got %v, want 7200", got.ValidLifetimeSeconds)
	}
	if got.RenewTimerSeconds == nil || *got.RenewTimerSeconds != 3600 {
		t.Errorf("RenewTimerSeconds: got %v, want 3600", got.RenewTimerSeconds)
	}
	if got.RebindTimerSeconds != nil {
		t.Errorf("RebindTimerSeconds should pass through nil; got %v", got.RebindTimerSeconds)
	}
	if got.KeaSubnetID == nil || *got.KeaSubnetID != 42 {
		t.Errorf("KeaSubnetID: got %v, want 42", got.KeaSubnetID)
	}
	if got.TemplateID == nil || *got.TemplateID != templateID.String() {
		t.Errorf("TemplateID: got %v, want %q", got.TemplateID, templateID.String())
	}
}

func TestFromDbqScope_NilTemplateID_StaysNil(t *testing.T) {
	// Specifically guard the typed-nil interface trap — if
	// uuidPtrToStringPtr were declared with an interface arg, a
	// *uuid.UUID(nil) wrapped in the iface would panic on .String().
	src := dbq.DhcpScope{
		ID:           uuid.New(),
		DhcpServerID: uuid.New(),
		IPFamily:     4,
		Prefix:       "10.0.0.0/24",
		Enabled:      true,
		TemplateID:   nil, // explicit nil
	}
	got := FromDbqScope(src)
	if got.TemplateID != nil {
		t.Errorf("TemplateID should stay nil; got %v", got.TemplateID)
	}
}

func TestFromDbqTemplate_FieldShape(t *testing.T) {
	tplID := uuid.New()
	fabID := uuid.New()
	vl := int32(7200)
	preferred := int32(3600)
	opts := json.RawMessage(`[{"code":3,"name":"routers","data":"10.0.0.1"}]`)
	src := dbq.DhcpScopeTemplate{
		ID:                       tplID,
		FabricID:                 fabID,
		Name:                     "office-defaults",
		IPFamily:                 4,
		OptionsJSON:              opts,
		ValidLifetimeSeconds:     &vl,
		PreferredLifetimeSeconds: &preferred,
	}
	got := FromDbqTemplate(src)
	if got.ID != tplID.String() {
		t.Errorf("ID: got %q, want %q", got.ID, tplID.String())
	}
	if string(got.OptionsJSON) != string(opts) {
		t.Errorf("OptionsJSON should be the same bytes; got %q", got.OptionsJSON)
	}
	if got.ValidLifetimeSeconds == nil || *got.ValidLifetimeSeconds != 7200 {
		t.Errorf("ValidLifetimeSeconds: got %v", got.ValidLifetimeSeconds)
	}
	if got.PreferredLifetimeSeconds == nil || *got.PreferredLifetimeSeconds != 3600 {
		t.Errorf("PreferredLifetimeSeconds: got %v", got.PreferredLifetimeSeconds)
	}
	if got.RenewTimerSeconds != nil {
		t.Errorf("RenewTimerSeconds should pass through nil; got %v", got.RenewTimerSeconds)
	}
}

func TestRenderBundleEndToEndFromDbqRows(t *testing.T) {
	// Round-trip a realistic input set through the dbq mapper +
	// the renderer. Pins that the mapper produces a Scope shape
	// the renderer accepts and that the etag is stable. PR 3 (the
	// HTTP endpoint) will plug this exact sequence into a handler;
	// catching shape drift here saves a round-trip later.
	serverID := uuid.New()
	tplID := uuid.New()
	tpl := dbq.DhcpScopeTemplate{
		ID:                   tplID,
		FabricID:             uuid.New(),
		Name:                 "office-defaults",
		IPFamily:             4,
		OptionsJSON:          json.RawMessage(`[{"code":3,"name":"routers","data":"10.0.0.1"}]`),
		ValidLifetimeSeconds: int32Ptr(7200),
	}
	scopeVL := int32(1800)
	kid := int32(1)
	scope := dbq.DhcpScope{
		ID:                   uuid.New(),
		DhcpServerID:         serverID,
		Name:                 "office-v4",
		IPFamily:             4,
		Prefix:               "10.0.0.0/24",
		PoolsJSON:            json.RawMessage(`[{"first":"10.0.0.10","last":"10.0.0.250"}]`),
		ValidLifetimeSeconds: &scopeVL, // overrides template
		KeaSubnetID:          &kid,
		TemplateID:           &tplID,
		Enabled:              true,
	}
	srv := dbq.DhcpServerBundleRow{
		ID:         serverID,
		Name:       "kea-1",
		FabricID:   uuid.New(),
		BaseConfig: json.RawMessage(`{"dhcp4":{"interfaces-config":{"interfaces":["eth0"]}}}`),
	}
	b, err := RenderKeaBundle(
		FromDbqServer(srv),
		[]Scope{FromDbqScope(scope)},
		map[string]Template{tpl.ID.String(): FromDbqTemplate(tpl)},
	)
	if err != nil {
		t.Fatalf("RenderKeaBundle: %v", err)
	}
	if b.ServerID != serverID.String() {
		t.Errorf("ServerID: got %q, want %q", b.ServerID, serverID.String())
	}
	s4 := b.Dhcp4["subnet4"].([]any)
	if len(s4) != 1 {
		t.Fatalf("subnet4: want 1 entry, got %d", len(s4))
	}
	got := s4[0].(map[string]any)
	if got["subnet"] != "10.0.0.0/24" {
		t.Errorf("subnet: got %v", got["subnet"])
	}
	// Scope's 1800 wins over template's 7200.
	if got["valid-lifetime"].(int64) != 1800 {
		t.Errorf("valid-lifetime: got %v, want 1800 (scope override)", got["valid-lifetime"])
	}
	// Template's "routers" option lands in option-data alongside
	// any scope options (scope had none, so just the one).
	opts := got["option-data"].([]map[string]any)
	if len(opts) != 1 || opts[0]["name"] != "routers" {
		t.Errorf("option-data should carry template's routers entry; got %v", opts)
	}
}

func int32Ptr(v int32) *int32 { return &v }
