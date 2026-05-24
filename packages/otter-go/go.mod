module github.com/usg-dcim/packages/otter-go

go 1.25.0

require (
	github.com/coreos/go-oidc/v3 v3.18.0
	github.com/fernet/fernet-go v0.0.0-20240119011108-303da6aec611
	github.com/go-chi/chi/v5 v5.3.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.9.2
	github.com/usg-dcim/packages/shared-go v0.0.0-00010101000000-000000000000
	golang.org/x/crypto v0.51.0
	golang.org/x/oauth2 v0.36.0
)

require (
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/text v0.37.0 // indirect
)

// In-tree module; see packages/shared-go/README.md.
replace github.com/usg-dcim/packages/shared-go => ../shared-go
