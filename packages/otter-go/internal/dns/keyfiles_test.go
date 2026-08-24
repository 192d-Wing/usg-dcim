package dns

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// generatePEM is a test helper that mints a fresh ECDSA P-256 or
// Ed25519 PKCS8 PEM. Pure crypto/* stdlib — no test fixtures to
// keep in sync.
func generatePEM(t *testing.T, alg string) string {
	t.Helper()
	var key any
	switch alg {
	case "ecdsap256sha256":
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key = k
	case "ed25519":
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key = priv
	default:
		t.Fatalf("unknown alg %q", alg)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// ===== BindKeyBasename =====

func TestBindKeyBasename_FormatPadding(t *testing.T) {
	got := BindKeyBasename("example.com.", 13, 12345)
	want := "Kexample.com.+013+12345"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestBindKeyBasename_KeyTagPadsTo5Digits(t *testing.T) {
	got := BindKeyBasename("z.example.", 13, 7)
	if !strings.HasSuffix(got, "+00007") {
		t.Errorf("key tag should pad to 5 digits; got %q", got)
	}
}

func TestBindKeyBasename_TrailingDotNormalized(t *testing.T) {
	a := BindKeyBasename("example.com", 13, 1)
	b := BindKeyBasename("example.com.", 13, 1)
	if a != b {
		t.Errorf("missing-trailing-dot must produce same basename: %q vs %q", a, b)
	}
}

// ===== RenderBindPublicKeyFile =====

func TestRenderBindPublicKeyFile_KskGolden(t *testing.T) {
	got := RenderBindPublicKeyFile("example.com.", "ksk", 13, 12345, "AAAA")
	want := "; This is a KSK-type key, keyid 12345, for example.com.\n" +
		"example.com. IN DNSKEY 257 3 13 AAAA\n"
	if got != want {
		t.Errorf("KSK key file golden mismatch\nwant %q\ngot  %q", want, got)
	}
}

func TestRenderBindPublicKeyFile_ZskFlags(t *testing.T) {
	got := RenderBindPublicKeyFile("z.example.", "zsk", 13, 1, "AAAA")
	if !strings.Contains(got, "IN DNSKEY 256 3 13") {
		t.Errorf("ZSK flags should be 256, not 257; got %q", got)
	}
}

// ===== RenderBindPrivateKeyFile =====

func TestRenderBindPrivateKeyFile_EcdsaShape(t *testing.T) {
	pem := generatePEM(t, "ecdsap256sha256")
	got, err := RenderBindPrivateKeyFile("ecdsap256sha256", pem)
	if err != nil {
		t.Fatal(err)
	}
	wantHead := "Private-key-format: v1.3\nAlgorithm: 13 (ECDSAP256SHA256)\n"
	if !strings.HasPrefix(got, wantHead) {
		t.Errorf("ECDSA header wrong\nwant prefix %q\ngot %q", wantHead, got)
	}
	if !strings.Contains(got, "PrivateKey: ") {
		t.Errorf("missing PrivateKey line; got %q", got)
	}
	// PrivateKey value must be 32 bytes base64 → 44 chars (with padding).
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "PrivateKey: ") {
			v := strings.TrimPrefix(line, "PrivateKey: ")
			raw, err := base64.StdEncoding.DecodeString(v)
			if err != nil {
				t.Fatalf("PrivateKey base64 decode: %v", err)
			}
			if len(raw) != 32 {
				t.Errorf("ECDSA private scalar must be 32 bytes; got %d", len(raw))
			}
		}
	}
}

func TestRenderBindPrivateKeyFile_Ed25519Shape(t *testing.T) {
	pem := generatePEM(t, "ed25519")
	got, err := RenderBindPrivateKeyFile("ed25519", pem)
	if err != nil {
		t.Fatal(err)
	}
	wantHead := "Private-key-format: v1.3\nAlgorithm: 15 (ED25519)\n"
	if !strings.HasPrefix(got, wantHead) {
		t.Errorf("Ed25519 header wrong\nwant prefix %q\ngot %q", wantHead, got)
	}
	// 32-byte seed.
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "PrivateKey: ") {
			v := strings.TrimPrefix(line, "PrivateKey: ")
			raw, err := base64.StdEncoding.DecodeString(v)
			if err != nil {
				t.Fatalf("PrivateKey base64 decode: %v", err)
			}
			if len(raw) != 32 {
				t.Errorf("Ed25519 seed must be 32 bytes; got %d", len(raw))
			}
		}
	}
}

func TestRenderBindPrivateKeyFile_RsaDeferred(t *testing.T) {
	_, err := RenderBindPrivateKeyFile("rsasha256", "")
	if err == nil {
		t.Error("RSA should return error (rendering deferred); got nil")
	}
}

func TestRenderBindPrivateKeyFile_BadPemErrors(t *testing.T) {
	_, err := RenderBindPrivateKeyFile("ecdsap256sha256", "not a pem")
	if err == nil {
		t.Error("invalid PEM should error")
	}
}

// ===== RenderDnssecKeyFiles =====

func TestRenderDnssecKeyFiles_PairsSortedByFilename(t *testing.T) {
	pem1 := generatePEM(t, "ed25519")
	pem2 := generatePEM(t, "ed25519")
	keys := []dbq.DnsKey{
		{Algorithm: "ed25519", Role: "zsk", KeyTag: 54321, PublicKeyB64: "AAAA", PrivatePem: pem1},
		{Algorithm: "ed25519", Role: "ksk", KeyTag: 12345, PublicKeyB64: "AAAA", PrivatePem: pem2},
	}
	got, err := RenderDnssecKeyFiles("z.example.",
		keys,
		map[int32]string{54321: pem1, 12345: pem2},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 files (2 keys × 2 files); got %d", len(got))
	}
	// Sort order pins K-name+alg+tag asc, .key before .private (lex).
	wantFilenames := []string{
		"Kz.example.+015+12345.key",
		"Kz.example.+015+12345.private",
		"Kz.example.+015+54321.key",
		"Kz.example.+015+54321.private",
	}
	for i, w := range wantFilenames {
		if got[i].Filename != w {
			t.Errorf("file[%d]: got %q want %q", i, got[i].Filename, w)
		}
	}
}

func TestRenderDnssecKeyFiles_UnsupportedAlgErrors(t *testing.T) {
	_, err := RenderDnssecKeyFiles("z.example.",
		[]dbq.DnsKey{{Algorithm: "ml-kem", Role: "ksk", KeyTag: 1}},
		nil,
	)
	if err == nil {
		t.Error("unknown algorithm should error")
	}
}

// ===== RenderCdnskeyCdsLines =====

func TestRenderCdnskeyCdsLines_KskOnlyActive(t *testing.T) {
	// Mix of: active KSK, retired KSK (skipped), ZSK (skipped).
	now := int64(1700000000)
	_ = now
	keys := []dbq.DnsKey{
		{Algorithm: "ed25519", Role: "ksk", KeyTag: 11111, PublicKeyB64: "AAAA"},
		{Algorithm: "ed25519", Role: "ksk", KeyTag: 22222, PublicKeyB64: "AAAA",
			RetiredAt: tsPtr(1700000000)},
		{Algorithm: "ed25519", Role: "zsk", KeyTag: 33333, PublicKeyB64: "AAAA"},
	}
	got := RenderCdnskeyCdsLines("example.com.", keys)
	// 1 active KSK → 1 CDNSKEY + 1 CDS = 2 lines.
	if len(got) != 2 {
		t.Fatalf("want 2 lines (one pair), got %d:\n%v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "@\tIN\tCDNSKEY\t257 3 15 AAAA") {
		t.Errorf("CDNSKEY shape wrong: %q", got[0])
	}
	if !strings.HasPrefix(got[1], "@\tIN\tCDS\t11111 15 2 ") {
		t.Errorf("CDS shape wrong: %q", got[1])
	}
	// Retired KSK's tag must NOT appear (would mislead RFC 8078 scanners).
	for _, line := range got {
		if strings.Contains(line, "22222") {
			t.Errorf("retired KSK leaked into CDS/CDNSKEY: %q", line)
		}
	}
}

func TestRenderCdnskeyCdsLines_NoActiveKsk(t *testing.T) {
	got := RenderCdnskeyCdsLines("example.com.",
		[]dbq.DnsKey{{Algorithm: "ed25519", Role: "zsk", KeyTag: 1, PublicKeyB64: "AAAA"}},
	)
	if len(got) != 0 {
		t.Errorf("no active KSK should produce no lines; got %v", got)
	}
}

func TestRenderCdnskeyCdsLines_BadBase64Skipped(t *testing.T) {
	got := RenderCdnskeyCdsLines("example.com.",
		[]dbq.DnsKey{{Algorithm: "ed25519", Role: "ksk", KeyTag: 1, PublicKeyB64: "!!!"}},
	)
	if len(got) != 0 {
		t.Errorf("malformed public key should be skipped; got %v", got)
	}
}

// tsPtr — pgx scans nullable timestamptz to *time.Time. We're testing
// the renderer's RetiredAt-is-nil check, so any non-nil value works.
func tsPtr(unix int64) *time.Time {
	t := time.Unix(unix, 0).UTC()
	return &t
}
