module github.com/usg-dcim/packages/heron

go 1.22

require (
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.6.0
	github.com/usg-dcim/packages/shared-go v0.0.0-00010101000000-000000000000
)

// shared-go is an in-tree module; resolved via go.work in dev and via
// this replace directive when building this module standalone (e.g.
// Containerfile builds that copy only packages/heron/).
replace github.com/usg-dcim/packages/shared-go => ../shared-go

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20221227161230-091c0ba34f0a // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	github.com/stretchr/testify v1.8.4 // indirect
	golang.org/x/crypto v0.17.0 // indirect
	golang.org/x/sync v0.1.0 // indirect
	golang.org/x/text v0.14.0 // indirect
)
