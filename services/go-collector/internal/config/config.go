// Package config loads the on-disk collector YAML. Mirrors the Python
// pydantic CollectorConfig in collector/src/dcim_collector/config.py so
// the two collectors can share a config file during Phase-1 cutover
// (operator points one host's config at the Go binary; the rest stay on
// Python until drivers reach parity).
//
// Unknown YAML keys are accepted (yaml.v3 default) so a config written
// for the Python collector — which has driver-specific subtrees Go
// doesn't yet read — loads without error. Drivers that aren't
// implemented yet just don't fire.
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

type Mtls struct {
	Enabled    bool   `yaml:"enabled"`
	ClientCert string `yaml:"client_cert,omitempty"`
	ClientKey  string `yaml:"client_key,omitempty"`
	CABundle   string `yaml:"ca_bundle,omitempty"`
}

// Driver-specific configs. Phase 1 keeps them as opaque maps so the
// YAML round-trips through the buffer unchanged; Phase 2 promotes the
// fields each driver actually needs into typed structs.
type Device struct {
	AssetID            uuid.UUID              `yaml:"asset_id"`
	Kind               string                 `yaml:"kind"`
	Driver             string                 `yaml:"driver"`
	PollIntervalSecs   int                    `yaml:"poll_interval_seconds"`
	SNMP               map[string]any         `yaml:"snmp,omitempty"`
	Redfish            map[string]any         `yaml:"redfish,omitempty"`
	Modbus             map[string]any         `yaml:"modbus,omitempty"`
	REST               map[string]any         `yaml:"rest,omitempty"`
	IPMI               map[string]any         `yaml:"ipmi,omitempty"`
}

type Config struct {
	CollectorID             uuid.UUID `yaml:"collector_id"`
	SiteID                  uuid.UUID `yaml:"site_id"`
	IngestURL               string    `yaml:"ingest_url"`
	TelemetryURL            string    `yaml:"telemetry_url,omitempty"`
	HeartbeatIntervalSecs   int       `yaml:"heartbeat_interval_seconds"`
	BufferPath              string    `yaml:"buffer_path"`
	APITokenFile            string    `yaml:"api_token_file,omitempty"`
	Mtls                    Mtls      `yaml:"mtls"`
	Devices                 []Device  `yaml:"devices"`
	// DNS, syslog, etc. — accepted but ignored in Phase 1.
	DNS    map[string]any `yaml:"dns,omitempty"`
	Syslog *int           `yaml:"syslog_listen,omitempty"`
}

// TelemetryEndpoint resolves the URL the forwarder should POST telemetry
// batches to. Mirrors collector/forwarder.py:
// (telemetry_url or ingest_url).rstrip('/') + "/api/v1/ingest/telemetry"
func (c *Config) TelemetryEndpoint() string {
	base := c.TelemetryURL
	if base == "" {
		base = c.IngestURL
	}
	return trimRightSlash(base) + "/api/v1/ingest/telemetry"
}

// HeartbeatEndpoint always goes through ingest_url — heartbeats land in
// the Python API even when telemetry has been cut over to go-ingest.
func (c *Config) HeartbeatEndpoint() string {
	return fmt.Sprintf("%s/api/v1/collectors/%s/heartbeat",
		trimRightSlash(c.IngestURL), c.CollectorID)
}

func (c *Config) HeartbeatInterval() time.Duration {
	if c.HeartbeatIntervalSecs <= 0 {
		return 30 * time.Second
	}
	return time.Duration(c.HeartbeatIntervalSecs) * time.Second
}

// LoadToken reads the API token from api_token_file. Whitespace is
// trimmed so a trailing newline from `echo dcim_... > token` doesn't
// poison the Authorization header.
func (c *Config) LoadToken() (string, error) {
	if c.APITokenFile == "" {
		return "", nil
	}
	b, err := os.ReadFile(c.APITokenFile)
	if err != nil {
		return "", fmt.Errorf("read api_token_file: %w", err)
	}
	return trimSpace(string(b)), nil
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	if c.BufferPath == "" {
		c.BufferPath = "/var/lib/dcim-collector/buffer.db"
	}
	if c.IngestURL == "" {
		return nil, fmt.Errorf("ingest_url required")
	}
	return &c, nil
}

func trimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }
