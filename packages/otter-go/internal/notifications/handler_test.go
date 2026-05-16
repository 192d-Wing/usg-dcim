package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
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

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}
func do(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
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
