// Package config validates host discovery and runtime settings.
package config

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/spf13/viper"
)

// legacyEndpoint preserves the original ZFS-first configuration format.
type legacyEndpoint struct {
	URL             string `mapstructure:"url"`
	Label           string `mapstructure:"label"`
	Location        string `mapstructure:"location"`
	SmartctlURL     string `mapstructure:"smartctl_url"`      // optional; omit to skip disk metrics
	NodeExporterURL string `mapstructure:"node_exporter_url"` // optional; omit to skip system metrics
}

// HistoryConfig controls the embedded time-series store.
type HistoryConfig struct {
	Enabled        bool
	Path           string
	Retention      time.Duration
	RecordInterval time.Duration
}

// Config holds all runtime options.
type Config struct {
	Hosts           []Host
	Addr            string
	Refresh         time.Duration
	CacheTTL        time.Duration
	Debug           bool
	TrustedProxies  []string
	MaxUsagePercent float64
	LogFormat       string // "text" or "json"
	History         HistoryConfig
}

// parseFlexDuration converts a config value into a duration.
// Accepts bare numbers (seconds, back-compat), numeric strings (seconds),
// and Go duration strings ("5m", "1h30m"). Returns def when v is nil,
// unparseable, or non-positive.
func parseFlexDuration(v any, def time.Duration) time.Duration {
	var d time.Duration
	switch t := v.(type) {
	case nil:
		return def
	case time.Duration: // bound duration flags
		d = t
	case int:
		d = time.Duration(t) * time.Second
	case int64:
		d = time.Duration(t) * time.Second
	case float64:
		d = time.Duration(t * float64(time.Second))
	case string:
		if secs, err := strconv.Atoi(t); err == nil {
			d = time.Duration(secs) * time.Second
		} else if parsed, err := time.ParseDuration(t); err == nil {
			d = parsed
		} else {
			return def
		}
	default:
		return def
	}
	if d <= 0 {
		return def
	}
	return d
}

// Load reads viper state into a validated Config.
func Load() (*Config, error) {
	return load(viper.GetViper())
}

// knownKeys lists every accepted setting; anything else is a typo.
var knownKeys = []string{
	"addr", "refresh", "cache_ttl", "debug", "trusted_proxies",
	"max_usage_percent", "log_format", "hosts", "endpoints",
	"history.enabled", "history.path", "history.retention", "history.record_interval",
}

func load(v *viper.Viper) (*Config, error) {
	for _, key := range v.AllKeys() {
		if !slices.Contains(knownKeys, key) {
			return nil, fmt.Errorf("unknown setting %q", key)
		}
	}
	cfg := &Config{
		Addr:            cmp.Or(v.GetString("addr"), ":8054"),
		Refresh:         parseFlexDuration(v.Get("refresh"), 300*time.Second),
		CacheTTL:        parseFlexDuration(v.Get("cache_ttl"), 30*time.Second),
		Debug:           v.GetBool("debug"),
		TrustedProxies:  v.GetStringSlice("trusted_proxies"),
		MaxUsagePercent: v.GetFloat64("max_usage_percent"),
		LogFormat:       cmp.Or(v.GetString("log_format"), "text"),
		History: HistoryConfig{
			Enabled:        v.GetBool("history.enabled"),
			Path:           cmp.Or(v.GetString("history.path"), "./data/history.db"),
			Retention:      parseFlexDuration(v.Get("history.retention"), 720*time.Hour),
			RecordInterval: parseFlexDuration(v.Get("history.record_interval"), 0),
		},
	}
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		return nil, fmt.Errorf("log_format must be text or json")
	}
	if cfg.MaxUsagePercent < 0 || cfg.MaxUsagePercent > 100 {
		return nil, fmt.Errorf("max_usage_percent must be between 0 and 100")
	}
	var err error
	cfg.Hosts, err = loadHosts(v)
	if err != nil {
		return nil, fmt.Errorf("hosts: %w", err)
	}
	return cfg, nil
}
