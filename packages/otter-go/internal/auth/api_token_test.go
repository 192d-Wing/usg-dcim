// Tests for API-token capability validation (UX-debt batch): a
// wildcard admin can issue granular tokens, non-wildcard callers still
// can't escalate, and malformed codes 400 instead of being persisted.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

func TestValidTokenCapability(t *testing.T) {
	valid := []string{
		"*",                    // bare global wildcard
		"inventory:sites:read", // catalog shape
		"admin:api-tokens:create",
		"power:control", // two-segment specialty
		"dns:*:read",    // wildcard segment
		"inventory:sites:*",
	}
	for _, c := range valid {
		if !validTokenCapability(c) {
			t.Errorf("validTokenCapability(%q) = false, want true", c)
		}
	}
	invalid := []string{
		"",
		"inventory",             // one segment, not the bare *
		"a:b:c:d",               // too many segments
		"Inventory:Sites:Read",  // uppercase
		"inventory:sites:",      // trailing empty segment
		":sites:read",           // leading empty segment
		"inventory sites read",  // no colons
		"inventory:site s:read", // embedded space
		"dcim_x9",               // token plaintext, not a cap
	}
	for _, c := range invalid {
		if validTokenCapability(c) {
			t.Errorf("validTokenCapability(%q) = true, want false", c)
		}
	}
}

// captureCreateFakeQ records the CreateApiTokenParams the handler
// persists so tests can assert the stored permission_codes.
type captureCreateFakeQ struct {
	fakeQ
	got *dbq.CreateApiTokenParams
}

func (f *captureCreateFakeQ) CreateApiToken(_ context.Context, arg dbq.CreateApiTokenParams) (dbq.ApiToken, error) {
	f.got = &arg
	return dbq.ApiToken{
		ID: uuid.New(), Name: arg.Name, OwnerUserID: arg.OwnerUserID,
		TokenHash: arg.TokenHash, PermissionCodes: arg.PermissionCodes,
		ScopeJson: arg.ScopeJson, ExpiresAt: arg.ExpiresAt,
	}, nil
}

func issueAs(t *testing.T, q Querier, caps []string, req tokenIssueReq) *httptest.ResponseRecorder {
	t.Helper()
	h := &Handler{Q: q, Mint: MintConfig{Secret: []byte("s"), TTLSecond: 60}}
	r := chi.NewRouter()
	h.Mount(r)
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest("POST", "/auth/tokens", bytes.NewReader(body))
	withPrincipal(r, Principal{Subject: uuid.New(), Capabilities: caps}).ServeHTTP(rec, httpReq)
	return rec
}

// A `*` holder's literal capability list contains no concrete codes,
// but they must still be able to issue a granular token (that was the
// whole UX-debt item) — including delegating `*` itself.
func TestIssueToken_WildcardIssuerCanGrantGranularAndStar(t *testing.T) {
	q := &captureCreateFakeQ{}
	want := []string{"inventory:sites:read", "dns:zones:read", "power:control", "*"}
	rec := issueAs(t, q, []string{"*"}, tokenIssueReq{Name: "granular", PermissionCodes: want})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if q.got == nil {
		t.Fatal("CreateApiToken never called")
	}
	var stored []string
	if err := json.Unmarshal(q.got.PermissionCodes, &stored); err != nil {
		t.Fatalf("stored permission_codes not JSON: %v", err)
	}
	if len(stored) != len(want) {
		t.Fatalf("stored %v, want %v", stored, want)
	}
	for i := range want {
		if stored[i] != want[i] {
			t.Errorf("stored[%d] = %q, want %q", i, stored[i], want[i])
		}
	}
}

// Non-wildcard callers keep the old behavior: a code their held caps
// don't grant is a 403, even when well-formed.
func TestIssueToken_NonWildcardStillRefusedForeignCaps(t *testing.T) {
	q := &captureCreateFakeQ{}
	rec := issueAs(t, q,
		[]string{"admin:api-tokens:create", "dns:zones:read"},
		tokenIssueReq{Name: "escalate", PermissionCodes: []string{"dns:zones:read", "inventory:sites:delete"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if q.got != nil {
		t.Error("CreateApiToken called despite escalation refusal")
	}
}

// Non-wildcard callers also can't request `*` itself.
func TestIssueToken_NonWildcardCannotGrantStar(t *testing.T) {
	rec := issueAs(t, &captureCreateFakeQ{},
		[]string{"admin:api-tokens:create"},
		tokenIssueReq{Name: "star", PermissionCodes: []string{"*"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// Malformed codes 400 — even for a `*` holder, who would otherwise
// pass HasCapability for any string and persist the typo.
func TestIssueToken_MalformedCode400(t *testing.T) {
	for _, bad := range []string{"Inventory:Sites:Read", "a:b:c:d", "inventory", "inventory:sites:", ""} {
		q := &captureCreateFakeQ{}
		rec := issueAs(t, q, []string{"*"},
			tokenIssueReq{Name: "typo", PermissionCodes: []string{"dns:zones:read", bad}})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("code %q: status = %d (%s), want 400", bad, rec.Code, rec.Body.String())
		}
		if q.got != nil {
			t.Errorf("code %q: CreateApiToken called despite malformed code", bad)
		}
	}
}
