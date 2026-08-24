package dashboards

import (
	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// Compile-time guards for the narrow per-endpoint Querier interfaces
// that handlers assert at RUNTIME via h.Q.(XxxQuerier). Without these,
// a signature drift in db/generated turns into a silent 500
// ("… requires full Querier") instead of a build failure — which is
// exactly how the sqlc regeneration briefly broke the rack-detail
// endpoint.
var (
	_ AssetDetailQuerier    = (*dbq.Queries)(nil)
	_ BuildingDetailQuerier = (*dbq.Queries)(nil)
	_ ForecastQuerier       = (*dbq.Queries)(nil)
	_ FreeSpaceQuerier      = (*dbq.Queries)(nil)
	_ RackDetailQuerier     = (*dbq.Queries)(nil)
	_ SiteDetailQuerier     = (*dbq.Queries)(nil)
	_ SitesAtRiskQuerier    = (*dbq.Queries)(nil)
)
