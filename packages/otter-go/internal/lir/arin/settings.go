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
	GetSystemSettings(ctx context.Context, keys []string) ([]dbq.SystemSetting, error)
}

// LoadConfig reads the three ARIN-related system_settings rows and
// builds a Config. Missing rows fall back to defaults — Enabled is
// false unless explicitly set, so a fresh deployment that hasn't
// configured ARIN stays inert.
//
// Issues a single GetSystemSettings query per call; earlier shape
// fired one round-trip per key for three round-trips per worker
// tick.
func LoadConfig(ctx context.Context, q SettingsQuerier) Config {
	cfg := Config{
		Endpoint: EndpointOTE,
		Enabled:  false,
	}
	rows, err := q.GetSystemSettings(ctx, []string{
		SettingEndpoint, SettingAPIKey, SettingEnabled,
	})
	if err != nil {
		// DB error — log nothing here (the worker tick logs the
		// outer error); return defaults so the tick treats ARIN
		// as disabled and skips the drain.
		return cfg
	}
	byKey := make(map[string]json.RawMessage, len(rows))
	for _, r := range rows {
		byKey[r.Key] = r.Value
	}
	if v, ok := byKey[SettingEndpoint]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil && s != "" {
			cfg.Endpoint = s
		}
	}
	if v, ok := byKey[SettingAPIKey]; ok {
		var s string
		_ = json.Unmarshal(v, &s)
		cfg.APIKey = s
	}
	if v, ok := byKey[SettingEnabled]; ok {
		var b bool
		_ = json.Unmarshal(v, &b)
		cfg.Enabled = b
	}
	return cfg
}
