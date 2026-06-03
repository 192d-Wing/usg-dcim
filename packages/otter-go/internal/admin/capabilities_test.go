package admin

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestCapabilitiesCatalog_HappyPath asserts the wire shape of the catalog
// endpoint: a non-empty domain → resource → actions map plus the
// specialties dict. We don't pin every key value here — capabilities.go
// is the source of truth and TestCapabilitiesCatalog_PythonParity below
// catches drops.
func TestCapabilitiesCatalog_HappyPath(t *testing.T) {
	rec := doReq(t, mount(&fakeQ{}), "GET", "/admin/capabilities/catalog", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out capabilityCatalogOut
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Catalog) == 0 {
		t.Fatal("catalog should be non-empty")
	}
	if len(out.Specialties) == 0 {
		t.Fatal("specialties should be non-empty")
	}
}

// TestCapabilitiesCatalog_PythonParity is the regression guard that keeps
// the hand-maintained Go catalog in lock-step with Python's. Every domain
// the Python catalog declared must exist here too — otherwise the
// role-create picker would silently drop a domain when finch fetches the
// Go endpoint instead of the Python one.
//
// The list comes from packages/otter/src/dcim/security/capabilities.py:28
// at the time of the cutover. When a new domain is added Python-side, add
// it here too; this test will fail loudly until both sides agree.
func TestCapabilitiesCatalog_PythonParity(t *testing.T) {
	required := []string{
		"inventory", "routing", "search", "ipam", "dns", "collectors",
		"alerts", "telemetry", "dashboards", "maintenance", "power",
		"audit", "admin", "notifications", "lir",
	}
	for _, domain := range required {
		if _, ok := capabilityCatalog[domain]; !ok {
			t.Errorf("missing domain %q — Python catalog declares it", domain)
		}
	}
	// SPECIALTY codes are the two-segment exceptions; without them the
	// role picker can't grant power:control / power:approve and
	// power-control workflows break.
	for _, code := range []string{"power:control", "power:approve"} {
		if _, ok := specialtyCapabilities[code]; !ok {
			t.Errorf("missing specialty %q", code)
		}
	}
}

// TestCapabilitiesCatalog_AdminSystemSettings ensures the catalog
// advertises the read+update actions for the system-settings resource
// (admin:system-settings:{read,update}). Both gates the new GET + PUT
// endpoints rely on, and a regression that drops them would silently
// take the role picker off-spec.
func TestCapabilitiesCatalog_AdminSystemSettings(t *testing.T) {
	admin, ok := capabilityCatalog["admin"]
	if !ok {
		t.Fatal("admin domain missing")
	}
	actions, ok := admin["system-settings"]
	if !ok {
		t.Fatal("admin.system-settings missing")
	}
	have := map[string]bool{}
	for _, a := range actions {
		have[a] = true
	}
	if !have["read"] || !have["update"] {
		t.Errorf("system-settings should have read+update; got %v", actions)
	}
}
