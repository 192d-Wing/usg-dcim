package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// mountWithDefault is a variant of mount() that lets the test specify
// the Handler.DefaultDnsRecursiveUpstreams field — needed because the
// system-DNS endpoints surface it as `default_recursive_upstreams` and
// asserting it requires a known value rather than the empty slice
// mount() ships with.
func mountWithDefault(f *fakeQ, def []string) http.Handler {
	r := chi.NewRouter()
	(&Handler{
		Q:                            f,
		Audit:                        audit.Recorder(noopAudit{}),
		DefaultDnsRecursiveUpstreams: def,
	}).Mount(r)
	return r
}

func TestGetSystemDnsSettings_NoOverride(t *testing.T) {
	def := []string{"1.1.1.1", "8.8.8.8"}
	rec := doReq(t, mountWithDefault(&fakeQ{}, def), "GET", "/admin/system/dns-settings", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out systemDnsSettingsOut
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.RecursiveUpstreams, def) {
		t.Errorf("effective = %v, want %v", out.RecursiveUpstreams, def)
	}
	if !reflect.DeepEqual(out.DefaultRecursiveUpstreams, def) {
		t.Errorf("default = %v, want %v", out.DefaultRecursiveUpstreams, def)
	}
	if out.OverrideActive {
		t.Error("override_active should be false when no row")
	}
	if out.UpdatedAt != nil {
		t.Error("updated_at should be nil when no row")
	}
}

func TestGetSystemDnsSettings_WithOverride(t *testing.T) {
	def := []string{"1.1.1.1"}
	override := []string{"10.0.0.53", "10.0.0.54"}
	ts := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	blob, _ := json.Marshal(override)
	f := &fakeQ{
		sysGet: func(key string) (dbq.SystemSetting, error) {
			if key != "dns_recursive_upstreams" {
				t.Errorf("queried wrong key: %q", key)
			}
			return dbq.SystemSetting{Key: key, Value: blob, UpdatedAt: ts}, nil
		},
	}
	rec := doReq(t, mountWithDefault(f, def), "GET", "/admin/system/dns-settings", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out systemDnsSettingsOut
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.RecursiveUpstreams, override) {
		t.Errorf("effective = %v, want %v", out.RecursiveUpstreams, override)
	}
	if !out.OverrideActive {
		t.Error("override_active should be true")
	}
	if out.UpdatedAt == nil || !out.UpdatedAt.Equal(ts) {
		t.Errorf("updated_at = %v, want %v", out.UpdatedAt, ts)
	}
}

// An override row whose value is an empty list falls through to the env
// default — Python parity for the "operator wrote []" stale-row case.
func TestGetSystemDnsSettings_EmptyListRowFallsBack(t *testing.T) {
	def := []string{"1.1.1.1"}
	blob, _ := json.Marshal([]string{})
	f := &fakeQ{
		sysGet: func(_ string) (dbq.SystemSetting, error) {
			return dbq.SystemSetting{Value: blob, UpdatedAt: time.Now()}, nil
		},
	}
	rec := doReq(t, mountWithDefault(f, def), "GET", "/admin/system/dns-settings", nil)
	var out systemDnsSettingsOut
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if out.OverrideActive {
		t.Error("empty-list row should NOT count as an active override")
	}
	if !reflect.DeepEqual(out.RecursiveUpstreams, def) {
		t.Errorf("should fall back to default; got %v", out.RecursiveUpstreams)
	}
}

// PUT with a non-empty list upserts and audits .update.
func TestPutSystemDnsSettings_Upserts(t *testing.T) {
	def := []string{"1.1.1.1"}
	f := &fakeQ{}
	body := `{"recursive_upstreams": ["10.0.0.53", " 10.0.0.54 ", "10.0.0.53"]}`
	rec := doReq(t, mountWithDefault(f, def), "PUT", "/admin/system/dns-settings", []byte(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if f.sysUpsertedKey != "dns_recursive_upstreams" {
		t.Errorf("upserted wrong key: %q", f.sysUpsertedKey)
	}
	var stored []string
	if err := json.Unmarshal(f.sysUpsertedValue, &stored); err != nil {
		t.Fatalf("upserted value should be json: %v", err)
	}
	// Dedup + trim + preserve order. Python parity.
	want := []string{"10.0.0.53", "10.0.0.54"}
	if !reflect.DeepEqual(stored, want) {
		t.Errorf("stored = %v, want %v (deduped+trimmed)", stored, want)
	}
	if f.sysDeletedKey != "" {
		t.Error("upsert path should not call DeleteSystemSetting")
	}
}

// PUT with `null` clears the override and audits .reset.
func TestPutSystemDnsSettings_NullResets(t *testing.T) {
	def := []string{"1.1.1.1"}
	f := &fakeQ{}
	rec := doReq(t, mountWithDefault(f, def), "PUT", "/admin/system/dns-settings",
		[]byte(`{"recursive_upstreams": null}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.sysDeletedKey != "dns_recursive_upstreams" {
		t.Errorf("delete should run; deletedKey = %q", f.sysDeletedKey)
	}
	if f.sysUpsertedKey != "" {
		t.Error("reset path should not call UpsertSystemSetting")
	}
	var out systemDnsSettingsOut
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if out.OverrideActive {
		t.Error("override_active should be false after reset")
	}
	if !reflect.DeepEqual(out.RecursiveUpstreams, def) {
		t.Errorf("should fall back to default; got %v", out.RecursiveUpstreams)
	}
}

// PUT with an empty list ALSO clears the override — both null and []
// collapse to "clear" via normalizeUpstreams (Python parity).
func TestPutSystemDnsSettings_EmptyListResets(t *testing.T) {
	f := &fakeQ{}
	rec := doReq(t, mountWithDefault(f, []string{"1.1.1.1"}), "PUT", "/admin/system/dns-settings",
		[]byte(`{"recursive_upstreams": []}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.sysDeletedKey != "dns_recursive_upstreams" {
		t.Errorf("[] should clear override; deletedKey = %q", f.sysDeletedKey)
	}
}

// PUT with only-whitespace entries ALSO clears (collapses to []).
func TestPutSystemDnsSettings_WhitespaceOnlyResets(t *testing.T) {
	f := &fakeQ{}
	rec := doReq(t, mountWithDefault(f, []string{"1.1.1.1"}), "PUT", "/admin/system/dns-settings",
		[]byte(`{"recursive_upstreams": ["", " ", "\t"]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.sysDeletedKey != "dns_recursive_upstreams" {
		t.Error("all-whitespace input should clear override")
	}
}

func TestPutSystemDnsSettings_BadJson(t *testing.T) {
	rec := doReq(t, mountWithDefault(&fakeQ{}, nil), "PUT", "/admin/system/dns-settings",
		[]byte("not-json"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// Cap-gate: GET requires admin:system-settings:read.
func TestGetSystemDnsSettings_RejectsWithoutCap(t *testing.T) {
	r := chi.NewRouter()
	(&Handler{
		Q:     &fakeQ{},
		Audit: audit.Recorder(noopAudit{}),
	}).Mount(r)
	req := httptest.NewRequest("GET", "/admin/system/dns-settings", nil)
	// Principal carries an unrelated cap, NOT system-settings:read.
	p := auth.Principal{Capabilities: []string{"admin:users:read"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// Cap-gate: PUT requires admin:system-settings:update.
func TestPutSystemDnsSettings_RejectsWithoutCap(t *testing.T) {
	r := chi.NewRouter()
	(&Handler{
		Q:     &fakeQ{},
		Audit: audit.Recorder(noopAudit{}),
	}).Mount(r)
	req := httptest.NewRequest("PUT", "/admin/system/dns-settings", nil)
	// system-settings:read does NOT grant :update — independent action.
	p := auth.Principal{Capabilities: []string{"admin:system-settings:read"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// normalizeUpstreams: nil-in, nil-out — direct unit test that pins the
// "clear override" signal collapsing.
func TestNormalizeUpstreams_NilStaysNil(t *testing.T) {
	if got := normalizeUpstreams(nil); got != nil {
		t.Errorf("nil → %v, want nil", got)
	}
}

func TestNormalizeUpstreams_DedupsAndTrims(t *testing.T) {
	got := normalizeUpstreams([]string{" a ", "b", "a", "", "c "})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// silence "context unused" if test pkg moves around.
var _ = context.Background
