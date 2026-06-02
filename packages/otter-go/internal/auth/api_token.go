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

// HashAPIToken is the exported entry point for packages outside auth
// that need the same hashing (e.g. collectors enrollment tokens stored
// in collectors.enrollment_token_hash).
func HashAPIToken(plain string) string { return hashAPIToken(plain) }

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
// starting with "dcim_") in api_tokens and validates that the token
// is usable. Beyond not-revoked + not-expired, the owner must still
// exist and be active, and the returned Capabilities are the token's
// permission_codes intersected with the owner's current effective
// capabilities. This mirrors the Python _principal_from_api_token in
// dcim/security/deps.py so that deactivating an owner or revoking a
// role propagates to outstanding tokens without an explicit revoke.
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
	// Owner must still exist and be active. A deactivated owner — or
	// one deleted out from under the token — invalidates every token
	// they ever issued, without operators having to walk api_tokens.
	owner, err := q.GetUser(ctx, tok.OwnerUserID)
	if err != nil {
		return Principal{}, false
	}
	if !owner.IsActive {
		return Principal{}, false
	}
	requested, err := decodeStringArray(tok.PermissionCodes)
	if err != nil {
		return Principal{}, false
	}
	// Re-bound the token to the owner's current effective caps. If the
	// owner's role was downgraded after the token was issued, the
	// downgrade takes effect here — any cap the owner can no longer
	// grant is dropped from the token's effective set.
	ownerCaps, err := q.GetUserCapabilities(ctx, owner.ID)
	if err != nil {
		return Principal{}, false
	}
	effective := make([]string, 0, len(requested))
	for _, code := range requested {
		if HasCapability(ownerCaps, code) {
			effective = append(effective, code)
		}
	}
	// Best-effort last_used_at touch; failures are swallowed so a
	// stats-table problem doesn't break auth.
	_ = q.TouchApiTokenLastUsed(ctx, tok.ID)
	return Principal{
		Subject:      tok.OwnerUserID,
		Capabilities: effective,
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

