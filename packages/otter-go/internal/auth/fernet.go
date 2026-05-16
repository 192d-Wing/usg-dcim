// Fernet-compatible wrappers for storing the IdP refresh_token at
// rest. Same key (DCIM_DNS_DNSSEC_SECRET) the Python side uses, so a
// stored token survives the Python → Go cutover unchanged.
//
// Python's cryptography.fernet uses urlsafe-base64-encoded 32-byte
// keys; github.com/fernet/fernet-go matches that format byte for byte
// so DCIM_DNS_DNSSEC_SECRET drops in without translation.
//
// Storage format mirrors Python encrypt_refresh_token:
//   "enc:v1:" + fernet_token
// The prefix lets us distinguish encrypted from legacy plaintext rows
// (none should exist post-migration; defense in depth).
package auth

import (
	"errors"
	"strings"
	"time"

	"github.com/fernet/fernet-go"
)

const refreshEncPrefix = "enc:v1:"

// FernetConfig holds the key set Fernet uses. Empty Keys means
// refresh-token encryption is disabled — storage falls back to
// plaintext (with a loud audit-log warning at the call site).
type FernetConfig struct {
	Keys []*fernet.Key
}

// ParseFernetKey decodes a single urlsafe-base64 32-byte key, matching
// the Python `Fernet(secret.encode("ascii"))` shape. Multiple keys can
// be passed comma-separated to support key rotation: encryption uses
// the first key, decryption tries all in order.
func ParseFernetKey(csv string) (FernetConfig, error) {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return FernetConfig{}, nil
	}
	parts := strings.Split(csv, ",")
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			cleaned = append(cleaned, p)
		}
	}
	if len(cleaned) == 0 {
		return FernetConfig{}, errors.New("fernet: no keys parsed")
	}
	keys, err := fernet.DecodeKeys(cleaned...)
	if err != nil {
		return FernetConfig{}, err
	}
	return FernetConfig{Keys: keys}, nil
}

// encryptRefreshToken returns the "enc:v1:<fernet>" stored form. When
// no keys are configured returns the plaintext untouched — caller
// chooses whether to warn or refuse.
func encryptRefreshToken(plain string, cfg FernetConfig) (string, error) {
	if len(cfg.Keys) == 0 {
		return plain, nil
	}
	box, err := fernet.EncryptAndSign([]byte(plain), cfg.Keys[0])
	if err != nil {
		return "", err
	}
	return refreshEncPrefix + string(box), nil
}

// decryptRefreshToken handles both the "enc:v1:" envelope and legacy
// plaintext rows. ttl=0 disables freshness check (matches Python's
// behavior — the IdP itself enforces refresh-token expiry).
func decryptRefreshToken(stored string, cfg FernetConfig) (string, error) {
	if !strings.HasPrefix(stored, refreshEncPrefix) {
		return stored, nil
	}
	if len(cfg.Keys) == 0 {
		return "", errors.New("refresh-token is encrypted but no fernet keys configured")
	}
	box := stored[len(refreshEncPrefix):]
	if msg := fernet.VerifyAndDecrypt([]byte(box), time.Duration(0), cfg.Keys); msg != nil {
		return string(msg), nil
	}
	return "", errors.New("fernet: decrypt failed (key mismatch or ciphertext tampered)")
}
