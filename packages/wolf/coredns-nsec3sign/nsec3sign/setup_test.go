// Tests for the Corefile parser. Step-1 coverage targets the
// directive shape — key/salt/iterations/opt_out/cache_capacity — so
// any future change to parse() that breaks the eventual config
// surface trips a test, even though the crypto path isn't wired yet.

package nsec3sign

import (
	"strings"
	"testing"

	"github.com/coredns/caddy"
)

// parseWant captures every Nsec3Sign field a TestParseValid case
// might assert on. Flattening case-specific checks into a single
// struct + one assertion loop keeps the test's cognitive complexity
// bounded as cases accrete — every new case adds one row, not a
// new closure.
type parseWant struct {
	keyFiles      int    // expected len(n.KeyFiles)
	zoneFile      string // expected n.ZoneFile
	salt          string
	iterations    uint16
	optOut        bool
	cacheCapacity int
}

func (w parseWant) check(t *testing.T, n *Nsec3Sign) {
	t.Helper()
	if len(n.KeyFiles) != w.keyFiles {
		t.Errorf("KeyFiles len = %d, want %d", len(n.KeyFiles), w.keyFiles)
	}
	if n.ZoneFile != w.zoneFile {
		t.Errorf("ZoneFile = %q, want %q", n.ZoneFile, w.zoneFile)
	}
	if n.Salt != w.salt {
		t.Errorf("Salt = %q, want %q", n.Salt, w.salt)
	}
	if n.Iterations != w.iterations {
		t.Errorf("Iterations = %d, want %d", n.Iterations, w.iterations)
	}
	if n.OptOut != w.optOut {
		t.Errorf("OptOut = %v, want %v", n.OptOut, w.optOut)
	}
	if n.CacheCapacity != w.cacheCapacity {
		t.Errorf("CacheCapacity = %d, want %d", n.CacheCapacity, w.cacheCapacity)
	}
}

func TestParseValid(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  parseWant
	}{
		{
			name:  "minimal",
			input: `nsec3sign`,
			want:  parseWant{cacheCapacity: 10000},
		},
		{
			name: "full block",
			input: `nsec3sign {
				key file /etc/coredns/keys/Kexample.+013+12345
				key file /etc/coredns/keys/Kexample.+013+67890
				salt deadbeef
				iterations 0
				opt_out
				cache_capacity 5000
			}`,
			want: parseWant{
				keyFiles: 2, salt: "deadbeef", optOut: true,
				cacheCapacity: 5000,
			},
		},
		{
			name: "empty salt spelling",
			input: `nsec3sign {
				salt ""
			}`,
			want: parseWant{cacheCapacity: 10000},
		},
		{
			name: "dash salt spelling",
			input: `nsec3sign {
				salt -
			}`,
			want: parseWant{cacheCapacity: 10000},
		},
		{
			name: "zone file directive",
			input: `nsec3sign {
				zone file /var/lib/dcim-dns/auth/zones/example.test.zone
			}`,
			want: parseWant{
				zoneFile:      "/var/lib/dcim-dns/auth/zones/example.test.zone",
				cacheCapacity: 10000,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := caddy.NewTestController("dns", tc.input)
			n, err := parse(c)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			tc.want.check(t, n)
		})
	}
}

func TestParseInvalid(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name: "unknown directive",
			input: `nsec3sign {
				bogus value
			}`,
			wantErr: `unknown nsec3sign directive "bogus"`,
		},
		{
			name: "key without file",
			input: `nsec3sign {
				key Kexample.+013+12345
			}`,
			wantErr: "Wrong argument count",
		},
		{
			name: "iterations not an int",
			input: `nsec3sign {
				iterations abc
			}`,
			wantErr: "iterations must be a non-negative integer",
		},
		{
			name: "iterations over max",
			input: `nsec3sign {
				iterations 200
			}`,
			wantErr: "exceeds the maximum of 150",
		},
		{
			name: "cache_capacity zero",
			input: `nsec3sign {
				cache_capacity 0
			}`,
			wantErr: "cache_capacity must be a positive integer",
		},
		{
			name: "opt_out with arg",
			input: `nsec3sign {
				opt_out yes
			}`,
			wantErr: "Wrong argument count",
		},
		{
			name: "salt not hex",
			input: `nsec3sign {
				salt deadbeefg
			}`,
			wantErr: "salt must be hex",
		},
		{
			name: "salt odd-length hex",
			input: `nsec3sign {
				salt abc
			}`,
			wantErr: "salt must be hex",
		},
		{
			name: "salt too long",
			// 33 bytes hex (66 chars) — one past the configured 32-byte cap.
			input: `nsec3sign {
				salt 00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff00
			}`,
			wantErr: "exceeds the configured maximum",
		},
		{
			name: "zone without file",
			input: `nsec3sign {
				zone /var/lib/dcim-dns/auth/zones/example.test.zone
			}`,
			wantErr: "Wrong argument count",
		},
		{
			name: "zone file duplicated",
			input: `nsec3sign {
				zone file /a.zone
				zone file /b.zone
			}`,
			wantErr: "one zone file per nsec3sign block",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := caddy.NewTestController("dns", tc.input)
			_, err := parse(c)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
