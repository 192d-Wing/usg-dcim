// go-collector — Phase 1 shell.
//
// Wires config → buffer → forwarder → heartbeat → driver pollers.
// Drivers themselves are stubs in Phase 1; the value of this build is
// proving the cutover path: real config file in, real Authorization
// header out, real batches posted to go-ingest, real heartbeats posted
// to the Python API. Phase 2 swaps in SNMP + Redfish without touching
// anything below.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/usg-dcim/services/go-collector/internal/buffer"
	"github.com/usg-dcim/services/go-collector/internal/config"
	"github.com/usg-dcim/services/go-collector/internal/dnsagent"
	"github.com/usg-dcim/services/go-collector/internal/dnstap"
	"github.com/usg-dcim/services/go-collector/internal/drivers"
	"github.com/usg-dcim/services/go-collector/internal/forwarder"
	"github.com/usg-dcim/services/go-collector/internal/runtime"
)

func main() {
	configPath := flag.String("config", "/etc/dcim/collector.yaml", "path to collector YAML")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("config_load_failed", "err", err, "path", *configPath)
		os.Exit(1)
	}
	log.Info("config_loaded",
		"collector_id", cfg.CollectorID,
		"site_id", cfg.SiteID,
		"devices", len(cfg.Devices),
		"telemetry_endpoint", cfg.TelemetryEndpoint(),
		"heartbeat_endpoint", cfg.HeartbeatEndpoint(),
	)

	token, err := cfg.LoadToken()
	if err != nil {
		log.Error("token_load_failed", "err", err)
		os.Exit(1)
	}
	if token == "" {
		log.Warn("no_api_token", "hint", "set api_token_file in collector.yaml; ingest will 401 without it")
	}

	buf, err := buffer.Open(cfg.BufferPath)
	if err != nil {
		log.Error("buffer_open_failed", "err", err, "path", cfg.BufferPath)
		os.Exit(1)
	}
	defer buf.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Shared atomic config the heartbeat receiver writes and every loop
	// reads. Lets central push interval overrides via the heartbeat
	// response without restarting the collector.
	rt := runtime.New()

	fwd := forwarder.New(cfg, buf, token, log, rt)

	var wg sync.WaitGroup
	// Forwarder
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := fwd.Run(ctx); err != nil && err != context.Canceled {
			log.Error("forwarder_exited", "err", err)
		}
	}()
	// Heartbeat
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := fwd.RunHeartbeat(ctx); err != nil && err != context.Canceled {
			log.Error("heartbeat_exited", "err", err)
		}
	}()

	// Driver pollers — one goroutine per device. Phase 1 stubs log
	// only; Phase 2 starts enqueueing real samples.
	for _, d := range cfg.Devices {
		p, err := drivers.Build(d, log)
		if err != nil {
			log.Warn("driver_unknown", "asset", d.AssetID, "driver", d.Driver, "err", err)
			continue
		}
		dev := d
		wg.Add(1)
		go func() {
			defer wg.Done()
			drivers.Schedule(ctx, p, buf, time.Duration(dev.PollIntervalSecs)*time.Second, rt, log)
		}()
	}

	// dnstap reader: optional, opt-in via dnstap.socket_path. Phase 3
	// just logs decoded (qname, qtype) pairs; the top-K reservoir
	// + metrics POST land in Phase 4.
	if cfg.DNSTap != nil && cfg.DNSTap.SocketPath != "" {
		dlog := log.With("subsys", "dnstap")
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := dnstap.Serve(ctx, cfg.DNSTap.SocketPath, func(name, qtype string) {
				dlog.Debug("dnstap_query", "name", name, "type", qtype)
			}, dlog)
			if err != nil && err != context.Canceled {
				dlog.Error("dnstap_exited", "err", err)
			}
		}()
	}

	// DNS agent (bundle / metrics / dnstap / anycast). Self-no-ops
	// when cfg.dns.enabled = false or no servers are configured.
	wg.Add(1)
	go func() {
		defer wg.Done()
		dnsagent.Run(ctx, cfg, token, rt, log.With("subsys", "dnsagent"))
	}()

	log.Info("collector_running",
		"devices", len(cfg.Devices),
		"dns_servers", len(cfg.DNS.Servers),
	)
	<-ctx.Done()
	log.Info("collector_shutting_down")
	wg.Wait()
	log.Info("collector_stopped")
}
