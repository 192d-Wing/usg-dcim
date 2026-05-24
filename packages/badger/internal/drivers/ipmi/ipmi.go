// Package ipmi implements the IPMI driver. Strategy: shell out to
// `ipmitool sdr` and parse its pipe-delimited output. Matches what the
// Python driver does internally (pyipmi → ipmitool interface) without
// pulling in a fragile pure-Go IPMI stack.
//
// ipmitool sdr output looks like:
//
//	CPU1 Temp        | 35 degrees C      | ok
//	Inlet Temp       | 22 degrees C      | ok
//	Fan1A            | 8400 RPM          | ok
//	System Power     | 245 Watts         | ok
//
// We split on `|`, strip whitespace, and pull the first numeric token
// out of the middle field. Sensors whose middle field doesn't start
// with a numeric value (e.g. "Presence Detected") are skipped.
package ipmi

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/usg-dcim/packages/badger/internal/buffer"
	"github.com/usg-dcim/packages/badger/internal/config"
)

type Driver struct {
	dev *config.Device
	log *slog.Logger
}

func New(d *config.Device, log *slog.Logger) *Driver {
	return &Driver{dev: d, log: log.With("driver", "ipmi", "asset", d.AssetID.String())}
}

func (*Driver) Kind() string { return "ipmi" }

func (d *Driver) Poll(ctx context.Context, buf *buffer.Buffer) error {
	cfg := d.dev.IPMI
	if cfg == nil || cfg.Host == "" {
		return nil
	}
	password := cfg.Password
	if password == "" {
		password = cfg.PasswordRef // best-effort; full secret-ref support lands later
	}
	pollCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(pollCtx, "ipmitool",
		"-I", "lanplus",
		"-H", cfg.Host,
		"-U", cfg.Username,
		"-P", password,
		"sdr",
	)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("ipmitool: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	samples := make([]buffer.Sample, 0, 16)
	for _, line := range strings.Split(string(out), "\n") {
		s, ok := parseLine(line, d.dev.AssetID.String(), cfg.Host, now)
		if !ok {
			continue
		}
		samples = append(samples, s)
	}
	if len(samples) == 0 {
		return nil
	}
	if err := buf.EnqueueBatch(ctx, samples); err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	d.log.Info("ipmi_poll_ok", "count", len(samples))
	return nil
}

// parseLine pulls a sample out of one `ipmitool sdr` line. Returns
// ok=false for malformed lines, non-numeric readings, and disabled
// sensors. Metric name mirrors the Python driver:
//
//	"ipmi." + name.lower().replace(' ', '_')
func parseLine(line, assetID, host, ts string) (buffer.Sample, bool) {
	parts := strings.Split(line, "|")
	if len(parts) < 2 {
		return buffer.Sample{}, false
	}
	name := strings.TrimSpace(parts[0])
	reading := strings.TrimSpace(parts[1])
	if name == "" || reading == "" {
		return buffer.Sample{}, false
	}
	// First whitespace-separated token of the reading is the numeric
	// value; anything beyond is the unit ("degrees C", "RPM", etc.).
	fields := strings.Fields(reading)
	if len(fields) == 0 {
		return buffer.Sample{}, false
	}
	val, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return buffer.Sample{}, false
	}
	metric := "ipmi." + strings.ReplaceAll(strings.ToLower(name), " ", "_")
	return buffer.Sample{
		AssetID: assetID,
		Metric:  metric,
		Value:   val,
		Ts:      ts,
		Tags:    map[string]string{"host": host, "sensor": name},
	}, true
}
