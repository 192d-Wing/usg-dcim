package dns

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type fakeQ struct {
	lastZone dbq.ListDnsZonesParams
	lastRec  dbq.ListDnsRecordsParams
}

func (f *fakeQ) ListDnsZones(_ context.Context, a dbq.ListDnsZonesParams) ([]dbq.DnsZone, error) {
	f.lastZone = a
	return nil, nil
}
func (f *fakeQ) CountDnsZones(_ context.Context, _ dbq.CountDnsZonesParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetDnsZone(_ context.Context, _ uuid.UUID) (dbq.DnsZone, error) {
	return dbq.DnsZone{}, pgx.ErrNoRows
}
func (f *fakeQ) ListDnsRecords(_ context.Context, a dbq.ListDnsRecordsParams) ([]dbq.DnsRecord, error) {
	f.lastRec = a
	return nil, nil
}
func (f *fakeQ) CountDnsRecords(_ context.Context, _ dbq.CountDnsRecordsParams) (int64, error) {
	return 0, nil
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

func TestListZones_AllFilters(t *testing.T) {
	fid, sid := uuid.New(), uuid.New()
	f := &fakeQ{}
	rec := do(t, mount(f), "/dns/zones?fabric_id="+fid.String()+"&site_id="+sid.String()+"&kind=site")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.lastZone.FabricID == nil || *f.lastZone.FabricID != fid {
		t.Error("fabric_id")
	}
	if f.lastZone.SiteID == nil || *f.lastZone.SiteID != sid {
		t.Error("site_id")
	}
	if f.lastZone.Kind == nil || *f.lastZone.Kind != "site" {
		t.Error("kind")
	}
}

func TestGetZone_NotFound(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/dns/zones/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestListRecords_AllFilters(t *testing.T) {
	zid := uuid.New()
	f := &fakeQ{}
	rec := do(t, mount(f), "/dns/records?zone_id="+zid.String()+"&type=A&source=manual")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.lastRec.ZoneID == nil || *f.lastRec.ZoneID != zid {
		t.Error("zone_id")
	}
	if f.lastRec.Type == nil || *f.lastRec.Type != "A" {
		t.Error("type")
	}
	if f.lastRec.Source == nil || *f.lastRec.Source != "manual" {
		t.Error("source")
	}
}

func TestListZones_PageSizeAlias(t *testing.T) {
	f := &fakeQ{}
	rec := do(t, mount(f), "/dns/zones?page_size=200")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.lastZone.Limit != 200 {
		t.Errorf("page_size not honored: %d", f.lastZone.Limit)
	}
}

func TestBadUUIDs(t *testing.T) {
	for _, p := range []string{
		"/dns/zones?fabric_id=x",
		"/dns/zones?site_id=x",
		"/dns/zones/x",
		"/dns/records?zone_id=x",
	} {
		rec := do(t, mount(&fakeQ{}), p)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", p, rec.Code)
		}
	}
}
