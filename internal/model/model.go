// Package model defines host, storage, and system metrics and their extraction.
package model

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/crazyuploader/hostglance/internal/parser"
)

// PoolHealth is the human-readable pool state.
type PoolHealth string

const (
	HealthOnline    PoolHealth = "ONLINE"
	HealthDegraded  PoolHealth = "DEGRADED"
	HealthFaulted   PoolHealth = "FAULTED"
	HealthOffline   PoolHealth = "OFFLINE"
	HealthUnavail   PoolHealth = "UNAVAIL"
	HealthRemoved   PoolHealth = "REMOVED"
	HealthSuspended PoolHealth = "SUSPENDED"
	HealthUnknown   PoolHealth = "UNKNOWN"
)

// Pool holds the key metrics for one ZFS pool.
type Pool struct {
	Name               string     `json:"name"`
	Health             PoolHealth `json:"health"`
	Size               float64    `json:"size"`
	Allocated          float64    `json:"allocated"`
	Free               float64    `json:"free"`
	Freeing            float64    `json:"freeing"`
	LeakedBytes        float64    `json:"leaked_bytes"`
	DedupRatio         float64    `json:"dedup_ratio"`
	FragmentationRatio float64    `json:"fragmentation_ratio"`
	ReadOnly           bool       `json:"read_only"`
	UsedPercent        float64    `json:"used_percent"`
	Datasets           []Dataset  `json:"datasets,omitempty"`
}

// Dataset holds filesystem, volume, or snapshot metrics within a pool.
type Dataset struct {
	Name              string  `json:"name"`
	Pool              string  `json:"pool"`
	Type              string  `json:"type"`
	Available         float64 `json:"available"`
	LogicalUsed       float64 `json:"logical_used"`
	Quota             float64 `json:"quota"`
	Referenced        float64 `json:"referenced"`
	UsedByDataset     float64 `json:"used_by_dataset"`
	Used              float64 `json:"used"`
	VolumeSize        float64 `json:"volume_size"`
	Written           float64 `json:"written"`
	UsedPercent       float64 `json:"used_percent"`
	QuotaUsedPercent  float64 `json:"quota_used_percent"`
	VolumeUsedPercent float64 `json:"volume_used_percent"`
}

// DiskInfo holds key SMART metrics for one physical disk.
type DiskInfo struct {
	Device               string  `json:"device"`
	ModelName            string  `json:"model_name,omitempty"`
	SerialNumber         string  `json:"serial_number,omitempty"`
	FirmwareVersion      string  `json:"firmware_version,omitempty"`
	Interface            string  `json:"interface,omitempty"`       // "sat", "nvme", "scsi"
	FormFactor           string  `json:"form_factor,omitempty"`     // "3.5 inches", ""
	Temperature          float64 `json:"temperature"`               // °C current, 0 if unknown
	HasTemperature       bool    `json:"has_temperature,omitempty"` // true when current temp metric was present (even if 0)
	TempMin              float64 `json:"temp_min,omitempty"`        // lifetime min °C
	TempMax              float64 `json:"temp_max,omitempty"`        // lifetime max °C
	TempTrip             float64 `json:"temp_trip,omitempty"`       // hardware trip/critical °C
	SmartPassed          bool    `json:"smart_passed"`
	PowerOnHours         float64 `json:"power_on_hours"`
	PowerCycles          float64 `json:"power_cycles,omitempty"`
	CapacityBytes        float64 `json:"capacity_bytes"`
	RotationRate         int     `json:"rotation_rate"`              // RPM; 0 = SSD/NVMe
	PercentageUsed       float64 `json:"percentage_used,omitempty"`  // SSD/NVMe wear 0–100
	AvailableSpare       float64 `json:"available_spare,omitempty"`  // NVMe spare %
	SpareThreshold       float64 `json:"spare_threshold,omitempty"`  // NVMe spare threshold %
	MediaErrors          float64 `json:"media_errors,omitempty"`     // unrecovered integrity errors
	HasMediaErrors       bool    `json:"has_media_errors,omitempty"` // true when metric was present (even if 0)
	CriticalWarning      float64 `json:"critical_warning,omitempty"`
	BytesRead            float64 `json:"bytes_read,omitempty"`
	BytesWritten         float64 `json:"bytes_written,omitempty"`
	ErrorLogCount        float64 `json:"error_log_count,omitempty"`
	HasPercentUsed       bool    `json:"has_percent_used,omitempty"` // true when metric was present (even if 0)
	ReallocatedSectors   float64 `json:"reallocated_sectors,omitempty"`
	PendingSectors       float64 `json:"pending_sectors,omitempty"`
	OfflineUncorrectable float64 `json:"offline_uncorrectable,omitempty"`
	ReportedUncorrect    float64 `json:"reported_uncorrect,omitempty"`
	UDMACRCErrors        float64 `json:"udma_crc_errors,omitempty"`
	LoadCycleCount       float64 `json:"load_cycle_count,omitempty"`
	InterfaceSpeed       float64 `json:"interface_speed,omitempty"` // bits/sec
	ExitStatus           float64 `json:"exit_status,omitempty"`
	HasExitStatus        bool    `json:"has_exit_status,omitempty"`

	// SATA SSD-specific SMART attributes
	WearLevelingCount    float64 `json:"wear_leveling_count,omitempty"`
	HasWearLeveling      bool    `json:"has_wear_leveling,omitempty"`
	UsedReservedBlocks   float64 `json:"used_reserved_blocks,omitempty"`
	ProgramFailCount     float64 `json:"program_fail_count,omitempty"`
	EraseFailCount       float64 `json:"erase_fail_count,omitempty"`
	AvailableReservedPct float64 `json:"available_reserved_pct,omitempty"`
	HasAvailReserved     bool    `json:"has_avail_reserved,omitempty"`
	TotalLBAsWritten     float64 `json:"total_lbas_written,omitempty"`
	TotalLBAsRead        float64 `json:"total_lbas_read,omitempty"`
}

// ExporterInfo holds metadata from zfs_exporter_build_info.
type ExporterInfo struct {
	Version   string `json:"version,omitempty"`
	GoVersion string `json:"go_version,omitempty"`
	Revision  string `json:"revision,omitempty"`
}

// SmartctlInfo holds metadata from smartctl_exporter_build_info / smartctl_version.
type SmartctlInfo struct {
	ExporterVersion string `json:"exporter_version,omitempty"`
	ToolVersion     string `json:"tool_version,omitempty"`
	GoVersion       string `json:"go_version,omitempty"`
}

// NodeData holds independently collected metrics for one configured host.
type NodeData struct {
	Label        string           `json:"label"`
	Location     string           `json:"location,omitempty"`
	FetchedAt    time.Time        `json:"fetched_at"`
	Error        string           `json:"error,omitempty"`
	Exporters    ExporterStatuses `json:"exporters"`
	ExporterInfo ExporterInfo     `json:"exporter_info,omitempty"`
	SmartctlInfo SmartctlInfo     `json:"smartctl_info,omitempty"`
	Pools        []Pool           `json:"pools"`
	Disks        []DiskInfo       `json:"disks,omitempty"`
	System       *SystemInfo      `json:"system,omitempty"`
}

func healthFromValue(v float64) PoolHealth {
	switch int(math.Round(v)) {
	case 0:
		return HealthOnline
	case 1:
		return HealthDegraded
	case 2:
		return HealthFaulted
	case 3:
		return HealthOffline
	case 4:
		return HealthUnavail
	case 5:
		return HealthRemoved
	case 6:
		return HealthSuspended
	default:
		return HealthUnknown
	}
}

// ExtractSmartctlInfo extracts metadata from smartctl_exporter_build_info and smartctl_version samples.
func ExtractSmartctlInfo(samples []parser.Sample) SmartctlInfo {
	var info SmartctlInfo
	for _, s := range samples {
		switch s.Name {
		case "smartctl_exporter_build_info":
			if info.ExporterVersion == "" {
				info.ExporterVersion = s.Labels["version"]
			}
			if info.GoVersion == "" {
				info.GoVersion = s.Labels["goversion"]
			}
		case "smartctl_version":
			if info.ToolVersion == "" {
				info.ToolVersion = s.Labels["json_format_version"]
				if info.ToolVersion == "" {
					info.ToolVersion = s.Labels["smartctl_version"]
				}
			}
		}
	}
	return info
}

// ExtractExporterInfo extracts metadata from the zfs_exporter_build_info sample.
func ExtractExporterInfo(samples []parser.Sample) ExporterInfo {
	for _, s := range samples {
		if s.Name == "zfs_exporter_build_info" {
			return ExporterInfo{
				Version:   s.Labels["version"],
				GoVersion: s.Labels["goversion"],
				Revision:  s.Labels["revision"],
			}
		}
	}
	return ExporterInfo{}
}

// ExtractDisks builds DiskInfo structs from smartctl_exporter Prometheus samples.
// The mapping between pool and disk is not available in the metrics, so disks
// are returned at node level only.
func ExtractDisks(samples []parser.Sample) []DiskInfo {
	disks := map[string]*DiskInfo{}

	ensure := func(device string) *DiskInfo {
		if _, ok := disks[device]; !ok {
			disks[device] = &DiskInfo{Device: device, SmartPassed: true}
		}
		return disks[device]
	}

	for _, s := range samples {
		device := s.Labels["device"]
		if device == "" {
			continue
		}
		applyDiskSample(ensure(device), s)
	}

	if len(disks) == 0 {
		return nil
	}

	result := make([]DiskInfo, 0, len(disks))
	for _, d := range disks {
		result = append(result, *d)
	}
	slices.SortFunc(result, func(a, b DiskInfo) int {
		return strings.Compare(a.Device, b.Device)
	})
	return result
}

// applyDiskSample updates d from one smartctl_exporter sample.
func applyDiskSample(d *DiskInfo, s parser.Sample) {
	switch s.Name {
	case "smartctl_device":
		applyDiskIdentity(d, s)
	case "smartctl_device_temperature":
		applyDiskTemperature(d, s)
	case "smartctl_device_smart_status":
		d.SmartPassed = s.Value == 1
	case "smartctl_device_power_on_seconds":
		d.PowerOnHours = s.Value / 3600
	case "smartctl_device_power_cycle_count":
		d.PowerCycles = s.Value
	case "smartctl_device_capacity_bytes":
		d.CapacityBytes = s.Value
	case "smartctl_device_rotation_rate":
		d.RotationRate = int(math.Round(s.Value))
	case "smartctl_device_available_spare":
		d.AvailableSpare = s.Value
	case "smartctl_device_available_spare_threshold":
		d.SpareThreshold = s.Value
	case "smartctl_device_critical_warning":
		d.CriticalWarning = s.Value
	case "smartctl_device_bytes_read":
		d.BytesRead = s.Value
	case "smartctl_device_bytes_written":
		d.BytesWritten = s.Value
	case "smartctl_device_smartctl_exit_status":
		d.ExitStatus = s.Value
		d.HasExitStatus = true
	default:
		applyDiskCounterSample(d, s)
	}
}

// applyDiskCounterSample handles the remaining metrics whose handling
// depends on sample labels (has-flags, conditional label matches).
func applyDiskCounterSample(d *DiskInfo, s parser.Sample) {
	switch s.Name {
	case "smartctl_device_percentage_used":
		d.PercentageUsed = s.Value
		d.HasPercentUsed = true
	case "smartctl_device_media_errors":
		d.MediaErrors = s.Value
		d.HasMediaErrors = true
	case "smartctl_device_error_log_count":
		if s.Labels["error_log_type"] == "summary" {
			d.ErrorLogCount = s.Value
		}
	case "smartctl_device_num_err_log_entries":
		// NVMe error log entries (separate metric from HDD error_log_count)
		if d.ErrorLogCount == 0 {
			d.ErrorLogCount = s.Value
		}
	case "smartctl_device_interface_speed":
		if s.Labels["speed_type"] == "current" {
			d.InterfaceSpeed = s.Value
		}
	case "smartctl_device_attribute":
		applyDiskAttribute(d, s)
	}
}

func applyDiskIdentity(d *DiskInfo, s parser.Sample) {
	if d.ModelName == "" {
		d.ModelName = s.Labels["model_name"]
	}
	if d.SerialNumber == "" {
		d.SerialNumber = s.Labels["serial_number"]
	}
	if d.FirmwareVersion == "" {
		d.FirmwareVersion = s.Labels["firmware_version"]
	}
	if d.Interface == "" {
		d.Interface = s.Labels["interface"]
	}
	if d.FormFactor == "" {
		d.FormFactor = s.Labels["form_factor"]
	}
}

func applyDiskTemperature(d *DiskInfo, s parser.Sample) {
	switch s.Labels["temperature_type"] {
	case "current":
		d.Temperature = s.Value
		d.HasTemperature = true
	case "drive_trip":
		d.TempTrip = s.Value
	case "lifetime_min":
		if d.TempMin == 0 || s.Value < d.TempMin {
			d.TempMin = s.Value
		}
	case "lifetime_max":
		if d.TempMax == 0 || s.Value > d.TempMax {
			d.TempMax = s.Value
		}
	}
}

func applyDiskAttribute(d *DiskInfo, s parser.Sample) {
	if s.Labels["attribute_value_type"] != "raw" {
		return
	}
	switch s.Labels["attribute_name"] {
	case "Reallocated_Sector_Ct":
		d.ReallocatedSectors = s.Value
	case "Current_Pending_Sector":
		d.PendingSectors = s.Value
	case "Offline_Uncorrectable":
		d.OfflineUncorrectable = s.Value
	case "Reported_Uncorrect":
		d.ReportedUncorrect = s.Value
	case "UDMA_CRC_Error_Count":
		d.UDMACRCErrors = s.Value
	case "Load_Cycle_Count":
		d.LoadCycleCount = s.Value
	// SATA SSD attributes
	case "Wear_Leveling_Count":
		d.WearLevelingCount = s.Value
		d.HasWearLeveling = true
	case "Used_Rsvd_Blk_Cnt_Chip", "Used_Rsvd_Blk_Cnt_Tot":
		d.UsedReservedBlocks = s.Value
	case "Program_Fail_Cnt_Total", "Program_Fail_Count_Chip":
		d.ProgramFailCount = s.Value
	case "Erase_Fail_Count_Total", "Erase_Fail_Count_Chip", "Erase_Fail_Count":
		d.EraseFailCount = s.Value
	case "Available_Reservd_Space", "Available_Reserved_Space":
		d.AvailableReservedPct = s.Value
		d.HasAvailReserved = true
	case "Total_LBAs_Written":
		d.TotalLBAsWritten = s.Value
	case "Total_LBAs_Read":
		d.TotalLBAsRead = s.Value
	}
}

// ExtractPools builds Pool structs from a flat Prometheus sample slice.
func ExtractPools(samples []parser.Sample) []Pool {
	pools := map[string]*Pool{}
	datasets := map[string]*Dataset{}
	ensure := func(name string) *Pool {
		if _, ok := pools[name]; !ok {
			pools[name] = &Pool{Name: name, Health: HealthUnknown}
		}
		return pools[name]
	}
	ensureDataset := func(pool, name, datasetType string) *Dataset {
		key := pool + "/" + name
		if _, ok := datasets[key]; !ok {
			datasets[key] = &Dataset{
				Name: name,
				Pool: pool,
				Type: datasetType,
			}
		}
		if datasetType != "" {
			datasets[key].Type = datasetType
		}
		return datasets[key]
	}

	for _, s := range samples {
		pool := s.Labels["pool"]
		if pool == "" {
			continue
		}
		if datasetName := s.Labels["name"]; datasetName != "" {
			applyDatasetSample(ensureDataset(pool, datasetName, s.Labels["type"]), s)
		}
		applyPoolSample(ensure(pool), s)
	}

	result := make([]Pool, 0, len(pools))
	for _, d := range datasets {
		finalizeDataset(d)
		if p, ok := pools[d.Pool]; ok {
			p.Datasets = append(p.Datasets, *d)
		}
	}

	for _, p := range pools {
		finalizePool(p)
		result = append(result, *p)
	}

	slices.SortFunc(result, func(a, b Pool) int {
		return strings.Compare(a.Name, b.Name)
	})

	return result
}

func applyDatasetSample(d *Dataset, s parser.Sample) {
	switch s.Name {
	case "zfs_dataset_available_bytes":
		d.Available = s.Value
	case "zfs_dataset_logical_used_bytes":
		d.LogicalUsed = s.Value
	case "zfs_dataset_quota_bytes":
		d.Quota = s.Value
	case "zfs_dataset_referenced_bytes":
		d.Referenced = s.Value
	case "zfs_dataset_used_by_dataset_bytes":
		d.UsedByDataset = s.Value
	case "zfs_dataset_used_bytes":
		d.Used = s.Value
	case "zfs_dataset_volume_size_bytes":
		d.VolumeSize = s.Value
	case "zfs_dataset_written_bytes":
		d.Written = s.Value
	}
}

func applyPoolSample(p *Pool, s parser.Sample) {
	switch s.Name {
	case "zfs_pool_health":
		p.Health = healthFromValue(s.Value)
	case "zfs_pool_size_bytes":
		p.Size = s.Value
	case "zfs_pool_allocated_bytes":
		p.Allocated = s.Value
	case "zfs_pool_free_bytes":
		p.Free = s.Value
	case "zfs_pool_freeing_bytes":
		p.Freeing = s.Value
	case "zfs_pool_leaked_bytes":
		p.LeakedBytes = s.Value
	case "zfs_pool_deduplication_ratio":
		p.DedupRatio = s.Value
	case "zfs_pool_fragmentation_ratio":
		p.FragmentationRatio = s.Value
	case "zfs_pool_readonly":
		p.ReadOnly = s.Value != 0
	}
}

// finalizeDataset computes derived percentage fields for one dataset.
func finalizeDataset(d *Dataset) {
	if d.Used > 0 && d.Available > 0 {
		total := d.Used + d.Available
		d.UsedPercent = math.Round((d.Used/total)*10000) / 100
	}
	if d.Quota > 0 {
		d.QuotaUsedPercent = math.Round((d.Used/d.Quota)*10000) / 100
	}
	if d.VolumeSize > 0 {
		d.VolumeUsedPercent = math.Round((d.Used/d.VolumeSize)*10000) / 100
	}
}

// finalizePool reconciles size/allocated/free and sorts datasets for one pool.
func finalizePool(p *Pool) {
	if p.Size > 0 {
		if p.Allocated > 0 && p.Free == 0 {
			p.Free = p.Size - p.Allocated
		} else if p.Free > 0 && p.Allocated == 0 {
			p.Allocated = p.Size - p.Free
		}
		p.UsedPercent = math.Round((p.Allocated/p.Size)*10000) / 100
	}
	slices.SortFunc(p.Datasets, func(a, b Dataset) int {
		if a.Used != b.Used {
			if a.Used > b.Used {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// HumanBytes returns a human-readable byte string using IEC units
// (e.g. "3.72 TiB") — the divisor is 1024, so the labels say so.
func HumanBytes(b float64) string {
	const unit = 1024.0
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	if b < unit {
		return fmt.Sprintf("%.0f B", b)
	}
	div, exp := unit, 0
	for n := b / unit; n >= unit && exp < len(units)-1; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %s", b/div, units[exp])
}
