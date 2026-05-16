package auth

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMustStubRefusesWithoutEnv(t *testing.T) {
	t.Setenv(EnvInsecureStub, "")
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when env var is unset")
		}
	}()
	MustStub(nopLogger())
}

func TestMustStubRefusesFalsy(t *testing.T) {
	t.Setenv(EnvInsecureStub, "false")
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on 'false'")
		}
	}()
	MustStub(nopLogger())
}

func TestMustStubAcceptsTruthy(t *testing.T) {
	t.Setenv(EnvInsecureStub, "true")
	mw := MustStub(nopLogger())
	if mw == nil {
		t.Fatal("expected middleware constructor to return non-nil")
	}
}

func TestStubMiddlewareRejectsMissingBearer(t *testing.T) {
	mw := stubMiddleware(nopLogger())
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("downstream handler should not run")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
}

func TestStubMiddlewareAcceptsBearerAndAttachesPrincipal(t *testing.T) {
	mw := stubMiddleware(nopLogger())
	var gotPrincipal Principal
	var ok bool
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPrincipal, ok = From(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer dcim_xxx")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("got %d, want 200", rec.Code)
	}
	if !ok {
		t.Fatal("principal not attached to context")
	}
	if len(gotPrincipal.Capabilities) != 1 || gotPrincipal.Capabilities[0] != "*" {
		t.Errorf("stub should attach * caps; got %v", gotPrincipal.Capabilities)
	}
}
