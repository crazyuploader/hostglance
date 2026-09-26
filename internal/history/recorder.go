package history

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/crazyuploader/hostglance/internal/fetcher"
	"github.com/crazyuploader/hostglance/internal/model"
)

// Recorder polls the fetcher and writes metrics to the Store.
type Recorder struct {
	store    *Store
	fetcher  *fetcher.Fetcher
	interval time.Duration
}

// NewRecorder creates a Recorder that polls at the given interval.
// interval is clamped to a minimum of 1 second to prevent a zero/negative
// duration from causing time.NewTicker to panic.
func NewRecorder(store *Store, f *fetcher.Fetcher, interval time.Duration) *Recorder {
	if interval < time.Second {
		interval = time.Second
	}
	return &Recorder{store: store, fetcher: f, interval: interval}
}

// Run begins the recording loop; blocks until ctx is cancelled.
// Callers that want a background loop should invoke it as "go rec.Run(ctx)".
func (r *Recorder) Run(ctx context.Context) {
	// Initial prune on startup to clean up stale data.
	if err := r.store.Prune(); err != nil {
		slog.Warn("history prune failed", "error", err)
	}

	// Record immediately, then on every tick.
	r.record(ctx)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	pruneTicker := time.NewTicker(1 * time.Hour)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.record(ctx)
		case <-pruneTicker.C:
			if err := r.store.Prune(); err != nil {
				slog.Warn("history prune failed", "error", err)
			}
		}
	}
}

func (r *Recorder) record(ctx context.Context) {
	nodes, _ := r.fetcher.FetchAll(ctx)
	now := time.Now()
	var samples []Sample

	for _, node := range nodes {
		samples = append(samples, poolSamples(node, now)...)
		samples = append(samples, systemSamples(node, now)...)
		samples = append(samples, diskSamples(node, now)...)
	}

	if len(samples) == 0 {
		return
	}
	if err := r.store.WriteBatch(samples); err != nil {
		slog.Warn("history write failed", "error", err)
	} else {
		slog.Debug("history recorded", "samples", len(samples))
	}
}

// poolSamples builds the pool series samples for one node.
func poolSamples(node model.NodeData, now time.Time) []Sample {
	var samples []Sample
	for _, pool := range node.Pools {
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "pool", pool.Name, "used_pct"),
			Ts:  now, Value: pool.UsedPercent,
		})
		if pool.Allocated > 0 {
			samples = append(samples, Sample{
				Key:   SeriesKey(node.Label, "pool", pool.Name, "alloc_bytes"),
				Ts:    now,
				Value: pool.Allocated / (1 << 20), // Store in MiB to avoid float32 precision loss
			})
		}
		if pool.Free > 0 {
			samples = append(samples, Sample{
				Key:   SeriesKey(node.Label, "pool", pool.Name, "free_bytes"),
				Ts:    now,
				Value: pool.Free / (1 << 20), // Store in MiB to avoid float32 precision loss
			})
		}
		if pool.DedupRatio > 0 {
			samples = append(samples, Sample{
				Key: SeriesKey(node.Label, "pool", pool.Name, "dedup_ratio"),
				Ts:  now, Value: pool.DedupRatio,
			})
		}
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "pool", pool.Name, "frag_pct"),
			Ts:  now, Value: pool.FragmentationRatio * 100,
		})
	}
	return samples
}

// systemSamples builds the node_exporter series samples for one node.
// The fixed "node" name component keeps series keys stable even if the
// reported hostname changes. Per-filesystem, per-interface, and per-sensor
// readings get their own kind so they show up in the history tree as their
// own disks/interfaces/chips rather than being flattened into "system".
func systemSamples(node model.NodeData, now time.Time) []Sample {
	sys := node.System
	if sys == nil {
		return nil
	}
	var samples []Sample
	if sys.HasCPURates {
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "system", "node", "cpu_pct"),
			Ts:  now, Value: sys.CPUBusyPct,
		})
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "system", "node", "iowait_pct"),
			Ts:  now, Value: sys.CPUIOWaitPct,
		})
	}
	if sys.MemTotal > 0 {
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "system", "node", "mem_used_pct"),
			Ts:  now, Value: sys.MemUsedPct,
		})
	}
	if sys.SwapTotal > 0 {
		swapUsedPct := (sys.SwapTotal - sys.SwapFree) / sys.SwapTotal * 100
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "system", "node", "swap_used_pct"),
			Ts:  now, Value: swapUsedPct,
		})
	}
	if sys.HasLoad {
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "system", "node", "load1"),
			Ts:  now, Value: sys.Load1,
		})
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "system", "node", "load5"),
			Ts:  now, Value: sys.Load5,
		})
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "system", "node", "load15"),
			Ts:  now, Value: sys.Load15,
		})
	}
	if sys.HasPressureRates {
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "system", "node", "pressure_cpu_pct"),
			Ts:  now, Value: sys.PressureCPUPct,
		})
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "system", "node", "pressure_io_pct"),
			Ts:  now, Value: sys.PressureIOPct,
		})
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "system", "node", "pressure_mem_pct"),
			Ts:  now, Value: sys.PressureMemPct,
		})
	}
	for _, fs := range sys.Filesystems {
		if SkipFSMount(fs.Mountpoint) {
			continue // small boot partition; usage barely moves, not worth a time series
		}
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "fs", fs.Mountpoint, "used_pct"),
			Ts:  now, Value: fs.UsedPct,
		})
	}
	for _, n := range sys.Nets {
		if !n.HasRates {
			continue
		}
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "net", n.Name, "rx_bps"),
			Ts:  now, Value: n.RxBps,
		})
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "net", n.Name, "tx_bps"),
			Ts:  now, Value: n.TxBps,
		})
	}
	for _, t := range sys.Temps {
		samples = append(samples, Sample{
			Key: SeriesKey(node.Label, "temp", t.Chip+" "+t.Label, "temp_c"),
			Ts:  now, Value: t.Celsius,
		})
	}
	return samples
}

// diskSamples builds the SMART disk series samples for one node.
func diskSamples(node model.NodeData, now time.Time) []Sample {
	var samples []Sample
	for _, disk := range node.Disks {
		if disk.HasTemperature {
			samples = append(samples, Sample{
				Key: SeriesKey(node.Label, "disk", disk.Device, "temp_c"),
				Ts:  now, Value: disk.Temperature,
			})
		}
		if disk.HasPercentUsed {
			samples = append(samples, Sample{
				Key: SeriesKey(node.Label, "disk", disk.Device, "wear_pct"),
				Ts:  now, Value: disk.PercentageUsed,
			})
		}
		if disk.HasWearLeveling {
			samples = append(samples, Sample{
				Key: SeriesKey(node.Label, "disk", disk.Device, "wear_lvl"),
				Ts:  now, Value: disk.WearLevelingCount,
			})
		}
		if disk.PowerOnHours > 0 {
			samples = append(samples, Sample{
				Key: SeriesKey(node.Label, "disk", disk.Device, "pow_hrs"),
				Ts:  now, Value: disk.PowerOnHours,
			})
		}
		if disk.HasMediaErrors {
			samples = append(samples, Sample{
				Key: SeriesKey(node.Label, "disk", disk.Device, "media_errors"),
				Ts:  now, Value: disk.MediaErrors,
			})
		}
		if disk.ReallocatedSectors > 0 {
			samples = append(samples, Sample{
				Key: SeriesKey(node.Label, "disk", disk.Device, "realloc_sectors"),
				Ts:  now, Value: disk.ReallocatedSectors,
			})
		}
		if disk.PendingSectors > 0 {
			samples = append(samples, Sample{
				Key: SeriesKey(node.Label, "disk", disk.Device, "pending_sectors"),
				Ts:  now, Value: disk.PendingSectors,
			})
		}
		if disk.AvailableSpare > 0 {
			samples = append(samples, Sample{
				Key: SeriesKey(node.Label, "disk", disk.Device, "spare_pct"),
				Ts:  now, Value: disk.AvailableSpare,
			})
		}
		if disk.BytesWritten > 0 {
			samples = append(samples, Sample{
				Key:   SeriesKey(node.Label, "disk", disk.Device, "written_bytes"),
				Ts:    now,
				Value: disk.BytesWritten / (1 << 20), // Store in MiB to avoid float32 precision loss
			})
		}
	}
	return samples
}

// exactSkipMounts are boot-adjacent mountpoints excluded from history outright.
var exactSkipMounts = map[string]bool{
	"/boot": true,
}

// SkipFSMount reports whether a filesystem mount is a small, rarely-changing
// boot/firmware partition not worth trending over time (EFI system
// partitions, /boot, and similar) as opposed to an actual data volume.
// Exported so the series-list API can also hide any such mount recorded
// before this filter existed.
func SkipFSMount(mountpoint string) bool {
	lower := strings.ToLower(mountpoint)
	if strings.Contains(lower, "efi") {
		return true
	}
	return exactSkipMounts[lower]
}
