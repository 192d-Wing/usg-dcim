// Package redfish implements the Redfish BMC poller. Parity with
// collector/src/dcim_collector/drivers/redfish.py:
//   - GET <base>/redfish/v1/Chassis/1/Thermal — emit temp sensors
//   - GET <base>/redfish/v1/Chassis/1/Power   — emit PowerConsumedWatts
//   - HTTP Basic auth via configured user/password
//
// The Python driver hard-codes Chassis/1 with a TODO to walk Chassis/
// properly. Phase 2 preserves that limitation to keep parity tight;
// a follow-up phase walks the chassis collection and handles multi-
// chassis hosts. PowerSubsystem / ThermalSubsystem (newer Redfish
// schemas) are a separate widening that lands with the chassis walk.
package redfish

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/usg-dcim/services/go-collector/internal/buffer"
	"github.com/usg-dcim/services/go-collector/internal/config"
)

type Driver struct {
	dev    *config.Device
	client *http.Client
	log    *slog.Logger
}

func New(d *config.Device, log *slog.Logger) *Driver {
	cfg := d.Redfish
	tr := &http.Transport{
		// nosemgrep — verify_tls comes from operator-controlled config;
		// the Python collector honours the same flag for self-signed
		// BMC certs in lab environments.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg != nil && !cfg.RedfishVerifyTLS()},
	}
	return &Driver{
		dev:    d,
		client: &http.Client{Timeout: 10 * time.Second, Transport: tr},
		log:    log.With("driver", "redfish", "asset", d.AssetID.String()),
	}
}

func (*Driver) Kind() string { return "redfish" }

type thermalPayload struct {
	Temperatures []struct {
		Name           string  `json:"Name"`
		ReadingCelsius float64 `json:"ReadingCelsius"`
	} `json:"Temperatures"`
}

type powerPayload struct {
	PowerControl []struct {
		PowerConsumedWatts float64 `json:"PowerConsumedWatts"`
	} `json:"PowerControl"`
}

func (d *Driver) Poll(ctx context.Context, buf *buffer.Buffer) error {
	cfg := d.dev.Redfish
	if cfg == nil || cfg.BaseURL == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var samples []buffer.Sample

	var thermal thermalPayload
	if err := d.fetch(ctx, cfg, "/redfish/v1/Chassis/1/Thermal", &thermal); err != nil {
		d.log.Warn("redfish_thermal_fetch_failed", "err", err)
	} else {
		for _, t := range thermal.Temperatures {
			if t.ReadingCelsius == 0 && t.Name == "" {
				continue
			}
			metric := "thermal." + sanitizeName(t.Name) + ".tempC"
			samples = append(samples, buffer.Sample{
				AssetID: d.dev.AssetID.String(),
				Metric:  metric,
				Value:   t.ReadingCelsius,
				Unit:    "C",
				Ts:      now,
				Tags:    map[string]string{"sensor": t.Name},
			})
		}
	}

	var power powerPayload
	if err := d.fetch(ctx, cfg, "/redfish/v1/Chassis/1/Power", &power); err != nil {
		d.log.Warn("redfish_power_fetch_failed", "err", err)
	} else {
		for _, p := range power.PowerControl {
			if p.PowerConsumedWatts == 0 {
				continue
			}
			samples = append(samples, buffer.Sample{
				AssetID: d.dev.AssetID.String(),
				Metric:  "power.consumed.W",
				Value:   p.PowerConsumedWatts,
				Unit:    "W",
				Ts:      now,
			})
		}
	}

	if len(samples) == 0 {
		return nil
	}
	if err := buf.EnqueueBatch(ctx, samples); err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	d.log.Info("redfish_poll_ok", "count", len(samples))
	return nil
}

func (d *Driver) fetch(ctx context.Context, cfg *config.RedfishConfig, path string, out any) error {
	url := strings.TrimRight(cfg.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	if cfg.Username != "" {
		req.SetBasicAuth(cfg.Username, cfg.Password)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// sanitizeName matches the Python driver's `.lower().replace(' ', '_')`
// so the same sensor produces the same metric name in both collectors.
func sanitizeName(name string) string {
	if name == "" {
		return "sensor"
	}
	return strings.ReplaceAll(strings.ToLower(name), " ", "_")
}
