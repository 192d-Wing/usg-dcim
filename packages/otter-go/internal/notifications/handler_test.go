package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

type fakeQ struct {
	last     dbq.ListNotificationChannelsParams
	channels []dbq.NotificationChannel
	listErr  error
}

func (f *fakeQ) ListNotificationChannels(_ context.Context, a dbq.ListNotificationChannelsParams) ([]dbq.NotificationChannel, error) {
	f.last = a
	return f.channels, f.listErr
}
func (f *fakeQ) CountNotificationChannels(_ context.Context) (int64, error) {
	return int64(len(f.channels)), nil
}
func (f *fakeQ) CreateNotificationChannel(_ context.Context, a dbq.CreateNotificationChannelParams) (dbq.NotificationChannel, error) {
	return dbq.NotificationChannel{ID: uuid.New(), Name: a.Name, Kind: a.Kind, MinSeverity: a.MinSeverity, Enabled: a.Enabled}, nil
}
func (f *fakeQ) UpdateNotificationChannel(_ context.Context, a dbq.UpdateNotificationChannelParams) (dbq.NotificationChannel, error) {
	return dbq.NotificationChannel{ID: a.ID}, nil
}
func (f *fakeQ) DeleteNotificationChannel(_ context.Context, _ uuid.UUID) error { return nil }

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}
func do(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	// Wildcard principal — the LIST gate gained
	// notifications:channels:read in this PR; mutation tests inject
	// their own principal.
	req := authtest.Request(http.MethodGet, p, authtest.PrincipalWithCaps("*"), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestListChannels_DefaultPaging(t *testing.T) {
	f := &fakeQ{channels: []dbq.NotificationChannel{{Name: "ops-pager", Kind: "slack"}}}
	rec := do(t, mount(f), "/notifications/channels")
	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if f.last.Limit != 50 || f.last.Offset != 0 {
		t.Errorf("default pagination wrong: %+v", f.last)
	}
	var body channelsPage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 1 || body.Items[0].Name != "ops-pager" {
		t.Errorf("body wrong: %+v", body)
	}
}

func TestListChannels_PageSizeAlias(t *testing.T) {
	f := &fakeQ{}
	do(t, mount(f), "/notifications/channels?page_size=200")
	if f.last.Limit != 200 {
		t.Errorf("page_size not honored: %d", f.last.Limit)
	}
}

func TestListChannels_LimitWinsOverPageSize(t *testing.T) {
	// FastAPI's alias rule: explicit `limit` wins when both are passed.
	f := &fakeQ{}
	do(t, mount(f), "/notifications/channels?limit=10&page_size=200")
	if f.last.Limit != 10 {
		t.Errorf("limit should win: %d", f.last.Limit)
	}
}

func TestListChannels_DBError(t *testing.T) {
	f := &fakeQ{listErr: errors.New("boom")}
	rec := do(t, mount(f), "/notifications/channels")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("got %d", rec.Code)
	}
}

// Cap-gate negative tests pin the RBAC fix: pre-fix, LIST had no
// gate (any verified principal could enumerate channels) and the
// mutations used `alerts:notifications:*` cap codes that didn't
// exist in either catalog. Both now require notifications:channels:*.

func TestListChannels_NoCap_403(t *testing.T) {
	// Empty-cap principal must not see the channel list.
	p := authtest.PrincipalWithCaps()
	req := authtest.Request(http.MethodGet, "/notifications/channels", p, nil)
	rec := httptest.NewRecorder()
	mount(&fakeQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestListChannels_OldBogusCap_StillDenied(t *testing.T) {
	// The pre-fix code accepted alerts:notifications:* — verify that
	// a principal holding only the old bogus code is now denied.
	p := authtest.PrincipalWithCaps("alerts:notifications:read")
	req := authtest.Request(http.MethodGet, "/notifications/channels", p, nil)
	rec := httptest.NewRecorder()
	mount(&fakeQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (old cap should not match), got %d", rec.Code)
	}
}

func TestListChannels_CanonicalCap_200(t *testing.T) {
	p := authtest.PrincipalWithCaps("notifications:channels:read")
	req := authtest.Request(http.MethodGet, "/notifications/channels", p, nil)
	rec := httptest.NewRecorder()
	mount(&fakeQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateChannel_CanonicalCap_201(t *testing.T) {
	p := authtest.PrincipalWithCaps("notifications:channels:create")
	body := []byte(`{"name":"ops","kind":"slack","webhook_url":"https://hooks.slack.com/x"}`)
	req := authtest.Request(http.MethodPost, "/notifications/channels", p, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mount(&fakeQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateChannel_OldBogusCap_403(t *testing.T) {
	p := authtest.PrincipalWithCaps("alerts:notifications:create")
	body := []byte(`{"name":"ops","kind":"slack","webhook_url":"https://hooks.slack.com/x"}`)
	req := authtest.Request(http.MethodPost, "/notifications/channels", p, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mount(&fakeQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteChannel_CanonicalCap_204(t *testing.T) {
	p := authtest.PrincipalWithCaps("notifications:channels:delete")
	req := authtest.Request(http.MethodDelete, "/notifications/channels/"+uuid.New().String(), p, nil)
	rec := httptest.NewRecorder()
	mount(&fakeQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rec.Code, rec.Body.String())
	}
}
