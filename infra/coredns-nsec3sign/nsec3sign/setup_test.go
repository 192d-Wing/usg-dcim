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

func TestParseValid(t *testing.T) {
	cases := []struct {
		name  string
		input string
		check func(t *testing.T, n *Nsec3Sign)
	}{
		{
			name:  "minimal",
			input: `nsec3sign`,
			check: func(t *testing.T, n *Nsec3Sign) {
				if len(n.KeyFiles) != 0 {
					t.Fatalf("expected no key files, got %v", n.KeyFiles)
				}
				if n.Salt != "" || n.Iterations != 0 || n.OptOut {
					t.Fatalf("expected RFC 9276 defaults, got salt=%q iter=%d optOut=%v",
						n.Salt, n.Iterations, n.OptOut)
				}
				if n.CacheCapacity != 10000 {
					t.Fatalf("expected default cache_capacity=10000, got %d", n.CacheCapacity)
				}
			},
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
			check: func(t *testing.T, n *Nsec3Sign) {
				if len(n.KeyFiles) != 2 {
					t.Fatalf("expected 2 key files, got %d", len(n.KeyFiles))
				}
				if n.Salt != "deadbeef" {
					t.Fatalf("expected salt=deadbeef, got %q", n.Salt)
				}
				if !n.OptOut {
					t.Fatal("expected opt_out=true")
				}
				if n.CacheCapacity != 5000 {
					t.Fatalf("expected cache_capacity=5000, got %d", n.CacheCapacity)
				}
			},
		},
		{
			name: "empty salt spellings",
			input: `nsec3sign {
				salt ""
			}`,
			check: func(t *testing.T, n *Nsec3Sign) {
				if n.Salt != "" {
					t.Fatalf("expected empty salt, got %q", n.Salt)
				}
			},
		},
		{
			name: "dash salt spelling",
			input: `nsec3sign {
				salt -
			}`,
			check: func(t *testing.T, n *Nsec3Sign) {
				if n.Salt != "" {
					t.Fatalf("expected empty salt, got %q", n.Salt)
				}
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
			tc.check(t, n)
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
