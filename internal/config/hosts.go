package config

import (
	"cmp"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"unicode"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// ExporterMode controls whether a missing exporter is an error.
type ExporterMode string

const (
	// ModeAuto discovers metrics without requiring the exporter to exist.
	ModeAuto ExporterMode = "auto"
	// ModeEnabled requires the exporter to respond with recognizable metrics.
	ModeEnabled ExporterMode = "enabled"
	// ModeDisabled prevents requests to this exporter.
	ModeDisabled ExporterMode = "disabled"
)

// Exporter configures one metrics source. A URL override does not make it required.
type Exporter struct {
	Mode ExporterMode `mapstructure:"mode"`
	URL  string       `mapstructure:"url"`
}

// Exporters groups the independently configurable sources on a host.
type Exporters struct {
	Node     Exporter `mapstructure:"node"`
	ZFS      Exporter `mapstructure:"zfs"`
	Smartctl Exporter `mapstructure:"smartctl"`
	// PVE is prometheus-pve-exporter. It usually runs on another server, so it
	// has no default URL and stays off until url is set.
	PVE Exporter `mapstructure:"pve"`
}

// Host is a stable display/history identity with optional metrics sources.
// After Load, all exporter modes and active URLs are resolved.
type Host struct {
	Address  string `mapstructure:"address"`
	Label    string `mapstructure:"label"`
	Location string `mapstructure:"location"`
	// Parent is the label of the host this one runs on (a VM or container).
	// Guests are excluded from fleet totals so resources are not counted twice.
	Parent    string    `mapstructure:"parent"`
	Exporters Exporters `mapstructure:"exporters"`
	// LegacyCombinedMetrics preserves disk metrics bundled into old ZFS endpoints.
	LegacyCombinedMetrics bool `mapstructure:"-"`
}

// loadHosts decodes hosts or legacy endpoints and rejects duplicate labels.
func loadHosts(v *viper.Viper) ([]Host, error) {
	hosts, err := decodeList[Host](v.Get("hosts"), "address")
	if err != nil {
		return nil, fmt.Errorf("decode hosts: %w", err)
	}
	endpoints, err := decodeList[legacyEndpoint](v.Get("endpoints"), "url")
	if err != nil {
		return nil, fmt.Errorf("decode endpoints: %w", err)
	}
	if len(hosts) > 0 && len(endpoints) > 0 {
		return nil, fmt.Errorf("use either hosts or legacy endpoints, not both")
	}
	for i := range hosts {
		if err := normalizeHost(&hosts[i]); err != nil {
			return nil, fmt.Errorf("host[%d]: %w", i, err)
		}
	}
	for i, ep := range endpoints {
		host, err := legacyHost(ep)
		if err != nil {
			return nil, fmt.Errorf("endpoint[%d]: %w", i, err)
		}
		hosts = append(hosts, host)
	}
	labels := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		if labels[host.Label] {
			return nil, fmt.Errorf("duplicate host label %q", host.Label)
		}
		labels[host.Label] = true
	}
	parents := make(map[string]string, len(hosts))
	for _, host := range hosts {
		parents[host.Label] = host.Parent
	}
	for _, host := range hosts {
		if host.Parent == "" {
			continue
		}
		// ponytail: one nesting level; deeper chains only matter for nested virtualization.
		grandparent, ok := parents[host.Parent]
		switch {
		case !ok:
			return nil, fmt.Errorf("host %q: parent %q is not a configured host label", host.Label, host.Parent)
		case host.Parent == host.Label:
			return nil, fmt.Errorf("host %q cannot be its own parent", host.Label)
		case grandparent != "":
			return nil, fmt.Errorf("host %q: parent %q is itself a guest, and only one nesting level is supported", host.Label, host.Parent)
		}
	}
	return hosts, nil
}

// decodeList accepts structured YAML, hostname/URL lists, and comma-separated
// flag/environment values. Strict decoding catches misspelled exporter settings.
func decodeList[T any](raw any, shorthand string) ([]T, error) {
	var out []T
	if raw == nil {
		return out, nil
	}
	if text, ok := raw.(string); ok {
		raw = strings.FieldsFunc(text, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	}
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:      &out,
		ErrorUnused: true,
		DecodeHook: func(from, to reflect.Type, data any) (any, error) {
			if from.Kind() == reflect.String && to == reflect.TypeFor[T]() {
				return map[string]any{shorthand: data}, nil
			}
			return data, nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create config decoder: %w", err)
	}
	if err := decoder.Decode(raw); err != nil {
		return nil, fmt.Errorf("decode list: %w", err)
	}
	return out, nil
}

// normalizeHost validates an address and resolves exporter modes and default URLs.
func normalizeHost(host *Host) error {
	host.Address = strings.TrimSpace(host.Address)
	if strings.HasPrefix(host.Address, "[") && strings.HasSuffix(host.Address, "]") {
		host.Address = strings.TrimSuffix(strings.TrimPrefix(host.Address, "["), "]")
	}
	if host.Address == "" {
		return fmt.Errorf("missing address")
	}
	_, ipErr := netip.ParseAddr(host.Address)
	badHostname := strings.ContainsAny(host.Address, ":/\\?#@[]") || strings.ContainsFunc(host.Address, unicode.IsSpace)
	if ipErr != nil && badHostname {
		return fmt.Errorf("address must be a hostname or IP address without a port or path: set the exporter url for a custom port or path")
	}
	host.Label = cmp.Or(strings.TrimSpace(host.Label), host.Address)
	host.Parent = strings.TrimSpace(host.Parent)
	sources := []struct {
		name string
		port string
		exp  *Exporter
	}{
		{name: "node", port: "9100", exp: &host.Exporters.Node},
		{name: "zfs", port: "9134", exp: &host.Exporters.ZFS},
		{name: "smartctl", port: "9633", exp: &host.Exporters.Smartctl},
		{name: "pve", exp: &host.Exporters.PVE},
	}
	for _, source := range sources {
		exp := source.exp
		exp.Mode = cmp.Or(exp.Mode, ModeAuto)
		switch exp.Mode {
		case ModeAuto, ModeEnabled, ModeDisabled:
		default:
			return fmt.Errorf("%s mode must be auto, enabled, or disabled", source.name)
		}
		if exp.URL == "" && source.port == "" {
			if exp.Mode == ModeEnabled {
				return fmt.Errorf("%s exporter has no default URL: set its url", source.name)
			}
			exp.Mode = ModeDisabled
		}
		if exp.URL == "" && exp.Mode != ModeDisabled {
			u := url.URL{Scheme: "http", Host: net.JoinHostPort(host.Address, source.port), Path: "/metrics"}
			exp.URL = u.String()
		}
		if exp.URL != "" {
			if err := validateExporterURL(exp.URL); err != nil {
				return fmt.Errorf("%s url: %w", source.name, err)
			}
		}
	}
	return nil
}

// validateExporterURL requires an http(s) URL with a host and no fragment.
func validateExporterURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		// Config URLs can contain credentials; do not echo the input in errors.
		return fmt.Errorf("invalid exporter URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("exporter URL must use http or https")
	}
	if u.Hostname() == "" || u.Fragment != "" {
		return fmt.Errorf("exporter URL must include a host and have no fragment")
	}
	return nil
}

// legacyHost converts an old endpoints entry, keeping its label for history.
func legacyHost(ep legacyEndpoint) (Host, error) {
	if ep.URL == "" {
		return Host{}, fmt.Errorf("missing url")
	}
	for _, raw := range []string{ep.URL, ep.NodeExporterURL, ep.SmartctlURL} {
		if raw != "" {
			if err := validateExporterURL(raw); err != nil {
				return Host{}, err
			}
		}
	}
	optional := func(raw string) Exporter {
		mode := ModeDisabled
		if raw != "" {
			mode = ModeAuto
		}
		return Exporter{Mode: mode, URL: raw}
	}
	return Host{
		// Retain the old fallback label so existing history keys remain valid.
		Label:                 cmp.Or(ep.Label, ep.URL),
		Location:              ep.Location,
		LegacyCombinedMetrics: true,
		Exporters: Exporters{
			ZFS:      Exporter{Mode: ModeEnabled, URL: ep.URL},
			Node:     optional(ep.NodeExporterURL),
			Smartctl: optional(ep.SmartctlURL),
			PVE:      Exporter{Mode: ModeDisabled},
		},
	}, nil
}
