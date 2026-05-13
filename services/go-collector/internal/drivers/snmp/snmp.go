// Package snmp implements the SNMP poller. Parity with
// collector/src/dcim_collector/drivers/snmp.py:
//   - iterate OIDs from device.snmp.oids
//   - issue an SNMP GET per OID
//   - coerce the response value to float64; skip non-numeric
//   - enqueue {asset_id, metric, value, ts, tags{oid, host}}
//
// gosnmp is the only Go SNMP library with broad enough coverage to be
// worth using. Connection setup is per-poll so a slow/stuck device
// can't wedge other polls; per-OID timeout keeps the loop honest.
package snmp

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/usg-dcim/services/go-collector/internal/buffer"
	"github.com/usg-dcim/services/go-collector/internal/config"
)

type Driver struct {
	dev *config.Device
	log *slog.Logger
}

func New(d *config.Device, log *slog.Logger) *Driver {
	return &Driver{dev: d, log: log.With("driver", "snmp", "asset", d.AssetID.String())}
}

func (*Driver) Kind() string { return "snmp" }

func (d *Driver) Poll(ctx context.Context, buf *buffer.Buffer) error {
	cfg := d.dev.SNMP
	if cfg == nil || len(cfg.OIDs) == 0 {
		return nil
	}

	g, err := dial(cfg)
	if err != nil {
		return fmt.Errorf("snmp dial %s:%d: %w", cfg.Host, cfg.Port, err)
	}
	defer g.Conn.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	samples := make([]buffer.Sample, 0, len(cfg.OIDs))

	for metric, oid := range cfg.OIDs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resp, err := g.Get([]string{oid})
		if err != nil {
			d.log.Warn("snmp_get_failed", "metric", metric, "oid", oid, "err", err)
			continue
		}
		if len(resp.Variables) == 0 {
			d.log.Warn("snmp_empty_response", "metric", metric, "oid", oid)
			continue
		}
		v := resp.Variables[0]
		num, ok := coerce(v)
		if !ok {
			d.log.Debug("snmp_non_numeric", "metric", metric, "oid", oid, "type", fmt.Sprintf("%T", v.Value))
			continue
		}
		samples = append(samples, buffer.Sample{
			AssetID: d.dev.AssetID.String(),
			Metric:  metric,
			Value:   num,
			Ts:      now,
			Tags:    map[string]string{"oid": oid, "host": cfg.Host},
		})
	}

	if len(samples) == 0 {
		return nil
	}
	if err := buf.EnqueueBatch(ctx, samples); err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	d.log.Info("snmp_poll_ok", "count", len(samples))
	return nil
}

func dial(cfg *config.SNMPConfig) (*gosnmp.GoSNMP, error) {
	port := cfg.Port
	if port == 0 {
		port = 161
	}
	community := cfg.Community
	if community == "" {
		community = "public"
	}
	g := &gosnmp.GoSNMP{
		Target:    cfg.Host,
		Port:      uint16(port),
		Community: community,
		Version:   gosnmp.Version2c,
		Timeout:   3 * time.Second,
		Retries:   1,
	}
	switch cfg.Version {
	case "1":
		g.Version = gosnmp.Version1
	case "2c", "":
		g.Version = gosnmp.Version2c
	case "3":
		return nil, fmt.Errorf("snmp v3 requires auth config not yet supported")
	}
	if err := g.Connect(); err != nil {
		return nil, err
	}
	return g, nil
}

// coerce squeezes whatever gosnmp returned into a float64. Counter32 /
// Counter64 / Gauge / Integer / TimeTicks are all numeric. OctetString
// is occasionally a number rendered as ASCII — caller logs and skips.
func coerce(v gosnmp.SnmpPDU) (float64, bool) {
	switch x := v.Value.(type) {
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case float32:
		return float64(x), true
	case float64:
		return x, true
	default:
		return 0, false
	}
}
