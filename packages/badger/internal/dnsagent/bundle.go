// Package dnsagent runs the on-site DNS bundle agent + Prometheus
// scraper + dnstap top-K + GoBGP anycast reconciler. Port of
// collector/src/dcim_collector/dns_agent.py.
//
// One goroutine family per configured DnsServer:
//   - serverLoop:    fetch bundle, apply on etag change, signal reload
//   - metricsLoop:   scrape Prom endpoint, delta + percentile, POST
//   - dnstapLoop:    listen on resolver dnstap socket, fold to top-K
//   - advertiseLoop: keep gobgpd's RIB matched to desired prefixes
//
// The dnstap reservoir + the metrics loop coordinate through the
// package-level top-K state in topk.go.
package dnsagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/usg-dcim/packages/badger/internal/config"
)

// Bundle is the JSON payload central renders per DnsServer. We only
// pull out the fields the agent acts on; unknown keys (versioning,
// future schema) are ignored.
type Bundle struct {
	Etag             string            `json:"etag"`
	Engine           string            `json:"engine"`
	Corefile         string            `json:"corefile"`
	Zones            map[string]string `json:"zones"`
	KeyFiles         map[string]string `json:"key_files"`
	GoBGP            any               `json:"gobgp"`
	AnycastPrefixes  []string          `json:"anycast_prefixes"`
}

const (
	corefileName       = "Corefile"
	hickoryConfigName  = "config.toml"
)

func configFilename(engine string) string {
	if engine == "hickory" {
		return hickoryConfigName
	}
	return corefileName
}

// fetchBundle GETs the bundle. 304 responses (server signals via the
// etag query param that nothing has changed) come back as nil, nil so
// the caller skips the apply step. 404 means the server row was
// deleted — log + sleep until it comes back or the operator removes
// the server from the collector config.
func fetchBundle(ctx context.Context, c *http.Client, apiBase, serverID, etag, token string) (*Bundle, error) {
	url := fmt.Sprintf("%s/api/v1/dns/servers/%s/bundle", apiBase, serverID)
	if etag != "" {
		url += "?etag=" + etag
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, errServerMissing
	}
	if resp.StatusCode == 304 {
		return nil, nil
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("bundle status %d", resp.StatusCode)
	}
	var b Bundle
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		return nil, fmt.Errorf("bundle decode: %w", err)
	}
	return &b, nil
}

// errServerMissing is sentinel for the 404 case so the loop can log
// once and back off instead of treating it as a hard failure.
var errServerMissing = fmt.Errorf("dns server row not found")

func postStatus(ctx context.Context, c *http.Client, apiBase, serverID, token string, status, errMsg, etag string) {
	url := fmt.Sprintf("%s/api/v1/dns/servers/%s/render-status", apiBase, serverID)
	payload := map[string]any{"status": status, "etag": etag}
	if errMsg != "" {
		if len(errMsg) > 1500 {
			errMsg = errMsg[:1500]
		}
		payload["error"] = errMsg
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// applyBundle materializes the bundle on disk:
//   <output_dir>/Corefile     (CoreDNS) or config.toml (Hickory)
//   <output_dir>/zones/*.zone (one per zone, atomic)
//   <output_dir>/keys/*       (DNSSEC keys, 0600 on .private)
//   <output_dir>/gobgp.yaml   (recursive only)
//
// Stale engine config (a leftover Corefile when the fabric just moved
// to Hickory) is removed so the resolver doesn't read it on next start.
func applyBundle(server *config.DNSServerConfig, b *Bundle) error {
	out := server.OutputDir
	engine := b.Engine
	if engine == "" {
		engine = "coredns"
	}

	if err := atomicWrite(filepath.Join(out, configFilename(engine)), []byte(b.Corefile)); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	// Drop the other engine's config file if it lingers.
	other := corefileName
	if configFilename(engine) == corefileName {
		other = hickoryConfigName
	}
	_ = os.Remove(filepath.Join(out, other))

	if err := syncDir(filepath.Join(out, "zones"), b.Zones, ".zone"); err != nil {
		return fmt.Errorf("zones: %w", err)
	}
	if err := syncDir(filepath.Join(out, "keys"), b.KeyFiles, ""); err != nil {
		return fmt.Errorf("keys: %w", err)
	}
	// 0600 the DNSSEC .private halves.
	for name := range b.KeyFiles {
		if filepath.Ext(name) == ".private" {
			_ = os.Chmod(filepath.Join(out, "keys", name), 0o600)
		}
	}
	if server.Role == "recursive" && b.GoBGP != nil {
		raw, err := yaml.Marshal(b.GoBGP)
		if err != nil {
			return fmt.Errorf("marshal gobgp.yaml: %w", err)
		}
		if err := atomicWrite(filepath.Join(out, "gobgp.yaml"), raw); err != nil {
			return fmt.Errorf("write gobgp.yaml: %w", err)
		}
	}
	return nil
}

// atomicWrite writes-then-renames so a consumer never sees a half-
// written file. Critical for CoreDNS — a torn zone file fails to parse
// and stalls the reload.
func atomicWrite(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// syncDir writes each (name, contents) into dir atomically and removes
// any pre-existing files in dir that aren't in the new set. When
// `suffix` is non-empty, only files with that suffix are eligible for
// cleanup so unrelated siblings survive.
func syncDir(dir string, files map[string]string, suffix string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	keep := make(map[string]struct{}, len(files))
	for name := range files {
		keep[name] = struct{}{}
	}
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if _, k := keep[e.Name()]; k {
				continue
			}
			if suffix != "" && filepath.Ext(e.Name()) != suffix {
				continue
			}
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	for name, text := range files {
		if err := atomicWrite(filepath.Join(dir, name), []byte(text)); err != nil {
			return err
		}
	}
	return nil
}
