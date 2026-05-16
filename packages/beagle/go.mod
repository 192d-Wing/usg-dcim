module github.com/usg-dcim/packages/beagle

go 1.22

require (
	github.com/jackc/pgx/v5 v5.6.0
	github.com/prometheus-community/pro-bing v0.4.0
	github.com/usg-dcim/packages/shared-go v0.0.0-00010101000000-000000000000
)

// In-tree module; see packages/shared-go/README.md.
replace github.com/usg-dcim/packages/shared-go => ../shared-go

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20221227161230-091c0ba34f0a // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	golang.org/x/crypto v0.19.0 // indirect
	golang.org/x/net v0.21.0 // indirect
	golang.org/x/sync v0.6.0 // indirect
	golang.org/x/sys v0.17.0 // indirect
	golang.org/x/text v0.14.0 // indirect
)
