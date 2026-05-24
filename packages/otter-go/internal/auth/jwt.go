// JWT verification matching the Python implementation in
// packages/otter/src/dcim/security/tokens.py and api/auth.py.
//
// Session JWTs are HS256-signed with `jwt_secret`; old secrets stay
// valid until their TTL passes so operators can rotate keys without
// invalidating active sessions. Tokens carry:
//   - sub:       user UUID
//   - jti:       UUID4 (deny-listable via revoked_jtis)
//   - exp:       unix seconds
//   - idp_roles: []string from IdP claim (optional)
//   - mfa:       bool (optional)
package auth

import (
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// SessionClaims mirrors the Python issue_user_jwt payload. Unknown
// fields are ignored on parse.
type SessionClaims struct {
	Subject  uuid.UUID `json:"-"`
	JTI      string    `json:"-"`
	IdpRoles []string  `json:"idp_roles,omitempty"`
	MFA      bool      `json:"mfa,omitempty"`
	jwt.RegisteredClaims
}

// VerifierConfig describes the HS256 keys this verifier accepts.
// PrimarySecret is the active signing key (matches jwt_secret); old
// secrets in OldSecrets are accepted but never used to mint. Algorithm
// is fixed at HS256 to mirror Python; allowing an attacker to choose
// would defeat the point.
type VerifierConfig struct {
	PrimarySecret []byte
	OldSecrets    [][]byte
}

// Verify parses a token string, checks the HS256 signature against any
// configured key, and returns the claims if exp/nbf are satisfied. The
// jti revocation check is handled by the middleware, not here, because
// it needs DB access.
func Verify(tokenStr string, cfg VerifierConfig) (*SessionClaims, error) {
	claims := &SessionClaims{}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"HS256"}))
	tok, err := parser.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		return cfg.PrimarySecret, nil
	})
	if err == nil && tok.Valid {
		return finalize(claims)
	}
	// Try old secrets next so key rotation doesn't invalidate live sessions.
	for _, old := range cfg.OldSecrets {
		c := &SessionClaims{}
		t2, err2 := parser.ParseWithClaims(tokenStr, c, func(t *jwt.Token) (any, error) {
			return old, nil
		})
		if err2 == nil && t2.Valid {
			return finalize(c)
		}
	}
	if err == nil {
		err = errors.New("token rejected")
	}
	return nil, err
}

func finalize(c *SessionClaims) (*SessionClaims, error) {
	if c.Subject == uuid.Nil {
		if c.RegisteredClaims.Subject == "" {
			return nil, errors.New("missing sub claim")
		}
		sub, err := uuid.Parse(c.RegisteredClaims.Subject)
		if err != nil {
			return nil, fmt.Errorf("sub is not a uuid: %w", err)
		}
		c.Subject = sub
	}
	if c.JTI == "" {
		c.JTI = c.RegisteredClaims.ID
	}
	if c.JTI == "" {
		return nil, errors.New("missing jti claim")
	}
	return c, nil
}
