// system_settings loader. The three keys the worker reads:
//
//   arin.regrws.endpoint  — string, e.g. "https://reg.ote.arin.net".
//                           Default: EndpointOTE.
//   arin.regrws.api_key   — string. Default: "" (disables submission).
//   arin.regrws.enabled   — bool. Default: false.
//
// Loaded fresh every tick so an operator who rotates the key via the
// admin Settings UI takes effect within one cycle — no worker
// restart needed.
package arin

import (
	"context"
	"encoding/json"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

const (
	SettingEndpoint = "arin.regrws.endpoint"
	SettingAPIKey   = "arin.regrws.api_key"
	SettingEnabled  = "arin.regrws.enabled"
)

// SettingsQuerier is the slice of sqlc methods the loader needs.
// *dbq.Queries satisfies it; tests substitute an in-memory fake.
type SettingsQuerier interface {
	GetSystemSetting(ctx context.Context, key string) (dbq.SystemSetting, error)
}

// LoadConfig reads the three ARIN-related system_settings rows and
// builds a Config. Missing rows fall back to defaults — Enabled is
// false unless explicitly set, so a fresh deployment that hasn't
// configured ARIN stays inert.
func LoadConfig(ctx context.Context, q SettingsQuerier) Config {
	cfg := Config{
		Endpoint: EndpointOTE,
		Enabled:  false,
	}
	if row, err := q.GetSystemSetting(ctx, SettingEndpoint); err == nil {
		var s string
		if json.Unmarshal(row.Value, &s) == nil && s != "" {
			cfg.Endpoint = s
		}
	}
	if row, err := q.GetSystemSetting(ctx, SettingAPIKey); err == nil {
		var s string
		_ = json.Unmarshal(row.Value, &s)
		cfg.APIKey = s
	}
	if row, err := q.GetSystemSetting(ctx, SettingEnabled); err == nil {
		var b bool
		_ = json.Unmarshal(row.Value, &b)
		cfg.Enabled = b
	}
	return cfg
}
