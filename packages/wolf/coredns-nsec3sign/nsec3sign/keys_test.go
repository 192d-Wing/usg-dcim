// Key-loader tests.
//
// Each test generates a fresh DNSSEC key pair via miekg/dns, renders
// both halves to BIND format on a temp filesystem, and re-loads them
// through loadKey() to assert the parsed result. Going through the
// generate→render→load cycle is more useful than embedding fixed
// fixtures: it would catch a future bump of miekg/dns that changed
// the on-disk format, since fresh keys would then round-trip
// against the new format and stale fixtures wouldn't.

package nsec3sign

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/miekg/dns"
)

// algCase pairs a DNSSEC algorithm number with the bit-size argument
// the miekg/dns generator wants. RSA needs an actual key size;
// ECDSA/Ed25519 take a nominal size that the library ignores.
type algCase struct {
	name     string
	alg      uint8
	bits     int
	flags    uint16 // 257 = KSK, 256 = ZSK
	wantKSK  bool
	privType string // textual hint for the type assertion error
}

func TestLoadKeyRoundTrip(t *testing.T) {
	cases := []algCase{
		{name: "ecdsap256_zsk", alg: dns.ECDSAP256SHA256, bits: 256, flags: 256, wantKSK: false, privType: "*ecdsa.PrivateKey"},
		{name: "ecdsap256_ksk", alg: dns.ECDSAP256SHA256, bits: 256, flags: 257, wantKSK: true, privType: "*ecdsa.PrivateKey"},
		{name: "ed25519_zsk", alg: dns.ED25519, bits: 256, flags: 256, wantKSK: false, privType: "ed25519.PrivateKey"},
		{name: "rsasha256_zsk", alg: dns.RSASHA256, bits: 2048, flags: 256, wantKSK: false, privType: "*rsa.PrivateKey"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			basename := writeKeyPair(t, "example.test.", tc.alg, tc.bits, tc.flags)

			sk, err := loadKey(basename)
			if err != nil {
				t.Fatalf("loadKey: %v", err)
			}

			if sk.IsKSK != tc.wantKSK {
				t.Fatalf("IsKSK = %v, want %v", sk.IsKSK, tc.wantKSK)
			}
			if sk.Public == nil {
				t.Fatal("Public DNSKEY is nil")
			}
			if sk.Public.Algorithm != tc.alg {
				t.Fatalf("algorithm = %d, want %d", sk.Public.Algorithm, tc.alg)
			}
			if sk.KeyTag != sk.Public.KeyTag() {
				t.Fatalf("cached KeyTag %d does not match recomputed %d",
					sk.KeyTag, sk.Public.KeyTag())
			}

			// Spot-check the private half is the right concrete type
			// — the signer relies on a crypto.Signer-shaped object.
			switch tc.alg {
			case dns.ECDSAP256SHA256:
				if _, ok := sk.Private.(*ecdsa.PrivateKey); !ok {
					t.Fatalf("private key is %T, want %s", sk.Private, tc.privType)
				}
			case dns.ED25519:
				if _, ok := sk.Private.(ed25519.PrivateKey); !ok {
					t.Fatalf("private key is %T, want %s", sk.Private, tc.privType)
				}
			case dns.RSASHA256:
				if _, ok := sk.Private.(*rsa.PrivateKey); !ok {
					t.Fatalf("private key is %T, want %s", sk.Private, tc.privType)
				}
			}

			// And that it implements crypto.Signer (what miekg/dns
			// passes to dns.RRSIG.Sign).
			if _, ok := sk.Private.(crypto.Signer); !ok {
				t.Fatalf("private key %T does not implement crypto.Signer", sk.Private)
			}
		})
	}
}

func TestLoadKeysGroupsRoles(t *testing.T) {
	kskBase := writeKeyPair(t, "example.test.", dns.ECDSAP256SHA256, 256, 257)
	zskBase := writeKeyPair(t, "example.test.", dns.ECDSAP256SHA256, 256, 256)

	n := &Nsec3Sign{KeyFiles: []string{kskBase, zskBase}}
	if err := n.loadKeys(); err != nil {
		t.Fatalf("loadKeys: %v", err)
	}

	if got := len(n.KSKs()); got != 1 {
		t.Fatalf("KSKs len = %d, want 1", got)
	}
	if got := len(n.ZSKs()); got != 1 {
		t.Fatalf("ZSKs len = %d, want 1", got)
	}
}

func TestLoadKeysEmptyIsAllowed(t *testing.T) {
	// While the plugin is in noop mode (steps 1–3) an empty key list
	// must not error — only when ServeDNS actually signs will the
	// zero-key configuration become a hard error.
	n := &Nsec3Sign{}
	if err := n.loadKeys(); err != nil {
		t.Fatalf("loadKeys on empty: %v", err)
	}
	if n.Keys != nil {
		t.Fatalf("expected nil Keys, got %v", n.Keys)
	}
}

func TestLoadKeyErrors(t *testing.T) {
	t.Run("missing public", func(t *testing.T) {
		_, err := loadKey(filepath.Join(t.TempDir(), "does-not-exist"))
		if err == nil || !strings.Contains(err.Error(), "does-not-exist.key") {
			t.Fatalf("expected open error mentioning .key, got %v", err)
		}
	})

	t.Run("public not a DNSKEY", func(t *testing.T) {
		dir := t.TempDir()
		base := filepath.Join(dir, "Kbad.+013+99999")
		// Drop an A record into the .key file — a syntactically valid
		// RR that isn't a DNSKEY. loadKey should refuse this rather
		// than panic on the type assertion.
		if err := os.WriteFile(base+".key", []byte("example.test. IN A 10.0.0.1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(base+".private", []byte("Private-key-format: v1.3\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := loadKey(base)
		if err == nil || !strings.Contains(err.Error(), "expected DNSKEY") {
			t.Fatalf("expected 'expected DNSKEY' error, got %v", err)
		}
	})

	t.Run("missing private", func(t *testing.T) {
		dir := t.TempDir()
		// Generate just the public half so the .key exists but the
		// .private doesn't.
		dk, _ := generateDNSKEY(t, "example.test.", dns.ECDSAP256SHA256, 256, 256)
		base := filepath.Join(dir, "Konly-pub")
		if err := os.WriteFile(base+".key", []byte(dk.String()+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := loadKey(base)
		if err == nil || !strings.Contains(err.Error(), ".private") {
			t.Fatalf("expected error mentioning .private, got %v", err)
		}
	})
}

// generateDNSKEY produces a fresh DNSKEY RR + private key for tests.
// Centralized so every test uses the same owner-name / TTL / flags
// shape; the per-test variation is just the algorithm + role.
func generateDNSKEY(t *testing.T, owner string, alg uint8, bits int, flags uint16) (*dns.DNSKEY, crypto.PrivateKey) {
	t.Helper()
	dk := &dns.DNSKEY{
		Hdr:       dns.RR_Header{Name: owner, Rrtype: dns.TypeDNSKEY, Class: dns.ClassINET, Ttl: 3600},
		Flags:     flags,
		Protocol:  3,
		Algorithm: alg,
	}
	priv, err := dk.Generate(bits)
	if err != nil {
		t.Fatalf("Generate(%d): %v", bits, err)
	}
	return dk, priv
}

// writeKeyPair generates a key, renders both halves to BIND format,
// drops them into a temp directory, and returns the basename. The
// returned basename is suitable for direct use with loadKey.
func writeKeyPair(t *testing.T, owner string, alg uint8, bits int, flags uint16) string {
	t.Helper()
	dk, priv := generateDNSKEY(t, owner, alg, bits, flags)

	dir := t.TempDir()
	// Use a stable filename so any t.Logf shows up readable.
	// miekg/dns doesn't compute the keytag the same way for KSK vs
	// ZSK before flags are set, so we pull it from the populated RR.
	base := filepath.Join(dir, "K"+strings.TrimSuffix(owner, ".")+".+"+
		formatAlg(alg)+"+"+formatTag(dk.KeyTag()))

	// .key — single DNSKEY RR in presentation form. miekg/dns adds
	// the trailing newline automatically.
	if err := os.WriteFile(base+".key", []byte(dk.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// .private — miekg/dns has DNSKEY.PrivateKeyString(priv) for the
	// BIND-private rendering.
	if err := os.WriteFile(base+".private", []byte(dk.PrivateKeyString(priv)), 0o600); err != nil {
		t.Fatal(err)
	}

	return base
}

func formatAlg(a uint8) string {
	const digits = "0123456789"
	return string([]byte{digits[a/100], digits[(a/10)%10], digits[a%10]})
}

func formatTag(t uint16) string {
	const digits = "0123456789"
	return string([]byte{
		digits[t/10000],
		digits[(t/1000)%10],
		digits[(t/100)%10],
		digits[(t/10)%10],
		digits[t%10],
	})
}
