// Package runtime carries the live ticker intervals every loop reads.
//
// Each interval starts at the YAML default and may be overridden by
// the heartbeat response from central. Reads are lock-free (atomic
// int64 nanoseconds); writes happen in one place — the heartbeat
// receiver — so a torn read isn't possible even on 32-bit hosts.
//
// Loops call Get<Name>(default) on every iteration so a change takes
// effect on the next tick, with up to one current-interval of lag.
// No restart, no ticker reset, no goroutine churn.
package runtime

import (
	"sync/atomic"
	"time"
)

type Config struct {
	dnsMetricsNS atomic.Int64
	devicePollNS atomic.Int64
	heartbeatNS  atomic.Int64
}

func New() *Config { return &Config{} }

// Apply sets each field from a heartbeat response. Zero / nil values
// clear the override so callers fall back to their YAML default.
func (c *Config) Apply(dnsMetrics, devicePoll, heartbeat time.Duration) {
	c.dnsMetricsNS.Store(int64(dnsMetrics))
	c.devicePollNS.Store(int64(devicePoll))
	c.heartbeatNS.Store(int64(heartbeat))
}

// DNSMetricsInterval returns the override if set, else fallback.
func (c *Config) DNSMetricsInterval(fallback time.Duration) time.Duration {
	if v := c.dnsMetricsNS.Load(); v > 0 {
		return time.Duration(v)
	}
	return fallback
}

// DevicePollInterval returns the override if set, else fallback.
func (c *Config) DevicePollInterval(fallback time.Duration) time.Duration {
	if v := c.devicePollNS.Load(); v > 0 {
		return time.Duration(v)
	}
	return fallback
}

// HeartbeatInterval returns the override if set, else fallback.
func (c *Config) HeartbeatInterval(fallback time.Duration) time.Duration {
	if v := c.heartbeatNS.Load(); v > 0 {
		return time.Duration(v)
	}
	return fallback
}
