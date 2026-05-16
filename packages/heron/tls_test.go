package main

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"
)

const testCAPEM = `-----BEGIN CERTIFICATE-----
MIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dy7WTAKBggqhkjOPQQDAjASMRAw
DgYDVQQKEwdBY21lIENvMB4XDTE3MTAyMDE5NDMwNloXDTE4MTAyMDE5NDMwNlow
EjEQMA4GA1UEChMHQWNtZSBDbzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABD0d
7VNhbWvZLWPuj/RtHFjvtJBEwOkhbN/BnnE8rnZR8+sbwnc/KhCk3FhnpHZnQz7B
5aETbbIgmuvewdjvSBSjYzBhMA4GA1UdDwEB/wQEAwICpDATBgNVHSUEDDAKBggr
BgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MCkGA1UdEQQiMCCCDmxvY2FsaG9zdDo1
NDUzgg4xMjcuMC4wLjE6NTQ1MzAKBggqhkjOPQQDAgNIADBFAiEA2zpJEPQyz6/l
Wf86aX6PepsntZv2GYlA5UpabfT2EZICICpJ5h/iI+i341gBmLiAFQOyTDT+/wQc
6MF9+Yw1Yy0t
-----END CERTIFICATE-----
`

func TestBuildTLSConfig(t *testing.T) {
	dir := t.TempDir()

	t.Run("no client CA = server-tls-only", func(t *testing.T) {
		cfg, err := buildTLSConfig("", false)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if cfg.ClientAuth != tls.NoClientCert {
			t.Fatalf("want NoClientCert, got %v", cfg.ClientAuth)
		}
		if cfg.ClientCAs != nil {
			t.Fatal("client CA pool should be nil")
		}
	})

	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, []byte(testCAPEM), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("client CA without require = verify-if-given", func(t *testing.T) {
		cfg, err := buildTLSConfig(caPath, false)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if cfg.ClientAuth != tls.VerifyClientCertIfGiven {
			t.Fatalf("want VerifyClientCertIfGiven, got %v", cfg.ClientAuth)
		}
		if cfg.ClientCAs == nil {
			t.Fatal("ClientCAs pool not populated")
		}
	})

	t.Run("client CA + require = require-and-verify", func(t *testing.T) {
		cfg, err := buildTLSConfig(caPath, true)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
			t.Fatalf("want RequireAndVerifyClientCert, got %v", cfg.ClientAuth)
		}
	})

	t.Run("bad PEM is an error", func(t *testing.T) {
		bad := filepath.Join(dir, "bad.pem")
		if err := os.WriteFile(bad, []byte("not a cert"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := buildTLSConfig(bad, false); err == nil {
			t.Fatal("expected error for invalid PEM")
		}
	})

	t.Run("missing file is an error", func(t *testing.T) {
		if _, err := buildTLSConfig(filepath.Join(dir, "nope.pem"), false); err == nil {
			t.Fatal("expected error for missing file")
		}
	})
}
