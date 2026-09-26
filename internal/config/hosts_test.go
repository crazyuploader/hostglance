package config

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

func configFromYAML(t *testing.T, text string) (*Config, error) {
	t.Helper()
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(text)); err != nil {
		t.Fatalf("read test config: %v", err)
	}
	return loadFrom(v)
}

// skipcq: GO-R1005 -- one assertion block per resolved host field
func TestLoadHosts(t *testing.T) {
	t.Parallel()
	cfg, err := configFromYAML(t, `hosts:
  - server.home
  - address: 2001:db8::1
    label: nas
    location: lab
    exporters:
      node:
        url: https://metrics.home/custom/node
      zfs:
        mode: enabled
      smartctl:
        mode: disabled
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hosts) != 2 {
		t.Fatalf("got %d hosts", len(cfg.Hosts))
	}
	first, nas := cfg.Hosts[0], cfg.Hosts[1]
	if first.Label != "server.home" || first.Exporters.Node.URL != "http://server.home:9100/metrics" {
		t.Fatalf("unexpected defaults: %+v", first)
	}
	if first.Exporters.ZFS.URL != "http://server.home:9134/metrics" || first.Exporters.Smartctl.URL != "http://server.home:9633/metrics" {
		t.Errorf("wrong discovery URLs: %+v", first.Exporters)
	}
	if first.Exporters.Node.Mode != ModeAuto || first.Exporters.ZFS.Mode != ModeAuto || first.Exporters.Smartctl.Mode != ModeAuto {
		t.Errorf("exporters must default to auto: %+v", first.Exporters)
	}
	if nas.Label != "nas" || nas.Location != "lab" || nas.Exporters.ZFS.URL != "http://[2001:db8::1]:9134/metrics" {
		t.Errorf("unexpected IPv6 host: %+v", nas)
	}
	if nas.Exporters.Node.URL != "https://metrics.home/custom/node" || nas.Exporters.Node.Mode != ModeAuto {
		t.Errorf("URL overrides must remain optional: %+v", nas.Exporters.Node)
	}
	if nas.Exporters.ZFS.Mode != ModeEnabled || nas.Exporters.Smartctl.Mode != ModeDisabled || nas.Exporters.Smartctl.URL != "" {
		t.Errorf("exporter overrides not respected: %+v", nas.Exporters)
	}
}

func TestLoadHostsValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{name: "missing address", yaml: "hosts: [{label: nas}]", want: "missing address"},
		{name: "url as host", yaml: "hosts: ['http://nas:9100/metrics']", want: "hostname or IP"},
		{name: "host with port", yaml: "hosts: ['nas:9100']", want: "hostname or IP"},
		{name: "invalid mode", yaml: "hosts: [{address: nas, exporters: {zfs: {mode: enable}}}]", want: "mode must be"},
		{name: "unknown exporter", yaml: "hosts: [{address: nas, exporters: {nod: {mode: enabled}}}]", want: "nod"},
		{name: "unknown setting", yaml: "hosts: [{address: nas, exporters: {node: {enabld: true}}}]", want: "enabld"},
		{name: "unsupported scheme", yaml: "hosts: [{address: nas, exporters: {node: {url: 'file:///tmp/metrics'}}}]", want: "http or https"},
		{name: "missing URL host", yaml: "hosts: [{address: nas, exporters: {node: {url: 'http:///metrics'}}}]", want: "include a host"},
		{name: "duplicate label", yaml: "hosts: [{address: one, label: same}, {address: two, label: same}]", want: "duplicate host label"},
		{name: "mixed formats", yaml: "hosts: [nas]\nendpoints: ['http://old:9134/metrics']", want: "either hosts or legacy endpoints"},
		{name: "legacy missing URL", yaml: "endpoints: [{label: nas}]", want: "missing url"},
		{name: "numeric host", yaml: "hosts: [42]", want: "decode"},
		{name: "null host", yaml: "hosts: [null]", want: "missing address"},
		{name: "unknown parent", yaml: "hosts: [{address: vm, parent: pve}]", want: "not a configured host label"},
		{name: "self parent", yaml: "hosts: [{address: vm, parent: vm}]", want: "own parent"},
		{name: "nested guest", yaml: "hosts: [pve, {address: vm, parent: pve}, {address: ct, parent: vm}]", want: "only one nesting level"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := configFromYAML(t, tt.yaml)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadLegacyEndpoints(t *testing.T) {
	t.Parallel()
	cfg, err := configFromYAML(t, `endpoints:
  - url: http://nas:9134/metrics
    label: original-history-label
    smartctl_url: http://nas:9633/metrics
    node_exporter_url: http://nas:9100/metrics
  - http://second:9134/metrics
`)
	if err != nil {
		t.Fatal(err)
	}
	first, second := cfg.Hosts[0], cfg.Hosts[1]
	if first.Label != "original-history-label" || first.Exporters.ZFS.Mode != ModeEnabled {
		t.Fatalf("legacy ZFS/identity changed: %+v", first)
	}
	if first.Exporters.Node.Mode != ModeAuto || first.Exporters.Smartctl.Mode != ModeAuto {
		t.Fatalf("legacy companions must remain optional: %+v", first.Exporters)
	}
	if second.Label != "http://second:9134/metrics" || second.Exporters.Node.Mode != ModeDisabled || second.Exporters.Smartctl.Mode != ModeDisabled {
		t.Fatalf("legacy fallback identity or absent companions changed: %+v", second)
	}
}

func TestLoadHostsFlagsAndEnvironment(t *testing.T) {
	v := viper.New()
	v.SetConfigType("yaml")
	v.SetEnvPrefix("HOSTGLANCE")
	v.AutomaticEnv()
	if err := v.ReadConfig(strings.NewReader("hosts: [from-config]")); err != nil {
		t.Fatal(err)
	}
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.StringSlice("hosts", nil, "")
	if err := v.BindPFlag("hosts", flags.Lookup("hosts")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOSTGLANCE_HOSTS", "env-one,env-two")
	cfg, err := loadFrom(v)
	if err != nil || len(cfg.Hosts) != 2 || cfg.Hosts[0].Label != "env-one" {
		t.Fatalf("environment did not override config: cfg=%+v err=%v", cfg, err)
	}
	if err := flags.Parse([]string{"--hosts", "flag-one,flag-two"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadFrom(v)
	if err != nil || len(cfg.Hosts) != 2 || cfg.Hosts[0].Label != "flag-one" {
		t.Fatalf("flag did not override environment: cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadLegacyEndpointFlag(t *testing.T) {
	t.Parallel()
	v := viper.New()
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.StringSlice("endpoints", nil, "")
	if err := v.BindPFlag("endpoints", flags.Lookup("endpoints")); err != nil {
		t.Fatal(err)
	}
	if err := flags.Parse([]string{"--endpoints", "http://one/metrics,http://two/metrics"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadFrom(v)
	if err != nil || len(cfg.Hosts) != 2 {
		t.Fatalf("legacy --endpoints failed: cfg=%+v err=%v", cfg, err)
	}
}

func TestExampleConfig(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../config.yaml.example")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := configFromYAML(t, string(data))
	if err != nil || len(cfg.Hosts) == 0 {
		t.Fatalf("example config does not load: cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadHostParent(t *testing.T) {
	t.Parallel()
	cfg, err := configFromYAML(t, "hosts: [{address: 10.0.0.1, label: pve}, {address: 10.0.0.2, parent: ' pve '}]")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hosts[0].Parent != "" || cfg.Hosts[1].Parent != "pve" {
		t.Fatalf("parents = %q, %q", cfg.Hosts[0].Parent, cfg.Hosts[1].Parent)
	}
}
