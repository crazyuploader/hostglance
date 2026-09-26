package model

import (
	"strings"
	"testing"

	"github.com/crazyuploader/hostglance/internal/parser"
)

// nodeExporterFixture mirrors the label shapes of a real node_exporter 1.11
// scrape from a Proxmox VE node.
const nodeExporterFixture = `
node_cpu_seconds_total{cpu="0",mode="idle"} 73102.66
node_cpu_seconds_total{cpu="0",mode="iowait"} 6763.85
node_cpu_seconds_total{cpu="0",mode="user"} 19126.62
node_cpu_seconds_total{cpu="0",mode="system"} 4901.34
node_cpu_seconds_total{cpu="1",mode="idle"} 76207.79
node_cpu_seconds_total{cpu="1",mode="iowait"} 6385.74
node_cpu_seconds_total{cpu="1",mode="user"} 20972.02
node_cpu_seconds_total{cpu="1",mode="system"} 5345.22
node_load1 1.5
node_load5 1.2
node_load15 0.9
node_time_seconds 1784374251.4
node_boot_time_seconds 1784262702
node_memory_MemTotal_bytes 16629796864
node_memory_MemAvailable_bytes 1962213376
node_memory_Buffers_bytes 46288896
node_memory_Cached_bytes 8249925632
node_memory_SwapTotal_bytes 8589930496
node_memory_SwapFree_bytes 5553860608
node_filesystem_size_bytes{device="/dev/mapper/pve-root",device_error="",fstype="ext4",mountpoint="/"} 100000000000
node_filesystem_avail_bytes{device="/dev/mapper/pve-root",device_error="",fstype="ext4",mountpoint="/"} 17395597312
node_filesystem_size_bytes{device="/dev/nvme0n1p2",device_error="",fstype="vfat",mountpoint="/boot/efi"} 1100000000
node_filesystem_avail_bytes{device="/dev/nvme0n1p2",device_error="",fstype="vfat",mountpoint="/boot/efi"} 1062166528
node_filesystem_size_bytes{device="nova",device_error="",fstype="zfs",mountpoint="/nova"} 2000000000000
node_filesystem_avail_bytes{device="nova",device_error="",fstype="zfs",mountpoint="/nova"} 1243742076928
node_filesystem_size_bytes{device="tmpfs",device_error="",fstype="tmpfs",mountpoint="/run"} 1662980096
node_network_receive_bytes_total{device="lo"} 2443612
node_network_receive_bytes_total{device="nic0"} 26822665065
node_network_transmit_bytes_total{device="nic0"} 12000000000
node_network_receive_bytes_total{device="tailscale0"} 336502062
node_network_transmit_bytes_total{device="tailscale0"} 100000000
node_network_receive_bytes_total{device="veth302i0"} 116939597
node_network_receive_bytes_total{device="tap301i0"} 4896114962
node_network_receive_bytes_total{device="fwbr301i0"} 12345
node_pressure_cpu_waiting_seconds_total 3716.54
node_pressure_io_waiting_seconds_total 11512.66
node_pressure_memory_waiting_seconds_total 27.31
node_hwmon_temp_celsius{chip="nvme_nvme0",sensor="temp1"} 41.85
node_hwmon_temp_celsius{chip="platform_coretemp_0",sensor="temp1"} 56
node_hwmon_chip_names{chip="nvme_nvme0",chip_name="nvme"} 1
node_hwmon_chip_names{chip="platform_coretemp_0",chip_name="coretemp"} 1
node_hwmon_sensor_label{chip="platform_coretemp_0",label="Package id 0",sensor="temp1"} 1
node_hwmon_sensor_label{chip="nvme_nvme0",label="Composite",sensor="temp1"} 1
node_uname_info{domainname="(none)",machine="x86_64",nodename="PVE02",release="7.0.14-5-pve",sysname="Linux",version="#1"} 1
node_os_info{id="debian",name="Debian GNU/Linux",pretty_name="Debian GNU/Linux 13 (trixie)",version_id="13"} 1
node_exporter_build_info{branch="HEAD",goarch="amd64",goos="linux",goversion="go1.26.1",revision="0dd664d",version="1.11.1"} 1
`

func parseFixture(t *testing.T) []parser.Sample {
	t.Helper()
	samples, err := parser.Parse(strings.NewReader(nodeExporterFixture))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return samples
}

// skipcq: GO-R1005 -- one assertion block per extracted field group
func TestExtractSystem(t *testing.T) {
	sys := ExtractSystem(parseFixture(t))
	if sys == nil {
		t.Fatal("ExtractSystem returned nil")
	}

	if sys.Hostname != "PVE02" {
		t.Errorf("Hostname = %q, want PVE02", sys.Hostname)
	}
	if sys.Kernel != "7.0.14-5-pve" {
		t.Errorf("Kernel = %q", sys.Kernel)
	}
	if sys.OSPretty != "Debian GNU/Linux 13 (trixie)" {
		t.Errorf("OSPretty = %q", sys.OSPretty)
	}
	if sys.ExporterVersion != "1.11.1" {
		t.Errorf("ExporterVersion = %q", sys.ExporterVersion)
	}
	if sys.Cores != 2 {
		t.Errorf("Cores = %d, want 2", sys.Cores)
	}
	if sys.Load1 != 1.5 || sys.Load5 != 1.2 || sys.Load15 != 0.9 {
		t.Errorf("loads = %v/%v/%v", sys.Load1, sys.Load5, sys.Load15)
	}
	if sys.UptimeSecs < 111000 || sys.UptimeSecs > 112000 {
		t.Errorf("UptimeSecs = %v", sys.UptimeSecs)
	}
	wantMemPct := (16629796864.0 - 1962213376.0) / 16629796864.0 * 100
	if diff := sys.MemUsedPct - wantMemPct; diff > 0.01 || diff < -0.01 {
		t.Errorf("MemUsedPct = %v, want %v", sys.MemUsedPct, wantMemPct)
	}

	// ZFS mounts remain visible even without a separate ZFS exporter; tmpfs is filtered.
	if len(sys.Filesystems) != 3 {
		t.Fatalf("Filesystems = %+v, want 3 entries", sys.Filesystems)
	}
	if sys.Filesystems[0].Mountpoint != "/" || sys.Filesystems[1].Mountpoint != "/boot/efi" || sys.Filesystems[2].Mountpoint != "/nova" {
		t.Errorf("filesystem order/mounts wrong: %+v", sys.Filesystems)
	}
	if sys.Filesystems[0].UsedPct < 82 || sys.Filesystems[0].UsedPct > 83 {
		t.Errorf("root UsedPct = %v", sys.Filesystems[0].UsedPct)
	}

	// Networks: lo/veth/tap/fwbr filtered; nic0 + tailscale0 remain.
	if len(sys.Nets) != 2 {
		t.Fatalf("Nets = %+v, want 2", sys.Nets)
	}
	if sys.Nets[0].Name != "nic0" || sys.Nets[1].Name != "tailscale0" {
		t.Errorf("net names: %+v", sys.Nets)
	}
	if sys.Nets[0].HasRates {
		t.Error("first scrape should have no net rates")
	}

	// Temps: joined names, hottest first.
	if len(sys.Temps) != 2 {
		t.Fatalf("Temps = %+v, want 2", sys.Temps)
	}
	if sys.Temps[0].Chip != "coretemp" || sys.Temps[0].Label != "Package id 0" || sys.Temps[0].Celsius != 56 {
		t.Errorf("hottest temp = %+v", sys.Temps[0])
	}
	if sys.Temps[1].Chip != "nvme" || sys.Temps[1].Label != "Composite" {
		t.Errorf("second temp = %+v", sys.Temps[1])
	}

	// Raw counters captured for the rate tracker.
	if sys.Counters == nil || sys.Counters.CPUTotal == 0 {
		t.Fatal("Counters not captured")
	}
	if sys.HasCPURates || sys.HasPressureRates {
		t.Error("extraction alone must not set rate flags")
	}
}

func TestExtractSystemEmpty(t *testing.T) {
	if got := ExtractSystem(nil); got != nil {
		t.Errorf("ExtractSystem(nil) = %+v, want nil", got)
	}
	samples, _ := parser.Parse(strings.NewReader("some_other_metric 1\n"))
	if got := ExtractSystem(samples); got != nil {
		t.Errorf("ExtractSystem(unrelated) = %+v, want nil", got)
	}
}

func TestExtractSystemWithPartialCollectors(t *testing.T) {
	samples, err := parser.Parse(strings.NewReader(`node_filesystem_size_bytes{device="tank",fstype="zfs",mountpoint="/tank"} 1000
node_filesystem_avail_bytes{device="tank",fstype="zfs",mountpoint="/tank"} 400
`))
	if err != nil {
		t.Fatal(err)
	}
	sys := ExtractSystem(samples)
	if sys == nil || len(sys.Filesystems) != 1 || sys.Filesystems[0].UsedPct != 60 {
		t.Fatalf("partial node_exporter metrics were discarded: %+v", sys)
	}
}
