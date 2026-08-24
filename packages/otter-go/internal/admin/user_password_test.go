// Handler tests for POST /admin/users/{id}/password (UX-debt batch).
package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

func TestSetUserPassword_HappyPath_204AndBcryptRoundTrip(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{setPwRows: 1}
	const password = "correct-horse-battery"
	body, _ := json.Marshal(map[string]any{"password": password})
	rec := doReq(t, mount(f), "POST", "/admin/users/"+id.String()+"/password", body)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body=%s), want 204", rec.Code, rec.Body.String())
	}
	if f.gotSetPw.ID != id {
		t.Errorf("forwarded id = %s, want %s", f.gotSetPw.ID, id)
	}
	// The handler must store a bcrypt hash of the password — the same
	// scheme auth's login CompareHashAndPassword verifies — never the
	// plaintext itself.
	if f.gotSetPw.PasswordHash == password {
		t.Fatal("plaintext password stored verbatim")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(f.gotSetPw.PasswordHash), []byte(password)); err != nil {
		t.Errorf("stored hash does not verify against the password: %v", err)
	}
}

func TestSetUserPassword_ShortPasswordIs400(t *testing.T) {
	f := &fakeQ{setPwRows: 1}
	body, _ := json.Marshal(map[string]any{"password": "elevenchars"}) // 11 < 12
	rec := doReq(t, mount(f), "POST", "/admin/users/"+uuid.New().String()+"/password", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if f.gotSetPw.ID != uuid.Nil {
		t.Error("SetUserPasswordHash called despite validation failure")
	}
}

func TestSetUserPassword_UnknownUserIs404(t *testing.T) {
	f := &fakeQ{setPwRows: 0} // execrows: no row matched the id
	body, _ := json.Marshal(map[string]any{"password": "long-enough-password"})
	rec := doReq(t, mount(f), "POST", "/admin/users/"+uuid.New().String()+"/password", body)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestSetUserPassword_BadUUIDIs400(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"password": "long-enough-password"})
	rec := doReq(t, mount(&fakeQ{}), "POST", "/admin/users/not-a-uuid/password", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestSetUserPassword_MalformedJSONIs400(t *testing.T) {
	rec := doReq(t, mount(&fakeQ{}), "POST", "/admin/users/"+uuid.New().String()+"/password", []byte("not-json"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestSetUserPassword_OverlongPasswordIs400(t *testing.T) {
	// bcrypt caps input at 72 bytes; the handler 400s instead of 500ing.
	long := bytes.Repeat([]byte("a"), 73)
	body, _ := json.Marshal(map[string]any{"password": string(long)})
	rec := doReq(t, mount(&fakeQ{setPwRows: 1}), "POST", "/admin/users/"+uuid.New().String()+"/password", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestSetUserPassword_RequiresUpdateCap(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"password": "long-enough-password"})
	req := httptest.NewRequest("POST", "/admin/users/"+uuid.New().String()+"/password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"admin:users:read"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mount(&fakeQ{setPwRows: 1}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}
