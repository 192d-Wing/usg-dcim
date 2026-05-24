package auth

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// MintConfig describes how Mint signs session JWTs. Algorithm is fixed
// at HS256 to match Python's issue_user_jwt.
type MintConfig struct {
	Secret    []byte
	TTLSecond int
}

// Mint issues a session JWT for `subject` with a fresh UUID4 jti. The
// claims shape matches Python's issue_user_jwt so otter-go-issued and
// Python-issued tokens interoperate verbatim.
func Mint(cfg MintConfig, subject uuid.UUID, idpRoles []string, mfa bool) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(time.Duration(cfg.TTLSecond) * time.Second)
	jti := uuid.New().String()
	claims := jwt.MapClaims{
		"sub": subject.String(),
		"jti": jti,
		"iat": now.Unix(),
		"exp": exp.Unix(),
	}
	if len(idpRoles) > 0 {
		claims["idp_roles"] = idpRoles
	}
	if mfa {
		claims["mfa"] = true
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString(cfg.Secret)
	return s, exp, err
}
