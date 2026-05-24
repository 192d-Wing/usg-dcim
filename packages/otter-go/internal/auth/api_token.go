package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// hashAPIToken mirrors Python security.tokens.hash_api_token:
// sha256(plaintext).hexdigest(). The plaintext is what the operator
// holds; the digest is what we store in api_tokens.token_hash.
func hashAPIToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// generateAPIToken returns (plaintext, digest). Plaintext is
// "dcim_" + base64url(32 random bytes), matching Python's
// generate_api_token which uses secrets.token_urlsafe(32).
func generateAPIToken() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw := "dcim_" + base64.RawURLEncoding.EncodeToString(buf)
	return raw, hashAPIToken(raw), nil
}

// apiTokenPrincipal looks up `raw` (the Authorization header value
// starting with "dcim_") in api_tokens, validates not-revoked +
// not-expired, and returns a Principal whose capabilities come
// straight from the token row's permission_codes.
func apiTokenPrincipal(ctx context.Context, q Querier, raw string) (Principal, bool) {
	if !strings.HasPrefix(raw, "dcim_") {
		return Principal{}, false
	}
	tok, err := q.GetApiTokenByHash(ctx, hashAPIToken(raw))
	if err != nil {
		return Principal{}, false
	}
	if tok.Revoked {
		return Principal{}, false
	}
	if tok.ExpiresAt != nil && tok.ExpiresAt.Before(time.Now()) {
		return Principal{}, false
	}
	caps, err := decodeStringArray(tok.PermissionCodes)
	if err != nil {
		return Principal{}, false
	}
	// Best-effort last_used_at touch; failures are swallowed so a
	// stats-table problem doesn't break auth.
	_ = q.TouchApiTokenLastUsed(ctx, tok.ID)
	return Principal{
		Subject:      tok.OwnerUserID,
		Capabilities: caps,
		Label:        "token:" + tok.ID.String(),
	}, true
}

func decodeStringArray(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

