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
	"net/url"
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

// SNMPConfig mirrors the SnmpDriverConfig pydantic model. Version is a
// string ("1", "2c", "3") because the YAML the Python collector reads
// uses string-quoted versions and we want config-file parity. v3 is
// accepted at parse time but rejected at poll time until the auth flow
// lands in a later phase.
type SNMPConfig struct {
	Host      string            `yaml:"host"`
	Port      int               `yaml:"port"`
	Community string            `yaml:"community"`
	Version   string            `yaml:"version"`
	OIDs      map[string]string `yaml:"oids"`
}

// RedfishConfig mirrors RedfishDriverConfig. password is preferred over
// password_ref in Phase 2 — refs (Vault / secret-store lookup) land
// with the broader credentials story later.
type RedfishConfig struct {
	BaseURL     string `yaml:"base_url"`
	Username    string `yaml:"username"`
	PasswordRef string `yaml:"password_ref,omitempty"`
	Password    string `yaml:"password,omitempty"`
	VerifyTLS   *bool  `yaml:"verify_tls,omitempty"`
}

// ModbusRegister mirrors the ModbusRegister pydantic model. `type`
// is one of holding|input_register|coil|discrete; the driver currently
// reads only holding and input_register (matches the Python parity
// scope). `scale` lets the operator pre-multiply raw register values
// into engineering units without the central having to know.
type ModbusRegister struct {
	Address int     `yaml:"address"`
	Type    string  `yaml:"type"`
	Scale   float64 `yaml:"scale"`
}

type ModbusConfig struct {
	Host      string                    `yaml:"host"`
	Port      int                       `yaml:"port"`
	UnitID    int                       `yaml:"unit_id"`
	Registers map[string]ModbusRegister `yaml:"registers"`
}

// RESTConfig mirrors the RestDriverConfig pydantic model. `paths`
// maps metric name → dot-path inside the JSON response, e.g.
// "power.kw": "sensors.power.kw".
type RESTConfig struct {
	BaseURL   string            `yaml:"base_url"`
	Headers   map[string]string `yaml:"headers,omitempty"`
	Paths     map[string]string `yaml:"paths"`
	VerifyTLS *bool             `yaml:"verify_tls,omitempty"`
}

func (r *RESTConfig) VerifyTLSFlag() bool {
	if r.VerifyTLS == nil {
		return true
	}
	return *r.VerifyTLS
}

// IPMIConfig mirrors IpmiDriverConfig. Password is read directly when
// present; password_ref (Vault lookup) lands with the credentials
// story later. The Phase-4 driver shells out to `ipmitool`, so the
// fields match its -H / -U / -P flags.
type IPMIConfig struct {
	Host        string `yaml:"host"`
	Username    string `yaml:"username"`
	Password    string `yaml:"password,omitempty"`
	PasswordRef string `yaml:"password_ref,omitempty"`
}

type Device struct {
	AssetID          uuid.UUID      `yaml:"asset_id"`
	Kind             string         `yaml:"kind"`
	Driver           string         `yaml:"driver"`
	PollIntervalSecs int            `yaml:"poll_interval_seconds"`
	SNMP             *SNMPConfig    `yaml:"snmp,omitempty"`
	Redfish          *RedfishConfig `yaml:"redfish,omitempty"`
	Modbus           *ModbusConfig  `yaml:"modbus,omitempty"`
	REST             *RESTConfig    `yaml:"rest,omitempty"`
	IPMI             *IPMIConfig    `yaml:"ipmi,omitempty"`
}

// DNSServerConfig mirrors collector/dcim_collector/config.py:DnsServerConfig.
// One entry per CoreDNS / Hickory deployment the agent renders configs
// for. Phase 4 implements bundle apply + metrics scrape + dnstap.
//
// GoBGP deprecation: the prior gobgp_pidfile + gobgp_api_host fields
// are gone. Cilium BGP owns the BGP session at the cluster level —
// the in-pod gobgpd process no longer runs, so there's nothing for
// the agent to PID-signal or RIB-reconcile.
type DNSServerConfig struct {
	ID             uuid.UUID `yaml:"id"`
	Role           string    `yaml:"role"` // auth | recursive
	OutputDir      string    `yaml:"output_dir"`
	CoreDNSPIDFile string    `yaml:"coredns_pidfile,omitempty"`
	MetricsURL     string    `yaml:"metrics_url,omitempty"`
	MetricsEnabled *bool     `yaml:"metrics_enabled,omitempty"`
	DnstapSocket   string    `yaml:"dnstap_socket,omitempty"`
}

func (s *DNSServerConfig) MetricsOn() bool {
	if s.MetricsEnabled == nil {
		return true
	}
	return *s.MetricsEnabled
}

// DNSAgentConfig mirrors DnsAgentConfig. When enabled is false (or the
// servers list is empty), the agent silently no-ops — matches the
// Python collector's "dns_agent_disabled" log line.
type DNSAgentConfig struct {
	Enabled             bool              `yaml:"enabled"`
	PollIntervalSecs    int               `yaml:"poll_interval_seconds"`
	APIBase             string            `yaml:"api_base,omitempty"`
	Servers             []DNSServerConfig `yaml:"servers"`
	MetricsIntervalSecs int               `yaml:"metrics_interval_seconds"`
	MetricsEnabled      bool              `yaml:"metrics_enabled"`
}

func (d *DNSAgentConfig) PollInterval() time.Duration {
	if d.PollIntervalSecs <= 0 {
		return 30 * time.Second
	}
	return time.Duration(d.PollIntervalSecs) * time.Second
}

func (d *DNSAgentConfig) MetricsInterval() time.Duration {
	if d.MetricsIntervalSecs <= 0 {
		return 60 * time.Second
	}
	return time.Duration(d.MetricsIntervalSecs) * time.Second
}

// RedfishVerifyTLS returns the effective verify-TLS flag. Defaults to
// true when the YAML field is absent — matches the Python collector.
func (r *RedfishConfig) RedfishVerifyTLS() bool {
	if r.VerifyTLS == nil {
		return true
	}
	return *r.VerifyTLS
}

// DNSTapConfig opts in to the dnstap reader. When SocketPath is set,
// main spawns a UNIX-socket fstrm server CoreDNS auth pods connect to
// for query-stream replay. Phase 3 surfaces the (name, type) pairs
// through OnQuery → log; the top-K reservoir + metrics POST land
// alongside the DNS metrics scraper in Phase 4.
type DNSTapConfig struct {
	SocketPath string `yaml:"socket_path"`
}

type Config struct {
	CollectorID           uuid.UUID     `yaml:"collector_id"`
	SiteID                uuid.UUID     `yaml:"site_id"`
	IngestURL             string        `yaml:"ingest_url"`
	TelemetryURL          string        `yaml:"telemetry_url,omitempty"`
	HeartbeatIntervalSecs int           `yaml:"heartbeat_interval_seconds"`
	BufferPath            string        `yaml:"buffer_path"`
	APITokenFile          string        `yaml:"api_token_file,omitempty"`
	Mtls                  Mtls          `yaml:"mtls"`
	Devices               []Device      `yaml:"devices"`
	DNSTap                *DNSTapConfig `yaml:"dnstap,omitempty"`
	DNS                   DNSAgentConfig `yaml:"dns"`
	Syslog                *int           `yaml:"syslog_listen,omitempty"`
}

// APIBase resolves the root the DNS agent dials for bundle + metrics
// POSTs. Operator override wins; otherwise we derive scheme://host:port
// from ingest_url. Mirrors collector/dns_agent.py:_api_base.
func (c *Config) APIBase() string {
	if c.DNS.APIBase != "" {
		return trimRightSlash(c.DNS.APIBase)
	}
	// trimRightSlash on ingest_url; strip everything after the
	// scheme://host[:port] portion. This handles ingest_url values that
	// embed a path (rare, but the API client doesn't want it).
	u, err := url.Parse(c.IngestURL)
	if err != nil || u.Scheme == "" {
		return trimRightSlash(c.IngestURL)
	}
	return u.Scheme + "://" + u.Host
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
