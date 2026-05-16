package dnsagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/usg-dcim/packages/badger/internal/config"
	"github.com/usg-dcim/packages/badger/internal/dnstap"
	"github.com/usg-dcim/packages/badger/internal/runtime"
)

// Run spawns one loop family per configured DnsServer and returns when
// ctx is cancelled. Mirrors collector/dns_agent.py::run_dns_agent.
//
// Loops per server:
//   - serverLoop:    poll bundle, apply on etag change
//   - metricsLoop:   scrape Prom endpoint, POST delta + top-K
//   - dnstapLoop:    listen on dnstap socket, fold into top-K
//   - advertiseLoop: reconcile gobgpd RIB to bundle's anycast prefixes
//
// All return cleanly when ctx is cancelled. metricsLoop + dnstapLoop
// + advertiseLoop are skipped per-server when their preconditions
// aren't met (metrics_enabled=false, no dnstap socket, no gobgp host).
func Run(ctx context.Context, cfg *config.Config, token string, rt *runtime.Config, log *slog.Logger) {
	if !cfg.DNS.Enabled || len(cfg.DNS.Servers) == 0 {
		log.Info("dns_agent_disabled")
		<-ctx.Done()
		return
	}
	apiBase := cfg.APIBase()
	client := &http.Client{Timeout: 30 * time.Second}
	anycast := newAnycastState()
	reservoir := newTopK()

	var wg sync.WaitGroup
	for i := range cfg.DNS.Servers {
		s := &cfg.DNS.Servers[i]
		slog := log.With("server_id", s.ID.String(), "role", s.Role)

		wg.Add(1)
		go func() {
			defer wg.Done()
			serverLoop(ctx, cfg, s, apiBase, client, token, anycast, slog)
		}()
		if cfg.DNS.MetricsEnabled && s.MetricsOn() && s.MetricsURL != "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				metricsLoop(ctx, cfg, s, apiBase, client, token, reservoir, rt, slog)
			}()
		}
		if s.DnstapSocket != "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				dnstapLoop(ctx, s, reservoir, slog)
			}()
		}
		if s.Role == "recursive" && s.GoBGPAPIHost != "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				advertiseLoop(ctx, s, anycast, slog)
			}()
		}
	}
	wg.Wait()
}

// serverLoop polls the bundle, applies on etag change, posts status.
func serverLoop(
	ctx context.Context,
	cfg *config.Config, s *config.DNSServerConfig,
	apiBase string, client *http.Client, token string,
	anycast *anycastState, log *slog.Logger,
) {
	log.Info("dns_agent_server_start", "output", s.OutputDir)
	interval := cfg.DNS.PollInterval()
	var lastEtag string
	for {
		err := serverCycle(ctx, s, apiBase, client, token, anycast, &lastEtag, log)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Warn("dns_agent_cycle_failed", "err", err)
			postStatus(ctx, client, apiBase, s.ID.String(), token, "error", err.Error(), lastEtag)
		}
		if ctxSleep(ctx, interval) {
			return
		}
	}
}

func serverCycle(
	ctx context.Context, s *config.DNSServerConfig,
	apiBase string, client *http.Client, token string,
	anycast *anycastState, lastEtag *string, log *slog.Logger,
) error {
	bundle, err := fetchBundle(ctx, client, apiBase, s.ID.String(), *lastEtag, token)
	if err != nil {
		if errors.Is(err, errServerMissing) {
			log.Warn("dns_server_missing")
			return nil
		}
		return err
	}
	if bundle == nil { // 304 — no change
		return nil
	}
	if bundle.Etag == *lastEtag {
		return nil
	}
	engine := bundle.Engine
	if engine == "" {
		engine = "coredns"
	}
	if err := applyBundle(s, bundle); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	resolverOK, gobgpOK := signalReloads(s, engine)
	anycast.set(s.ID.String(), bundle.AnycastPrefixes)
	*lastEtag = bundle.Etag
	log.Info("dns_bundle_applied",
		"engine", engine, "etag", bundle.Etag,
		"resolver_reloaded", resolverOK, "gobgp_reloaded", gobgpOK,
	)
	postStatus(ctx, client, apiBase, s.ID.String(), token, "ok", "", bundle.Etag)
	return nil
}

// metricsLoop scrapes the server's Prom endpoint, computes per-interval
// deltas + latency percentiles, snapshots the dnstap top-K, and POSTs
// to central. First cycle establishes the baseline and posts nothing.
// Reads dns_metrics_interval_seconds from rt each iteration so a
// runtime override (set in the UI, pushed via heartbeat) lands on the
// next cycle without restarting the loop.
func metricsLoop(
	ctx context.Context, cfg *config.Config, s *config.DNSServerConfig,
	apiBase string, client *http.Client, token string,
	reservoir *topK, rt *runtime.Config, log *slog.Logger,
) {
	log.Info("dns_metrics_loop_start", "metrics_url", s.MetricsURL)
	yamlDefault := cfg.DNS.MetricsInterval()
	scrapeClient := &http.Client{Timeout: 10 * time.Second}
	var prev *promSnapshot
	for {
		interval := yamlDefault
		if rt != nil {
			interval = rt.DNSMetricsInterval(yamlDefault)
		}
		snap, err := scrapePromSnapshot(ctx, scrapeClient, s.MetricsURL)
		if err != nil {
			log.Warn("dns_metrics_cycle_failed", "err", err)
			prev = nil
		} else if prev == nil {
			prev = snap
		} else {
			postMetrics(ctx, client, apiBase, s, token, prev, snap, reservoir, int(interval.Seconds()), log)
			prev = snap
		}
		if ctxSleep(ctx, interval) {
			return
		}
	}
}

func scrapePromSnapshot(ctx context.Context, c *http.Client, url string) (*promSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("scrape status %d", resp.StatusCode)
	}
	buf := bytes.Buffer{}
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	return parsePromText(buf.String()), nil
}

func postMetrics(
	ctx context.Context, c *http.Client, apiBase string,
	s *config.DNSServerConfig, token string,
	prev, snap *promSnapshot, reservoir *topK, intervalSecs int,
	log *slog.Logger,
) {
	delta := map[string]any{
		"interval_seconds": intervalSecs,
		"queries":          clampPos(snap.RequestsTotal - prev.RequestsTotal),
		"noerror":          clampPos(snap.NoError - prev.NoError),
		"nxdomain":         clampPos(snap.NXDomain - prev.NXDomain),
		"servfail":         clampPos(snap.ServFail - prev.ServFail),
	}
	if p50, ok := percentileFromBuckets(snap.DurationBuckets, snap.DurationCount, 0.50); ok {
		delta["p50_ms"] = p50
	} else {
		delta["p50_ms"] = nil
	}
	if p95, ok := percentileFromBuckets(snap.DurationBuckets, snap.DurationCount, 0.95); ok {
		delta["p95_ms"] = p95
	} else {
		delta["p95_ms"] = nil
	}
	if s.DnstapSocket != "" {
		delta["top_names"] = reservoir.snapshot(s.ID.String())
	} else {
		delta["top_names"] = nil
	}

	body, _ := json.Marshal(delta)
	url := fmt.Sprintf("%s/api/v1/dns/servers/%s/metrics", apiBase, s.ID.String())
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
		log.Warn("dns_metrics_push_failed", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		log.Warn("dns_metrics_push_failed", "status", resp.StatusCode)
	}
}

func clampPos(d int64) int64 {
	if d < 0 {
		return 0
	}
	return d
}

// dnstapLoop runs the fstrm listener forever for one server, feeding
// decoded queries into the shared reservoir. Restarts on unexpected
// errors with a 2s back-off; ctx cancellation bubbles cleanly.
func dnstapLoop(ctx context.Context, s *config.DNSServerConfig, reservoir *topK, log *slog.Logger) {
	log.Info("dnstap_loop_start", "socket", s.DnstapSocket)
	id := s.ID.String()
	onQuery := func(name, qtype string) {
		reservoir.record(id, name, qtype)
	}
	for {
		err := dnstap.Serve(ctx, s.DnstapSocket, onQuery, log)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Warn("dnstap_loop_restart", "err", err)
		}
		if ctxSleep(ctx, 2*time.Second) {
			return
		}
	}
}

// advertiseLoop reconciles gobgpd's RIB to the bundle's most-recent
// desired prefixes every 30s. A drift (operator removes a prefix by
// hand) gets healed on the next tick.
func advertiseLoop(ctx context.Context, s *config.DNSServerConfig, anycast *anycastState, log *slog.Logger) {
	log.Info("advertise_loop_start", "gobgp_api_host", s.GoBGPAPIHost)
	for {
		desired := anycast.get(s.ID.String())
		added, removed, errs := reconcileAdvertise(ctx, s, desired)
		if len(added) > 0 || len(removed) > 0 || len(errs) > 0 {
			log.Info("anycast_reconcile",
				"desired", desired, "added", added, "removed", removed, "errors", errs,
			)
		}
		if ctxSleep(ctx, 30*time.Second) {
			return
		}
	}
}

// ctxSleep returns true if the context was cancelled while waiting.
func ctxSleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}
