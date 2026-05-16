// Package modbus implements the Modbus-TCP driver. Parity with
// collector/src/dcim_collector/drivers/modbus.py:
//   - one TCP connection per poll cycle (cheap; ~5ms)
//   - read holding or input registers per the YAML map
//   - multiply raw u16 by scale → float64
//
// goburrow/modbus is the de-facto Go Modbus library. Single-register
// reads only in Phase 3; multi-register / float-encoded values land
// when an operator actually needs them (no real device in the dev
// stack to exercise that path yet).
package modbus

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"time"

	"github.com/goburrow/modbus"

	"github.com/usg-dcim/packages/badger/internal/buffer"
	"github.com/usg-dcim/packages/badger/internal/config"
)

type Driver struct {
	dev *config.Device
	log *slog.Logger
}

func New(d *config.Device, log *slog.Logger) *Driver {
	return &Driver{dev: d, log: log.With("driver", "modbus", "asset", d.AssetID.String())}
}

func (*Driver) Kind() string { return "modbus" }

func (d *Driver) Poll(ctx context.Context, buf *buffer.Buffer) error {
	cfg := d.dev.Modbus
	if cfg == nil || len(cfg.Registers) == 0 {
		return nil
	}
	port := cfg.Port
	if port == 0 {
		port = 502
	}
	handler := modbus.NewTCPClientHandler(fmt.Sprintf("%s:%d", cfg.Host, port))
	handler.Timeout = 3 * time.Second
	handler.SlaveId = byte(cfg.UnitID)
	if err := handler.Connect(); err != nil {
		return fmt.Errorf("modbus connect: %w", err)
	}
	defer handler.Close()

	client := modbus.NewClient(handler)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	samples := make([]buffer.Sample, 0, len(cfg.Registers))

	for metric, reg := range cfg.Registers {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var raw []byte
		var err error
		switch reg.Type {
		case "holding":
			raw, err = client.ReadHoldingRegisters(uint16(reg.Address), 1)
		case "input_register":
			raw, err = client.ReadInputRegisters(uint16(reg.Address), 1)
		default:
			// coil / discrete deferred — Python parity keeps them out
			// of the poll loop because they're boolean-only and the
			// dashboard surfaces them through a different metric shape.
			continue
		}
		if err != nil {
			d.log.Warn("modbus_read_failed", "metric", metric, "addr", reg.Address, "err", err)
			continue
		}
		if len(raw) < 2 {
			d.log.Warn("modbus_short_read", "metric", metric, "addr", reg.Address, "bytes", len(raw))
			continue
		}
		val := binary.BigEndian.Uint16(raw[:2])
		scale := reg.Scale
		if scale == 0 {
			scale = 1
		}
		samples = append(samples, buffer.Sample{
			AssetID: d.dev.AssetID.String(),
			Metric:  metric,
			Value:   float64(val) * scale,
			Ts:      now,
			Tags:    map[string]string{"address": fmt.Sprintf("%d", reg.Address), "host": cfg.Host},
		})
	}
	if len(samples) == 0 {
		return nil
	}
	if err := buf.EnqueueBatch(ctx, samples); err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	d.log.Info("modbus_poll_ok", "count", len(samples))
	return nil
}
