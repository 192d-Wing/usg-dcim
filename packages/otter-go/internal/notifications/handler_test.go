package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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
func (f *fakeQ) GetNotificationChannel(_ context.Context, id uuid.UUID) (dbq.NotificationChannel, error) {
	for _, c := range f.channels {
		if c.ID == id {
			return c, nil
		}
	}
	return dbq.NotificationChannel{}, pgx.ErrNoRows
}

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

// ---- POST /channels/{id}/test ----

// fakePoster captures requests and replays a programmed response.
type fakePoster struct {
	gotReq    *http.Request
	gotBody   []byte
	respCode  int
	respBody  string
	returnErr error
}

func (p *fakePoster) Do(req *http.Request) (*http.Response, error) {
	p.gotReq = req
	if req.Body != nil {
		buf := new(bytes.Buffer)
		buf.ReadFrom(req.Body)
		p.gotBody = buf.Bytes()
	}
	if p.returnErr != nil {
		return nil, p.returnErr
	}
	code := p.respCode
	if code == 0 {
		code = http.StatusOK
	}
	body := p.respBody
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		Header:     http.Header{},
	}, nil
}


// swapHTTPClient swaps the package-level default for the duration of
// the test. Tests in this package don't run in parallel.
func swapHTTPClient(t *testing.T, p httpPoster) {
	t.Helper()
	prev := defaultHTTPClient
	defaultHTTPClient = p
	t.Cleanup(func() { defaultHTTPClient = prev })
}

func swapSMTP(t *testing.T, s smtpSender) {
	t.Helper()
	prev := defaultSMTP
	defaultSMTP = s
	t.Cleanup(func() { defaultSMTP = prev })
}

func testChannel_POST(t *testing.T, h http.Handler, id uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	p := authtest.PrincipalWithCaps("notifications:channels:update")
	req := authtest.Request(http.MethodPost, "/notifications/channels/"+id.String()+"/test", p, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTestChannel_NotFound_404(t *testing.T) {
	rec := testChannel_POST(t, mount(&fakeQ{}), uuid.New())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTestChannel_DisabledChannel_ReportsReason(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{channels: []dbq.NotificationChannel{
		{ID: id, Name: "ops", Kind: "webhook", Enabled: false,
			MinSeverity: "warning", NotifyOnFire: true},
	}}
	rec := testChannel_POST(t, mount(f), id)
	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var body testChannelResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Delivered || body.Error != "channel is disabled" {
		t.Errorf("body wrong: %+v", body)
	}
}

func TestTestChannel_FilterSkipsWarning(t *testing.T) {
	// channel only fires on critical+ — the synthetic alert is
	// warning, so the filter rejects it. Matches Python's branch at
	// api/notifications.py:133.
	id := uuid.New()
	f := &fakeQ{channels: []dbq.NotificationChannel{
		{ID: id, Name: "ops", Kind: "webhook", Enabled: true,
			MinSeverity: "critical", NotifyOnFire: true},
	}}
	rec := testChannel_POST(t, mount(f), id)
	var body testChannelResponse
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Delivered || body.Error == "" {
		t.Errorf("expected delivered=false with error, got %+v", body)
	}
}

func TestTestChannel_FilterSkipsFireDisabled(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{channels: []dbq.NotificationChannel{
		{ID: id, Name: "ops", Kind: "webhook", Enabled: true,
			MinSeverity: "warning", NotifyOnFire: false},
	}}
	rec := testChannel_POST(t, mount(f), id)
	var body testChannelResponse
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Delivered {
		t.Errorf("notify_on_fire=false should skip; got delivered=true: %+v", body)
	}
}

func TestTestChannel_WebhookSuccess(t *testing.T) {
	id := uuid.New()
	cfg, _ := json.Marshal(map[string]any{"url": "https://hooks.example/foo"})
	f := &fakeQ{channels: []dbq.NotificationChannel{
		{ID: id, Name: "ops", Kind: "webhook", Enabled: true,
			MinSeverity: "warning", NotifyOnFire: true, ConfigJson: cfg},
	}}
	poster := &fakePoster{respCode: 200}
	swapHTTPClient(t, poster)
	rec := testChannel_POST(t, mount(f), id)
	var body testChannelResponse
	json.Unmarshal(rec.Body.Bytes(), &body)
	if !body.Delivered || body.Error != "" {
		t.Errorf("expected delivered=true, no error; got %+v", body)
	}
	if poster.gotReq == nil || poster.gotReq.URL.String() != "https://hooks.example/foo" {
		t.Errorf("webhook URL not forwarded; got %v", poster.gotReq)
	}
	// Spot-check the payload shape — at minimum the event key
	// should be "alert.fire".
	var posted map[string]any
	if err := json.Unmarshal(poster.gotBody, &posted); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	if posted["event"] != "alert.fire" {
		t.Errorf("event key wrong: %v", posted["event"])
	}
}

func TestTestChannel_WebhookErrorReportsStatus(t *testing.T) {
	id := uuid.New()
	cfg, _ := json.Marshal(map[string]any{"url": "https://hooks.example/foo"})
	f := &fakeQ{channels: []dbq.NotificationChannel{
		{ID: id, Name: "ops", Kind: "webhook", Enabled: true,
			MinSeverity: "warning", NotifyOnFire: true, ConfigJson: cfg},
	}}
	swapHTTPClient(t, &fakePoster{respCode: 503})
	rec := testChannel_POST(t, mount(f), id)
	var body testChannelResponse
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Delivered || body.Error == "" {
		t.Errorf("expected delivered=false with error, got %+v", body)
	}
	if !strings.Contains(body.Error, "503") {
		t.Errorf("error should mention status; got %q", body.Error)
	}
}

func TestTestChannel_UnknownKind_ReportsError(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{channels: []dbq.NotificationChannel{
		{ID: id, Name: "x", Kind: "carrier-pigeon", Enabled: true,
			MinSeverity: "warning", NotifyOnFire: true},
	}}
	rec := testChannel_POST(t, mount(f), id)
	var body testChannelResponse
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Delivered || body.Error == "" {
		t.Errorf("expected delivered=false with error, got %+v", body)
	}
}

func TestTestChannel_WebhookMissingURL(t *testing.T) {
	id := uuid.New()
	cfg, _ := json.Marshal(map[string]any{}) // empty config
	f := &fakeQ{channels: []dbq.NotificationChannel{
		{ID: id, Name: "ops", Kind: "webhook", Enabled: true,
			MinSeverity: "warning", NotifyOnFire: true, ConfigJson: cfg},
	}}
	rec := testChannel_POST(t, mount(f), id)
	var body testChannelResponse
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Delivered || !strings.Contains(body.Error, "url") {
		t.Errorf("expected error mentioning url, got %+v", body)
	}
}

func TestTestChannel_RequiresUpdateCap(t *testing.T) {
	// cap-less principal must be denied. The route uses
	// notifications:channels:update.
	id := uuid.New()
	f := &fakeQ{channels: []dbq.NotificationChannel{
		{ID: id, Name: "ops", Kind: "webhook", Enabled: true,
			MinSeverity: "warning", NotifyOnFire: true},
	}}
	req := authtest.Request(http.MethodPost,
		"/notifications/channels/"+id.String()+"/test",
		authtest.PrincipalWithCaps("notifications:channels:read"), nil)
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

// Pure-helper coverage so the (severity, event) filter logic is
// pinned even outside the HTTP path.
func TestChannelMatches_RespectsSeverityFloor(t *testing.T) {
	c := dbq.NotificationChannel{Enabled: true, NotifyOnFire: true, MinSeverity: "major"}
	if channelMatches(c, "warning", "fire") {
		t.Error("warning < major should not match")
	}
	if !channelMatches(c, "critical", "fire") {
		t.Error("critical >= major should match")
	}
}

func TestChannelMatches_RespectsEventGate(t *testing.T) {
	c := dbq.NotificationChannel{Enabled: true, NotifyOnFire: true, NotifyOnResolve: false, MinSeverity: "info"}
	if channelMatches(c, "info", "resolve") {
		t.Error("notify_on_resolve=false should reject resolve events")
	}
}

func TestSMTPSender_NoHostNoOp(t *testing.T) {
	// Mirrors Python's "soft no-op so dev/CI doesn't error on missing
	// SMTP" branch — when DCIM_SMTP_HOST is unset, sendEmail returns
	// (false, nil) without invoking the sender.
	t.Setenv("DCIM_SMTP_HOST", "")
	calls := 0
	swapSMTP(t, func(_ string, _ smtp.Auth, _ string, _ []string, _ []byte) error {
		calls++
		return nil
	})
	cfg := map[string]any{"recipients": []any{"a@example.com"}}
	delivered, err := sendEmail(defaultSMTP, cfg, syntheticAlert{Severity: "warning", State: "firing", Summary: "x"}, "fire")
	if err != nil || delivered {
		t.Errorf("expected (false, nil) for missing host, got (%v, %v)", delivered, err)
	}
	if calls != 0 {
		t.Errorf("smtp sender should not be invoked when host unset; calls=%d", calls)
	}
}

