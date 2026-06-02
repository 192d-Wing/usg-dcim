package dns

// Bundle-method stubs for every fakeQ-shaped struct in this package.
// PR 30 added ListDnsZonesByFabric / ListDnsRecordsByZoneIDs /
// ListUnhealthyEnabledHealthChecksByFabric to the Querier interface;
// the existing per-test fakes need stubs to keep compiling. Gathered
// here so a future Querier expansion doesn't require editing 8
// separate _test.go files.

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// --- fakeQ (handler_test.go) ---
func (f *fakeQ) ListDnsZonesByFabric(_ context.Context, _ uuid.UUID) ([]dbq.DnsZone, error) {
	return nil, nil
}
func (f *fakeQ) ListDnsRecordsByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsRecordForBundle, error) {
	return nil, nil
}
func (f *fakeQ) ListUnhealthyEnabledHealthChecksByFabric(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (f *fakeQ) GetEnabledDnsCatalogZoneByFabric(_ context.Context, _ uuid.UUID) (dbq.DnsCatalogZone, error) {
	return dbq.DnsCatalogZone{}, pgx.ErrNoRows
}
func (f *fakeQ) ListEnabledAuthDnsServersByFabric(_ context.Context, _ uuid.UUID) ([]dbq.AuthDnsServerForCatalog, error) {
	return nil, nil
}
func (f *fakeQ) ListDnsKeysByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsKeyRow, error) {
	return nil, nil
}

// --- fakeImportQ (bind_parser_test.go) ---
func (f *fakeImportQ) ListDnsZonesByFabric(_ context.Context, _ uuid.UUID) ([]dbq.DnsZone, error) {
	return nil, nil
}
func (f *fakeImportQ) ListDnsRecordsByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsRecordForBundle, error) {
	return nil, nil
}
func (f *fakeImportQ) ListUnhealthyEnabledHealthChecksByFabric(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (f *fakeImportQ) GetEnabledDnsCatalogZoneByFabric(_ context.Context, _ uuid.UUID) (dbq.DnsCatalogZone, error) {
	return dbq.DnsCatalogZone{}, pgx.ErrNoRows
}
func (f *fakeImportQ) ListEnabledAuthDnsServersByFabric(_ context.Context, _ uuid.UUID) ([]dbq.AuthDnsServerForCatalog, error) {
	return nil, nil
}
func (f *fakeImportQ) ListDnsKeysByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsKeyRow, error) {
	return nil, nil
}

// --- fakeDashboardQ (dashboard_test.go) ---
func (f *fakeDashboardQ) ListDnsZonesByFabric(_ context.Context, _ uuid.UUID) ([]dbq.DnsZone, error) {
	return nil, nil
}
func (f *fakeDashboardQ) ListDnsRecordsByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsRecordForBundle, error) {
	return nil, nil
}
func (f *fakeDashboardQ) ListUnhealthyEnabledHealthChecksByFabric(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (f *fakeDashboardQ) GetEnabledDnsCatalogZoneByFabric(_ context.Context, _ uuid.UUID) (dbq.DnsCatalogZone, error) {
	return dbq.DnsCatalogZone{}, pgx.ErrNoRows
}
func (f *fakeDashboardQ) ListEnabledAuthDnsServersByFabric(_ context.Context, _ uuid.UUID) ([]dbq.AuthDnsServerForCatalog, error) {
	return nil, nil
}
func (f *fakeDashboardQ) ListDnsKeysByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsKeyRow, error) {
	return nil, nil
}

// --- fakeEnableDnssecQ (dnssec_keygen_test.go) ---
func (f *fakeEnableDnssecQ) ListDnsZonesByFabric(_ context.Context, _ uuid.UUID) ([]dbq.DnsZone, error) {
	return nil, nil
}
func (f *fakeEnableDnssecQ) ListDnsRecordsByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsRecordForBundle, error) {
	return nil, nil
}
func (f *fakeEnableDnssecQ) ListUnhealthyEnabledHealthChecksByFabric(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (f *fakeEnableDnssecQ) GetEnabledDnsCatalogZoneByFabric(_ context.Context, _ uuid.UUID) (dbq.DnsCatalogZone, error) {
	return dbq.DnsCatalogZone{}, pgx.ErrNoRows
}
func (f *fakeEnableDnssecQ) ListEnabledAuthDnsServersByFabric(_ context.Context, _ uuid.UUID) ([]dbq.AuthDnsServerForCatalog, error) {
	return nil, nil
}
func (f *fakeEnableDnssecQ) ListDnsKeysByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsKeyRow, error) {
	return nil, nil
}

// --- fakeLifecycleQ (dnssec_lifecycle_test.go) ---
func (f *fakeLifecycleQ) ListDnsZonesByFabric(_ context.Context, _ uuid.UUID) ([]dbq.DnsZone, error) {
	return nil, nil
}
func (f *fakeLifecycleQ) ListDnsRecordsByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsRecordForBundle, error) {
	return nil, nil
}
func (f *fakeLifecycleQ) ListUnhealthyEnabledHealthChecksByFabric(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (f *fakeLifecycleQ) GetEnabledDnsCatalogZoneByFabric(_ context.Context, _ uuid.UUID) (dbq.DnsCatalogZone, error) {
	return dbq.DnsCatalogZone{}, pgx.ErrNoRows
}
func (f *fakeLifecycleQ) ListEnabledAuthDnsServersByFabric(_ context.Context, _ uuid.UUID) ([]dbq.AuthDnsServerForCatalog, error) {
	return nil, nil
}
func (f *fakeLifecycleQ) ListDnsKeysByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsKeyRow, error) {
	return nil, nil
}

// --- fakeDnssecQ (dnssec_test.go) ---
func (f *fakeDnssecQ) ListDnsZonesByFabric(_ context.Context, _ uuid.UUID) ([]dbq.DnsZone, error) {
	return nil, nil
}
func (f *fakeDnssecQ) ListDnsRecordsByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsRecordForBundle, error) {
	return nil, nil
}
func (f *fakeDnssecQ) ListUnhealthyEnabledHealthChecksByFabric(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (f *fakeDnssecQ) GetEnabledDnsCatalogZoneByFabric(_ context.Context, _ uuid.UUID) (dbq.DnsCatalogZone, error) {
	return dbq.DnsCatalogZone{}, pgx.ErrNoRows
}
func (f *fakeDnssecQ) ListEnabledAuthDnsServersByFabric(_ context.Context, _ uuid.UUID) ([]dbq.AuthDnsServerForCatalog, error) {
	return nil, nil
}
func (f *fakeDnssecQ) ListDnsKeysByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsKeyRow, error) {
	return nil, nil
}

// --- fakeHCResultQ (healthcheck_result_test.go) ---
func (f *fakeHCResultQ) ListDnsZonesByFabric(_ context.Context, _ uuid.UUID) ([]dbq.DnsZone, error) {
	return nil, nil
}
func (f *fakeHCResultQ) ListDnsRecordsByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsRecordForBundle, error) {
	return nil, nil
}
func (f *fakeHCResultQ) ListUnhealthyEnabledHealthChecksByFabric(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (f *fakeHCResultQ) GetEnabledDnsCatalogZoneByFabric(_ context.Context, _ uuid.UUID) (dbq.DnsCatalogZone, error) {
	return dbq.DnsCatalogZone{}, pgx.ErrNoRows
}
func (f *fakeHCResultQ) ListEnabledAuthDnsServersByFabric(_ context.Context, _ uuid.UUID) ([]dbq.AuthDnsServerForCatalog, error) {
	return nil, nil
}
func (f *fakeHCResultQ) ListDnsKeysByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsKeyRow, error) {
	return nil, nil
}

// --- scopedFakeQ (scope_test.go) ---
func (s *scopedFakeQ) ListDnsZonesByFabric(_ context.Context, _ uuid.UUID) ([]dbq.DnsZone, error) {
	return nil, nil
}
func (s *scopedFakeQ) ListDnsRecordsByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsRecordForBundle, error) {
	return nil, nil
}
func (s *scopedFakeQ) ListUnhealthyEnabledHealthChecksByFabric(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (s *scopedFakeQ) GetEnabledDnsCatalogZoneByFabric(_ context.Context, _ uuid.UUID) (dbq.DnsCatalogZone, error) {
	return dbq.DnsCatalogZone{}, pgx.ErrNoRows
}
func (s *scopedFakeQ) ListEnabledAuthDnsServersByFabric(_ context.Context, _ uuid.UUID) ([]dbq.AuthDnsServerForCatalog, error) {
	return nil, nil
}
func (s *scopedFakeQ) ListDnsKeysByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsKeyRow, error) {
	return nil, nil
}
