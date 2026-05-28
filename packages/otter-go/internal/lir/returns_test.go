// Tests for the return lifecycle endpoints:
//   POST /allocations/{id}/return-request  (tenant)
//   POST /allocations/{id}/return-confirm  (NIC)
//
// Exercises the state-machine guards on both sides, the org-scope
// 404-not-403 posture, and the arin_status='registered' →
// 'removing' co-promotion that confirm performs.
package lir

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// ---- shared fixture ----

func setupReturnScenario(t *testing.T, status, arinStatus string) (*fakeQ, uuid.UUID, uuid.UUID) {
	t.Helper()
	f := newFake()
	allocID := uuid.New()
	orgID := uuid.New()
	a := dbq.LirAllocation{
		ID: allocID, OrganizationID: orgID,
		Status:     status,
		ArinStatus: arinStatus,
	}
	if arinStatus == "registered" {
		h := "NET-OK-1"
		a.ArinNetHandle = &h
	}
	f.allocations = map[uuid.UUID]dbq.LirAllocation{allocID: a}
	return f, allocID, orgID
}

// ---- return-request ----

func TestReturnRequest_OK(t *testing.T) {
	f, allocID, _ := setupReturnScenario(t, "active", "registered")
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/allocations/"+allocID.String()+"/return-request",
		map[string]any{"reason": "decommissioning the lab"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.allocations[allocID].Status != "return_requested" {
		t.Errorf("status not flipped: %s", f.allocations[allocID].Status)
	}
}

func TestReturnRequest_RequiresReason(t *testing.T) {
	f, allocID, _ := setupReturnScenario(t, "active", "registered")
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/allocations/"+allocID.String()+"/return-request",
		map[string]any{"reason": ""})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d", rec.Code)
	}
}

func TestReturnRequest_NotActiveIs409(t *testing.T) {
	f, allocID, _ := setupReturnScenario(t, "return_requested", "registered")
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/allocations/"+allocID.String()+"/return-request",
		map[string]any{"reason": "x"})
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
}

func TestReturnRequest_NotFound(t *testing.T) {
	rec := do(t, mountWith(newFake(), globalPrincipal()),
		"POST", "/lir/allocations/"+uuid.New().String()+"/return-request",
		map[string]any{"reason": "x"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestReturnRequest_OutOfScopeIs404(t *testing.T) {
	f, allocID, _ := setupReturnScenario(t, "active", "registered")
	rec := do(t, mountWith(f, orgScopedPrincipal(uuid.New())),
		"POST", "/lir/allocations/"+allocID.String()+"/return-request",
		map[string]any{"reason": "x"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

// ---- return-confirm ----

func TestReturnConfirm_OK_PromotesRegisteredToRemoving(t *testing.T) {
	f, allocID, _ := setupReturnScenario(t, "return_requested", "registered")
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/allocations/"+allocID.String()+"/return-confirm", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	after := f.allocations[allocID]
	if after.Status != "returned" {
		t.Errorf("status not flipped: %s", after.Status)
	}
	if after.ArinStatus != "removing" {
		t.Errorf("arin_status should co-promote to 'removing', got %s", after.ArinStatus)
	}
	if after.ArinAttempts != 0 {
		t.Errorf("arin_attempts should reset to 0 for the new direction, got %d", after.ArinAttempts)
	}
}

func TestReturnConfirm_OK_NeverRegisteredStaysNone(t *testing.T) {
	// arin_status='none' (pool without an upstream handle) shouldn't
	// promote to 'removing' — there's nothing to deassign upstream.
	f, allocID, _ := setupReturnScenario(t, "return_requested", "none")
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/allocations/"+allocID.String()+"/return-confirm", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	after := f.allocations[allocID]
	if after.Status != "returned" {
		t.Errorf("status: %s", after.Status)
	}
	if after.ArinStatus != "none" {
		t.Errorf("arin_status should stay 'none', got %s", after.ArinStatus)
	}
}

func TestReturnConfirm_EmptyBodyAllowed(t *testing.T) {
	f, allocID, _ := setupReturnScenario(t, "return_requested", "none")
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/allocations/"+allocID.String()+"/return-confirm", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("got %d", rec.Code)
	}
}

func TestReturnConfirm_WrongStateIs409(t *testing.T) {
	f, allocID, _ := setupReturnScenario(t, "active", "none")
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/allocations/"+allocID.String()+"/return-confirm", nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
}

func TestReturnConfirm_OutOfScopeIs404(t *testing.T) {
	f, allocID, _ := setupReturnScenario(t, "return_requested", "none")
	rec := do(t, mountWith(f, orgScopedPrincipal(uuid.New())),
		"POST", "/lir/allocations/"+allocID.String()+"/return-confirm", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}
