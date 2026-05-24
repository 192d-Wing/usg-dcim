// loadgen — synthetic telemetry-ingest load for the DCIM compose stack.
//
// Simulates N concurrent site collectors POSTing batches of samples to
// heron's /api/v1/ingest/telemetry endpoint at a configurable poll
// interval, then reports p50/p95/p99 request latency on a rolling
// window. Intended to feed the Phase 4 "performance test harness"
// item: a way to measure whether the rest of the scale items (alert
// eval rearchitecture, telemetry retention tuning) are actually
// needed.
//
// Defaults match the 184-site fleet target from docs/ROADMAP.md:
//   - 184 collectors
//   - 50 assets each
//   - 4 metrics per asset
//   - 30s poll interval
//   - 5min run
// → ~5M samples/min steady state.
//
// Smaller scales (-collectors 10 -duration 30s) are useful for the
// compose stack on a laptop where the full target rate would saturate
// the local Postgres.
//
// Usage:
//
//	loadgen [-target URL] [-bearer TOK] [-collectors N] [-assets N] \
//	         [-poll D] [-duration D] [-report D]
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	target := flag.String("target", "http://localhost:8081/api/v1/ingest/telemetry",
		"heron ingest URL")
	bearer := flag.String("bearer", "",
		"optional Bearer token for collectors:ingest:write")
	collectors := flag.Int("collectors", 184, "concurrent collectors to simulate")
	assets := flag.Int("assets", 50, "assets per collector")
	metrics := flag.String("metrics", "cpu_pct,mem_pct,disk_pct,temp_c",
		"comma-separated metric names sent each poll")
	poll := flag.Duration("poll", 30*time.Second, "per-collector poll interval")
	duration := flag.Duration("duration", 5*time.Minute, "total run time")
	report := flag.Duration("report", 15*time.Second, "interval between reports")
	maxIdle := flag.Int("max-idle-conns", 256,
		"http transport MaxIdleConns (per-collector connection reuse)")
	flag.Parse()

	if *collectors < 1 || *assets < 1 {
		fmt.Fprintln(os.Stderr, "loadgen: collectors and assets must be >= 1")
		os.Exit(2)
	}
	metricList := strings.Split(*metrics, ",")
	for i := range metricList {
		metricList[i] = strings.TrimSpace(metricList[i])
	}

	// Single shared HTTP client so connections are pooled across all
	// goroutines. MaxIdleConns sized to hold roughly one keepalive per
	// collector at peak.
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        *maxIdle,
			MaxIdleConnsPerHost: *maxIdle,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	t := &tracker{}
	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *duration > 0 {
		var timed context.CancelFunc
		ctx, timed = context.WithTimeout(ctx, *duration)
		defer timed()
	}

	printHeader(*collectors, *assets, metricList, *poll, *duration, *target)

	var wg sync.WaitGroup
	for i := 0; i < *collectors; i++ {
		c := newCollector(*target, *bearer, *assets, metricList, client)
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.run(ctx, *poll, t)
		}()
	}

	// Reporter ticks every -report and on shutdown emits a final
	// summary. Run on the main goroutine so it can exit cleanly when
	// all collectors are done.
	rep := time.NewTicker(*report)
	defer rep.Stop()
	start := time.Now()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-rep.C:
			printSnapshot(t.report(), time.Since(start))
		}
	}
	// Wait for in-flight requests to drain (bounded by client.Timeout).
	wg.Wait()
	final := t.report()
	fmt.Println("\n=== final ===")
	printSnapshot(final, time.Since(start))
	if final.errors > 0 {
		os.Exit(1)
	}
}

func printHeader(c, a int, metrics []string, poll, dur time.Duration, target string) {
	rateHz := float64(c*a*len(metrics)) / poll.Seconds()
	fmt.Printf(`loadgen — %d collectors × %d assets × %d metrics every %s
target:        %s
rated load:    %.0f samples/sec  (%.0f samples/min)
total run:     %s
GOMAXPROCS:    %d

`, c, a, len(metrics), poll, target, rateHz, rateHz*60, dur, runtime.GOMAXPROCS(0))
}

func printSnapshot(s snapshot, elapsed time.Duration) {
	rate := 0.0
	if elapsed > 0 {
		rate = float64(s.n-s.errors) / elapsed.Seconds()
	}
	mb := float64(s.bytes) / (1024 * 1024)
	fmt.Printf("[%5s] reqs=%d errs=%d  p50=%6s p95=%6s p99=%6s max=%6s  rate=%.1f/s  sent=%.1f MB\n",
		elapsed.Round(time.Second), s.n, s.errors,
		s.p50.Round(time.Millisecond),
		s.p95.Round(time.Millisecond),
		s.p99.Round(time.Millisecond),
		s.max.Round(time.Millisecond),
		rate, mb,
	)
}
