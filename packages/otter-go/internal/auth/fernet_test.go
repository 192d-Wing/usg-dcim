package auth

import (
	"strings"
	"testing"

	"github.com/fernet/fernet-go"
)

func newKeyB64(t *testing.T) string {
	t.Helper()
	k := &fernet.Key{}
	if err := k.Generate(); err != nil {
		t.Fatal(err)
	}
	return k.Encode()
}

func TestFernet_RoundTrip(t *testing.T) {
	cfg, err := ParseFernetKey(newKeyB64(t))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := encryptRefreshToken("super-secret-refresh", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, refreshEncPrefix) {
		t.Fatalf("missing envelope prefix: %q", enc)
	}
	got, err := decryptRefreshToken(enc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != "super-secret-refresh" {
		t.Errorf("got %q", got)
	}
}

func TestFernet_LegacyPlaintextPassthrough(t *testing.T) {
	cfg, _ := ParseFernetKey(newKeyB64(t))
	// Strings without the "enc:v1:" prefix are returned as-is (legacy
	// rows from before encryption was wired).
	got, err := decryptRefreshToken("legacy-plaintext", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != "legacy-plaintext" {
		t.Errorf("got %q", got)
	}
}

func TestFernet_NoKeysReturnsPlaintext(t *testing.T) {
	// encrypt with empty config = no-op
	got, err := encryptRefreshToken("plain", FernetConfig{})
	if err != nil || got != "plain" {
		t.Errorf("got %q err=%v", got, err)
	}
}

func TestFernet_EncryptedWithoutKeysFails(t *testing.T) {
	// Encrypt first with a real key, then try to decrypt with empty config.
	cfg, _ := ParseFernetKey(newKeyB64(t))
	enc, _ := encryptRefreshToken("x", cfg)
	if _, err := decryptRefreshToken(enc, FernetConfig{}); err == nil {
		t.Fatal("expected decrypt-without-keys to fail")
	}
}

func TestFernet_KeyRotation(t *testing.T) {
	// Encrypt with old key, decrypt with both old + new in the keyring.
	oldKey := newKeyB64(t)
	newKey := newKeyB64(t)
	oldCfg, _ := ParseFernetKey(oldKey)
	enc, _ := encryptRefreshToken("rotate-me", oldCfg)

	// New cfg has new key first, old second (rotation: write-new, read-both).
	bothCfg, err := ParseFernetKey(newKey + "," + oldKey)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptRefreshToken(enc, bothCfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != "rotate-me" {
		t.Errorf("got %q", got)
	}
}
