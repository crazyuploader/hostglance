package model

import (
	"slices"
	"strings"
	"time"

	"github.com/crazyuploader/hostglance/internal/parser"
)

// FSInfo holds usage for one real filesystem mount.
type FSInfo struct {
	Device     string  `json:"device"`
	FSType     string  `json:"fstype"`
	Mountpoint string  `json:"mountpoint"`
	SizeBytes  float64 `json:"size_bytes"`
	AvailBytes float64 `json:"avail_bytes"`
	UsedPct    float64 `json:"used_pct"`
}

// NetDev holds throughput rates for one network interface.
type NetDev struct {
	Name     string  `json:"name"`
	RxBps    float64 `json:"rx_bps"` // bytes/sec
	TxBps    float64 `json:"tx_bps"` // bytes/sec
	HasRates bool    `json:"has_rates"`
}

// TempSensor holds one hwmon temperature reading.
type TempSensor struct {
	Chip    string  `json:"chip"`  // e.g. "coretemp"
	Label   string  `json:"label"` // e.g. "Package id 0"
	Celsius float64 `json:"celsius"`
}

// NetCounters holds raw byte counters for one interface.
type NetCounters struct {
	Rx, Tx float64
}

// SystemCounters is the raw counter snapshot used for rate computation.
// It is never serialized; the fetcher's rate tracker keeps the previous
// snapshot per endpoint and fills the *Pct/*Bps fields from deltas.
type SystemCounters struct {
	At                           time.Time
	CPUTotal, CPUIdle, CPUIOWait float64 // seconds, summed across all cpus
	Net                          map[string]NetCounters
	PCPUWait, PIOWait, PMemWait  float64 // pressure waiting seconds
}

// SystemInfo holds node-level metrics extracted from node_exporter.
type SystemInfo struct {
	Hostname        string `json:"hostname,omitempty"`
	Kernel          string `json:"kernel,omitempty"`
	OSPretty        string `json:"os_pretty,omitempty"`
	ExporterVersion string `json:"exporter_version,omitempty"`

	Cores      int     `json:"cores"`
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	Load15     float64 `json:"load15"`
	HasLoad    bool    `json:"has_load,omitempty"` // true when node_load1 was present
	UptimeSecs float64 `json:"uptime_secs"`

	MemTotal     float64 `json:"mem_total"`
	MemAvailable float64 `json:"mem_available"`
	Buffers      float64 `json:"buffers"`
	Cached       float64 `json:"cached"`
	SwapTotal    float64 `json:"swap_total"`
	SwapFree     float64 `json:"swap_free"`
	MemUsedPct   float64 `json:"mem_used_pct"`

	CPUBusyPct   float64 `json:"cpu_busy_pct"`
	CPUIOWaitPct float64 `json:"cpu_iowait_pct"`
	HasCPURates  bool    `json:"has_cpu_rates"`

	PressureCPUPct   float64 `json:"pressure_cpu_pct"`
	PressureIOPct    float64 `json:"pressure_io_pct"`
	PressureMemPct   float64 `json:"pressure_mem_pct"`
	HasPressureRates bool    `json:"has_pressure_rates"`

	Filesystems []FSInfo     `json:"filesystems,omitempty"`
	Nets        []NetDev     `json:"nets,omitempty"`
	Temps       []TempSensor `json:"temps,omitempty"`

	Counters *SystemCounters `json:"-"`
}

// realFSTypes are filesystem types shown on the system page. Include ZFS
// mounts because a host need not have a separate ZFS exporter.
var realFSTypes = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true,
	"xfs": true, "btrfs": true, "vfat": true,
	"f2fs": true, "ntfs": true, "zfs": true,
}

// skipNetPrefixes are interface prefixes hidden from the network list:
// loopback plus Proxmox guest-facing virtual devices.
var skipNetPrefixes = []string{"veth", "tap", "fwbr", "fwpr", "fwln"}

func skipNetDevice(name string) bool {
	if name == "lo" {
		return true
	}
	for _, p := range skipNetPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// ExtractSystem builds a SystemInfo from node_exporter samples.
// Counter-based fields (CPU %, net rates, pressure %) are left unset here;
// the fetcher's rate tracker fills them from consecutive snapshots.
// One flat switch over sample names keeps extraction in a single pass;
// complexity is inherent to the metric count, not branching logic.
// skipcq: GO-R1005
func ExtractSystem(samples []parser.Sample) *SystemInfo {
	if len(samples) == 0 {
		return nil
	}

	sys := &SystemInfo{
		Counters: &SystemCounters{Net: map[string]NetCounters{}},
	}
	var timeSecs, bootSecs float64
	cores := map[string]bool{}
	fs := map[string]*FSInfo{}
	tempVals := map[string]map[string]float64{} // chip -> sensor -> °C
	chipNames := map[string]string{}            // chip -> friendly name
	sensorLabels := map[string]map[string]string{}
	recognized := false

	for _, s := range samples {
		recognized = recognized || strings.HasPrefix(s.Name, "node_")
		switch s.Name {
		case "node_cpu_seconds_total":
			cores[s.Labels["cpu"]] = true
			sys.Counters.CPUTotal += s.Value
			switch s.Labels["mode"] {
			case "idle":
				sys.Counters.CPUIdle += s.Value
			case "iowait":
				sys.Counters.CPUIOWait += s.Value
			}
		case "node_load1":
			sys.Load1 = s.Value
			sys.HasLoad = true
		case "node_load5":
			sys.Load5 = s.Value
		case "node_load15":
			sys.Load15 = s.Value
		case "node_time_seconds":
			timeSecs = s.Value
		case "node_boot_time_seconds":
			bootSecs = s.Value
		case "node_memory_MemTotal_bytes":
			sys.MemTotal = s.Value
		case "node_memory_MemAvailable_bytes":
			sys.MemAvailable = s.Value
		case "node_memory_Buffers_bytes":
			sys.Buffers = s.Value
		case "node_memory_Cached_bytes":
			sys.Cached = s.Value
		case "node_memory_SwapTotal_bytes":
			sys.SwapTotal = s.Value
		case "node_memory_SwapFree_bytes":
			sys.SwapFree = s.Value
		case "node_filesystem_size_bytes":
			if f := ensureFS(fs, s); f != nil {
				f.SizeBytes = s.Value
			}
		case "node_filesystem_avail_bytes":
			if f := ensureFS(fs, s); f != nil {
				f.AvailBytes = s.Value
			}
		case "node_network_receive_bytes_total":
			if dev := s.Labels["device"]; dev != "" && !skipNetDevice(dev) {
				nc := sys.Counters.Net[dev]
				nc.Rx = s.Value
				sys.Counters.Net[dev] = nc
			}
		case "node_network_transmit_bytes_total":
			if dev := s.Labels["device"]; dev != "" && !skipNetDevice(dev) {
				nc := sys.Counters.Net[dev]
				nc.Tx = s.Value
				sys.Counters.Net[dev] = nc
			}
		case "node_pressure_cpu_waiting_seconds_total":
			sys.Counters.PCPUWait = s.Value
		case "node_pressure_io_waiting_seconds_total":
			sys.Counters.PIOWait = s.Value
		case "node_pressure_memory_waiting_seconds_total":
			sys.Counters.PMemWait = s.Value
		case "node_hwmon_temp_celsius":
			chip, sensor := s.Labels["chip"], s.Labels["sensor"]
			if tempVals[chip] == nil {
				tempVals[chip] = map[string]float64{}
			}
			tempVals[chip][sensor] = s.Value
		case "node_hwmon_chip_names":
			chipNames[s.Labels["chip"]] = s.Labels["chip_name"]
		case "node_hwmon_sensor_label":
			chip := s.Labels["chip"]
			if sensorLabels[chip] == nil {
				sensorLabels[chip] = map[string]string{}
			}
			sensorLabels[chip][s.Labels["sensor"]] = s.Labels["label"]
		case "node_uname_info":
			sys.Hostname = s.Labels["nodename"]
			sys.Kernel = s.Labels["release"]
		case "node_os_info":
			sys.OSPretty = s.Labels["pretty_name"]
		case "node_exporter_build_info":
			sys.ExporterVersion = s.Labels["version"]
		}
	}

	if !recognized {
		return nil
	}

	sys.Cores = len(cores)
	if timeSecs > 0 && bootSecs > 0 && timeSecs > bootSecs {
		sys.UptimeSecs = timeSecs - bootSecs
	}
	if sys.MemTotal > 0 {
		sys.MemUsedPct = (sys.MemTotal - sys.MemAvailable) / sys.MemTotal * 100
	}

	sys.Filesystems = finalizeFS(fs)
	sys.Temps = finalizeTemps(tempVals, chipNames, sensorLabels)

	// Net devices are listed even before rates exist so the UI can render
	// the interface names on the first scrape.
	for dev := range sys.Counters.Net {
		sys.Nets = append(sys.Nets, NetDev{Name: dev})
	}
	slices.SortFunc(sys.Nets, func(a, b NetDev) int {
		return strings.Compare(a.Name, b.Name)
	})

	return sys
}

// ensureFS returns the FSInfo for a filesystem sample, or nil when the
// fstype is not a real local filesystem.
func ensureFS(fs map[string]*FSInfo, s parser.Sample) *FSInfo {
	if !realFSTypes[s.Labels["fstype"]] {
		return nil
	}
	mp := s.Labels["mountpoint"]
	if mp == "" {
		return nil
	}
	if _, ok := fs[mp]; !ok {
		fs[mp] = &FSInfo{
			Device:     s.Labels["device"],
			FSType:     s.Labels["fstype"],
			Mountpoint: mp,
		}
	}
	return fs[mp]
}

func finalizeFS(fs map[string]*FSInfo) []FSInfo {
	if len(fs) == 0 {
		return nil
	}
	out := make([]FSInfo, 0, len(fs))
	for _, f := range fs {
		if f.SizeBytes > 0 {
			f.UsedPct = (f.SizeBytes - f.AvailBytes) / f.SizeBytes * 100
		}
		out = append(out, *f)
	}
	slices.SortFunc(out, func(a, b FSInfo) int {
		return strings.Compare(a.Mountpoint, b.Mountpoint)
	})
	return out
}

// finalizeTemps joins temperature values with chip names and sensor labels
// into display-ready sensors, sorted hottest first.
func finalizeTemps(vals map[string]map[string]float64, chipNames map[string]string, sensorLabels map[string]map[string]string) []TempSensor {
	var out []TempSensor
	for chip, sensors := range vals {
		name := chipNames[chip]
		if name == "" {
			name = chip
		}
		for sensor, v := range sensors {
			label := ""
			if m := sensorLabels[chip]; m != nil {
				label = m[sensor]
			}
			if label == "" {
				label = sensor
			}
			out = append(out, TempSensor{Chip: name, Label: label, Celsius: v})
		}
	}
	slices.SortFunc(out, func(a, b TempSensor) int {
		if a.Celsius != b.Celsius {
			if a.Celsius > b.Celsius {
				return -1
			}
			return 1
		}
		if c := strings.Compare(a.Chip, b.Chip); c != 0 {
			return c
		}
		return strings.Compare(a.Label, b.Label)
	})
	return out
}
