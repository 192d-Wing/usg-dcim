// Package rest implements the generic JSON-REST driver. Parity with
// collector/src/dcim_collector/drivers/rest.py:
//   - single GET against base_url with operator-supplied headers
//   - dot-paths in cfg.paths resolve into the JSON tree
//   - numeric leaves emit samples; everything else is skipped
//
// One HTTP client per poll matches the Python driver's lifetime. If
// an operator points many devices at the same endpoint we'd want a
// shared client; defer that until a real workload demands it.
package rest

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
	cfg := d.REST
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg != nil && !cfg.VerifyTLSFlag()},
	}
	return &Driver{
		dev:    d,
		client: &http.Client{Timeout: 10 * time.Second, Transport: tr},
		log:    log.With("driver", "rest", "asset", d.AssetID.String()),
	}
}

func (*Driver) Kind() string { return "rest" }

func (d *Driver) Poll(ctx context.Context, buf *buffer.Buffer) error {
	cfg := d.dev.REST
	if cfg == nil || cfg.BaseURL == "" || len(cfg.Paths) == 0 {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", cfg.BaseURL, nil)
	if err != nil {
		return err
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("rest fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("rest status %d", resp.StatusCode)
	}
	var doc any
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("rest decode: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	samples := make([]buffer.Sample, 0, len(cfg.Paths))
	for metric, path := range cfg.Paths {
		v, ok := resolveNumeric(doc, path)
		if !ok {
			continue
		}
		samples = append(samples, buffer.Sample{
			AssetID: d.dev.AssetID.String(),
			Metric:  metric,
			Value:   v,
			Ts:      now,
			Tags:    map[string]string{"source": cfg.BaseURL},
		})
	}
	if len(samples) == 0 {
		return nil
	}
	if err := buf.EnqueueBatch(ctx, samples); err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	d.log.Info("rest_poll_ok", "count", len(samples))
	return nil
}

// resolveNumeric mirrors the Python driver's _resolve: walks `.`-split
// keys into nested maps and returns the value as float64 if it's an
// int/float JSON number. Anything else (nil, string, object, array)
// returns false. json.Unmarshal hands every number back as float64,
// so the int branch is defensive but practically dead.
func resolveNumeric(doc any, path string) (float64, bool) {
	cur := doc
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return 0, false
		}
		next, present := m[part]
		if !present {
			return 0, false
		}
		cur = next
	}
	switch v := cur.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}
