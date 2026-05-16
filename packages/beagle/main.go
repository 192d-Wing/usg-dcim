// DNS health-check probe service — Go port of the `dns_health_checks`
// cron in backend/src/dcim/worker.py + probe_health_check in services/dns.py.
//
// Every interval, scan dns_health_checks for rows whose last_checked_at
// is older than interval_seconds * 1.5 (or NULL), probe them
// concurrently, and write status + last_checked_at + last_error back.
// Records bound to an unhealthy check get dropped from the rendered
// zone — that's done by the Python renderer; we only own the probe
// loop here.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	probing "github.com/prometheus-community/pro-bing"

	"github.com/usg-dcim/packages/shared-go/env"
)

type check struct {
	id              string
	targetIP        string
	protocol        string
	port            *int
	path            string
	timeoutSecs     int
	intervalSecs    int
	lastCheckedAt   *time.Time
	currentStatus   string
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	pgDSN := env.String("DCIM_POSTGRES_DSN_RAW", "postgres://dcim:dcim@postgres:5432/dcim")
	loopEvery := env.Duration("DNS_PROBE_TICK", 30*time.Second)
	concurrency := env.Int("DNS_PROBE_CONCURRENCY", 64)

	pg, err := pgxpool.New(context.Background(), pgDSN)
	if err != nil {
		log.Error("pg_connect_failed", "err", err)
		os.Exit(1)
	}
	defer pg.Close()

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
		_ = http.ListenAndServe(env.String("DNS_PROBE_HEALTH_ADDR", ":8102"), mux)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := &prober{pg: pg, log: log, sem: make(chan struct{}, concurrency)}

	t := time.NewTicker(loopEvery)
	defer t.Stop()
	log.Info("dns_probe_running", "tick", loopEvery.String(), "concurrency", concurrency)
	p.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.runOnce(ctx)
		}
	}
}

type prober struct {
	pg  *pgxpool.Pool
	log *slog.Logger
	sem chan struct{}
}

func (p *prober) runOnce(ctx context.Context) {
	due, err := p.loadDue(ctx)
	if err != nil {
		p.log.Error("load_due_failed", "err", err)
		return
	}
	if len(due) == 0 {
		return
	}

	var wg sync.WaitGroup
	changed := 0
	var mu sync.Mutex

	for _, c := range due {
		c := c
		wg.Add(1)
		p.sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-p.sem }()
			status, errMsg := p.probe(ctx, c)
			if status != c.currentStatus {
				mu.Lock()
				changed++
				mu.Unlock()
			}
			now := time.Now().UTC()
			if _, err := p.pg.Exec(ctx, `
				UPDATE dns_health_checks
				SET status=$1::dns_health_check_status,
				    last_checked_at=$2,
				    last_error=$3,
				    updated_at=NOW()
				WHERE id=$4
			`, status, now, nullableStr(errMsg), c.id); err != nil {
				p.log.Warn("status_update_failed", "id", c.id, "err", err)
			}
		}()
	}
	wg.Wait()
	p.log.Info("dns_probe_cycle", "probed", len(due), "changed", changed)
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// loadDue mirrors central_health_checks_due — every enabled check whose
// last_checked_at is older than interval_seconds * 1.5 or NULL. We push
// the staleness predicate into SQL so a busy table doesn't haul every
// row over the wire.
func (p *prober) loadDue(ctx context.Context) ([]check, error) {
	rows, err := p.pg.Query(ctx, `
		SELECT id::text,
		       host(target_ip),
		       protocol::text,
		       port,
		       path,
		       timeout_seconds,
		       interval_seconds,
		       last_checked_at,
		       status::text
		FROM dns_health_checks
		WHERE enabled = TRUE
		  AND (
		    last_checked_at IS NULL
		    OR last_checked_at < NOW() - make_interval(secs => interval_seconds * 1.5)
		  )
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []check
	for rows.Next() {
		var c check
		if err := rows.Scan(&c.id, &c.targetIP, &c.protocol, &c.port, &c.path,
			&c.timeoutSecs, &c.intervalSecs, &c.lastCheckedAt, &c.currentStatus); err != nil {
			return nil, err
		}
		if c.timeoutSecs <= 0 {
			c.timeoutSecs = 5
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (p *prober) probe(ctx context.Context, c check) (string, string) {
	timeout := time.Duration(c.timeoutSecs) * time.Second
	switch c.protocol {
	case "icmp":
		return probeICMP(c.targetIP, timeout)
	case "tcp":
		port := 0
		if c.port != nil {
			port = *c.port
		}
		return probeTCP(c.targetIP, port, timeout)
	case "http":
		port := 80
		if c.port != nil {
			port = *c.port
		}
		return probeHTTP(ctx, "http", c.targetIP, port, c.path, timeout, false)
	case "https":
		port := 443
		if c.port != nil {
			port = *c.port
		}
		return probeHTTP(ctx, "https", c.targetIP, port, c.path, timeout, true)
	default:
		return "unhealthy", "unknown protocol " + c.protocol
	}
}

func probeICMP(target string, timeout time.Duration) (string, string) {
	pinger, err := probing.NewPinger(target)
	if err != nil {
		return "unhealthy", err.Error()
	}
	// Try unprivileged ICMP first; fall back to raw if explicitly enabled.
	pinger.SetPrivileged(env.Bool("DNS_PROBE_ICMP_PRIVILEGED", false))
	pinger.Count = 1
	pinger.Timeout = timeout
	if err := pinger.Run(); err != nil {
		return "unhealthy", err.Error()
	}
	stats := pinger.Statistics()
	if stats.PacketsRecv > 0 {
		return "healthy", ""
	}
	return "unhealthy", "no reply"
}

func probeTCP(target string, port int, timeout time.Duration) (string, string) {
	if port <= 0 {
		return "unhealthy", "tcp requires port"
	}
	addr := net.JoinHostPort(target, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return "unhealthy", err.Error()
	}
	_ = conn.Close()
	return "healthy", ""
}

func probeHTTP(ctx context.Context, scheme, target string, port int, path string, timeout time.Duration, tlsOn bool) (string, string) {
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	url := fmt.Sprintf("%s://%s:%d%s", scheme, target, port, path)
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: tlsOn}, // probes hit infra IPs; cert pinning out of scope here
		},
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "unhealthy", err.Error()
	}
	resp, err := client.Do(req)
	if err != nil {
		return "unhealthy", err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return "healthy", ""
	}
	return "unhealthy", fmt.Sprintf("http %d", resp.StatusCode)
}

