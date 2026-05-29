// Tests for POST /lir/allocations/{id}/arin/retry — the manual
// reset endpoint operators hit after a permanently-failed ARIN job.
package lir

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

func TestArinRetry_ResetsFailedJob(t *testing.T) {
	f := newFake()
	allocID := uuid.New()
	orgID := uuid.New()
	errMsg := "transient: timeout"
	f.allocations = map[uuid.UUID]dbq.LirAllocation{
		allocID: {
			ID: allocID, OrganizationID: orgID,
			ArinStatus: "failed", ArinAttempts: 5, ArinLastError: &errMsg,
		},
	}
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/allocations/"+allocID.String()+"/arin/retry", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	after := f.allocations[allocID]
	if after.ArinStatus != "pending" {
		t.Errorf("status not flipped to pending: %s", after.ArinStatus)
	}
	if after.ArinAttempts != 0 {
		t.Errorf("attempts not zeroed: %d", after.ArinAttempts)
	}
	if after.ArinLastError != nil {
		t.Errorf("last_error not cleared: %v", after.ArinLastError)
	}
}

func TestArinRetry_NotFound(t *testing.T) {
	rec := do(t, mountWith(newFake(), globalPrincipal()),
		"POST", "/lir/allocations/"+uuid.New().String()+"/arin/retry", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestArinRetry_RegisteredIs409(t *testing.T) {
	f := newFake()
	allocID := uuid.New()
	handle := "NET-OK-1"
	f.allocations = map[uuid.UUID]dbq.LirAllocation{
		allocID: {
			ID: allocID, OrganizationID: uuid.New(),
			ArinStatus: "registered", ArinNetHandle: &handle,
		},
	}
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/allocations/"+allocID.String()+"/arin/retry", nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("registered should be 409, got %d", rec.Code)
	}
}

func TestArinRetry_PendingIs409(t *testing.T) {
	// Already pending → retrying is racy. Conflict.
	f := newFake()
	allocID := uuid.New()
	f.allocations = map[uuid.UUID]dbq.LirAllocation{
		allocID: {ID: allocID, OrganizationID: uuid.New(), ArinStatus: "pending"},
	}
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/allocations/"+allocID.String()+"/arin/retry", nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
}

func TestArinRetry_OutOfScopeIs404(t *testing.T) {
	f := newFake()
	allocID := uuid.New()
	f.allocations = map[uuid.UUID]dbq.LirAllocation{
		allocID: {
			ID: allocID, OrganizationID: uuid.New(),
			ArinStatus: "failed", ArinAttempts: 5,
		},
	}
	rec := do(t, mountWith(f, orgScopedPrincipal(uuid.New())),
		"POST", "/lir/allocations/"+allocID.String()+"/arin/retry", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

// Pin the post-review fix that ResetArinJobForRetry routes by the
// allocation's direction (inferred from arin_net_handle). A
// previously-registered allocation that failed during the remove
// direction must reset to 'removing' so the remove-claim query
// picks it up. The earlier shape always wrote 'pending' which
// orphaned the row — submit-claim refused it (net_handle non-null),
// remove-claim refused it (status not 'removing'/'failed').
func TestArinRetry_RemoveDirectionResetsToRemoving(t *testing.T) {
	f := newFake()
	allocID := uuid.New()
	handle := "NET-OK-1"
	errMsg := "transient: timeout during delete"
	f.allocations = map[uuid.UUID]dbq.LirAllocation{
		allocID: {
			ID:             allocID,
			OrganizationID: uuid.New(),
			ArinStatus:     "failed",
			ArinAttempts:   5,
			ArinNetHandle:  &handle,
			ArinLastError:  &errMsg,
		},
	}
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/allocations/"+allocID.String()+"/arin/retry", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	after := f.allocations[allocID]
	if after.ArinStatus != "removing" {
		t.Errorf("remove-direction reset should flip to 'removing', got %q", after.ArinStatus)
	}
	if after.ArinAttempts != 0 {
		t.Errorf("attempts should reset to 0, got %d", after.ArinAttempts)
	}
	if after.ArinNetHandle == nil || *after.ArinNetHandle != handle {
		t.Errorf("net_handle must NOT be cleared (deassignment still targets it), got %+v", after.ArinNetHandle)
	}
}

func TestArinRetry_NoneStatusAlsoResets(t *testing.T) {
	// arin_status='none' covers pools with no ARIN handle. The retry
	// endpoint still accepts it — flipping to 'pending' lets the
	// worker pick it up if the pool was later wired with a handle.
	f := newFake()
	allocID := uuid.New()
	f.allocations = map[uuid.UUID]dbq.LirAllocation{
		allocID: {ID: allocID, OrganizationID: uuid.New(), ArinStatus: "none"},
	}
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/allocations/"+allocID.String()+"/arin/retry", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.allocations[allocID].ArinStatus != "pending" {
		t.Errorf("status not flipped: %s", f.allocations[allocID].ArinStatus)
	}
}
