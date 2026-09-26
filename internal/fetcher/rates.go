package fetcher

import (
	"sync"
	"time"

	"github.com/crazyuploader/hostglance/internal/model"
)

// minRateInterval guards against nonsense rates from back-to-back scrapes.
const minRateInterval = time.Second

// rateTracker computes per-second rates for counter-based node_exporter
// metrics (CPU, network, pressure) from consecutive scrape snapshots.
// Keyed by node_exporter URL so label renames on config reload do not
// reset rate state.
type rateTracker struct {
	mu   sync.Mutex
	prev map[string]*model.SystemCounters
}

// apply fills sys's rate fields from the previous snapshot for url (if any)
// and stores sys's counters as the new snapshot. Negative deltas (counter
// reset after a reboot) and sub-second intervals leave the Has*Rates flags
// false.
// skipcq: GO-R1005 -- guard clauses per counter family, not deep branching
func (r *rateTracker) apply(url string, sys *model.SystemInfo) {
	if sys == nil || sys.Counters == nil {
		return
	}
	cur := sys.Counters
	cur.At = time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.prev == nil {
		r.prev = map[string]*model.SystemCounters{}
	}
	prev := r.prev[url]
	r.prev[url] = cur
	if prev == nil {
		return
	}

	dt := cur.At.Sub(prev.At)
	if dt < minRateInterval {
		return
	}
	secs := dt.Seconds()

	// CPU busy/iowait percent of total cpu-seconds elapsed.
	dTotal := cur.CPUTotal - prev.CPUTotal
	dIdle := cur.CPUIdle - prev.CPUIdle
	dIOWait := cur.CPUIOWait - prev.CPUIOWait
	if dTotal > 0 && dIdle >= 0 && dIOWait >= 0 {
		sys.CPUBusyPct = clampPct((dTotal - dIdle - dIOWait) / dTotal * 100)
		sys.CPUIOWaitPct = clampPct(dIOWait / dTotal * 100)
		sys.HasCPURates = true
	}

	// Pressure stall percentages (waiting seconds per elapsed second).
	dpc := cur.PCPUWait - prev.PCPUWait
	dpi := cur.PIOWait - prev.PIOWait
	dpm := cur.PMemWait - prev.PMemWait
	if dpc >= 0 && dpi >= 0 && dpm >= 0 {
		sys.PressureCPUPct = clampPct(dpc / secs * 100)
		sys.PressureIOPct = clampPct(dpi / secs * 100)
		sys.PressureMemPct = clampPct(dpm / secs * 100)
		sys.HasPressureRates = true
	}

	// Network throughput per device; devices with a reset are skipped
	// individually.
	for i := range sys.Nets {
		dev := &sys.Nets[i]
		curN, okCur := cur.Net[dev.Name]
		prevN, okPrev := prev.Net[dev.Name]
		if !okCur || !okPrev {
			continue
		}
		dRx := curN.Rx - prevN.Rx
		dTx := curN.Tx - prevN.Tx
		if dRx < 0 || dTx < 0 {
			continue
		}
		dev.RxBps = dRx / secs
		dev.TxBps = dTx / secs
		dev.HasRates = true
	}
}

// retain drops rate state for endpoints no longer configured.
func (r *rateTracker) retain(urls map[string]struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for u := range r.prev {
		if _, ok := urls[u]; !ok {
			delete(r.prev, u)
		}
	}
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
