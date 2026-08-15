module github.com/usg-dcim/packages/otter-go

go 1.25.7

require (
	github.com/coreos/go-oidc/v3 v3.18.0
	github.com/fernet/fernet-go v0.0.0-20240119011108-303da6aec611
	github.com/go-chi/chi/v5 v5.3.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/miekg/dns v1.1.72
	github.com/pressly/goose/v3 v3.27.1
	github.com/prometheus/client_golang v1.24.1
	github.com/prometheus/client_model v0.6.2
	github.com/redis/go-redis/v9 v9.20.0
	github.com/robfig/cron/v3 v3.0.1
	github.com/usg-dcim/packages/shared-go v0.0.0-00010101000000-000000000000
	golang.org/x/crypto v0.55.0
	golang.org/x/oauth2 v0.36.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/common v0.70.1 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	github.com/sethvargo/go-retry v0.3.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/mod v0.38.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/tools v0.48.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

// In-tree module; see packages/shared-go/README.md.
replace github.com/usg-dcim/packages/shared-go => ../shared-go
