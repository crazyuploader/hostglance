package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseFlexDuration(t *testing.T) {
	def := 300 * time.Second
	tests := []struct {
		name string
		in   any
		want time.Duration
	}{
		{"nil", nil, def},
		{"int seconds", 120, 120 * time.Second},
		{"int64 seconds", int64(60), 60 * time.Second},
		{"float seconds", 90.0, 90 * time.Second},
		{"numeric string", "120", 120 * time.Second},
		{"duration string", "5m", 5 * time.Minute},
		{"compound duration", "1h30m", 90 * time.Minute},
		{"garbage", "not-a-duration", def},
		{"zero", 0, def},
		{"negative", -5, def},
		{"negative duration string", "-5m", def},
		{"bound duration flag", 48 * time.Hour, 48 * time.Hour},
		{"unsupported type", []string{"5m"}, def},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseFlexDuration(tt.in, def); got != tt.want {
				t.Errorf("parseFlexDuration(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestLoadSettings(t *testing.T) {
	t.Parallel()
	cfg, err := configFromYAML(t, `hosts: [nas]
cache_ttl: 1m
history: {retention: 720, record_interval: "300"}
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CacheTTL != time.Minute || cfg.History.Retention != 720*time.Second || cfg.History.RecordInterval != 300*time.Second {
		t.Fatalf("durations: ttl=%v retention=%v record=%v", cfg.CacheTTL, cfg.History.Retention, cfg.History.RecordInterval)
	}

	for yaml, want := range map[string]string{
		"refesh: 5":                `unknown setting "refesh"`,
		"history: {enabeld: true}": `unknown setting "history.enabeld"`,
		"log_format: jsn":          "log_format",
		"max_usage_percent: 900":   "max_usage_percent",
		"max_usage_percent: -1":    "max_usage_percent",
	} {
		if _, err := configFromYAML(t, "hosts: [nas]\n"+yaml); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error = %v, want containing %q", yaml, err, want)
		}
	}
}
