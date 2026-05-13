// Package drivers is the per-protocol poller interface. Phase 1 only
// registers stubs that log "not implemented" — the real drivers land
// in Phase 2 (SNMP + Redfish) and Phase 3 (Modbus + REST). The shape
// lets main wire devices to drivers today so dropping a real Poller in
// later is a one-line registry edit.
//
// A Poller is expected to:
//   1. Be called every poll_interval_seconds by the main loop.
//   2. Talk to its device.
//   3. Enqueue one or more buffer.Sample rows.
//   4. Return cleanly even on transient errors (the loop will call again).
package drivers

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/usg-dcim/services/go-collector/internal/buffer"
	"github.com/usg-dcim/services/go-collector/internal/config"
	"github.com/usg-dcim/services/go-collector/internal/drivers/modbus"
	"github.com/usg-dcim/services/go-collector/internal/drivers/redfish"
	"github.com/usg-dcim/services/go-collector/internal/drivers/rest"
	"github.com/usg-dcim/services/go-collector/internal/drivers/snmp"
)

// Poller is the per-device worker. One instance per Device row in
// config.yaml, kept alive for the collector's lifetime.
type Poller interface {
	// Poll runs one iteration. Errors are logged but not fatal.
	Poll(ctx context.Context, buf *buffer.Buffer) error
	// Kind returns the driver name for logging.
	Kind() string
}

// Build instantiates a Poller for the given device. Phase 2 ships real
// SNMP and Redfish; modbus / rest / ipmi keep their stubs until their
// own phase lands.
func Build(d config.Device, log *slog.Logger) (Poller, error) {
	switch d.Driver {
	case "snmp":
		if d.SNMP == nil {
			return nil, fmt.Errorf("snmp driver requires snmp: block")
		}
		return snmp.New(&d, log), nil
	case "redfish":
		if d.Redfish == nil {
			return nil, fmt.Errorf("redfish driver requires redfish: block")
		}
		return redfish.New(&d, log), nil
	case "modbus":
		if d.Modbus == nil {
			return nil, fmt.Errorf("modbus driver requires modbus: block")
		}
		return modbus.New(&d, log), nil
	case "rest":
		if d.REST == nil {
			return nil, fmt.Errorf("rest driver requires rest: block")
		}
		return rest.New(&d, log), nil
	case "ipmi":
		return &stub{driver: d.Driver, asset: d.AssetID.String(), log: log}, nil
	default:
		return nil, fmt.Errorf("unknown driver %q", d.Driver)
	}
}

// stub fulfils the interface and logs on every Poll so the operator
// can see the main loop is firing even before real drivers land. It
// never enqueues — Phase-2 drivers will replace this.
type stub struct {
	driver string
	asset  string
	log    *slog.Logger
}

func (s *stub) Kind() string { return s.driver }
func (s *stub) Poll(_ context.Context, _ *buffer.Buffer) error {
	s.log.Info("driver_poll_stub",
		"driver", s.driver, "asset", s.asset,
		"hint", "Phase 2 replaces this with the real driver")
	return nil
}

// Schedule runs p.Poll on `interval`, returning when ctx is cancelled.
// Lives here (not in main) so each driver implementation can override
// scheduling later — e.g. SNMP bulkget batching, Redfish session reuse.
func Schedule(ctx context.Context, p Poller, buf *buffer.Buffer, interval time.Duration, log *slog.Logger) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	// Stagger first poll so a config with many devices doesn't fire
	// every poller in the same millisecond at startup.
	if err := p.Poll(ctx, buf); err != nil {
		log.Warn("poll_failed", "driver", p.Kind(), "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := p.Poll(ctx, buf); err != nil {
				log.Warn("poll_failed", "driver", p.Kind(), "err", err)
			}
		}
	}
}
