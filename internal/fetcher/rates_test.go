package fetcher

import (
	"testing"
	"time"

	"github.com/crazyuploader/hostglance/internal/model"
)

func snapshot(cpuTotal, cpuIdle, cpuIOWait, netRx, netTx float64) *model.SystemInfo {
	return &model.SystemInfo{
		Nets: []model.NetDev{{Name: "eth0"}},
		Counters: &model.SystemCounters{
			CPUTotal:  cpuTotal,
			CPUIdle:   cpuIdle,
			CPUIOWait: cpuIOWait,
			Net:       map[string]model.NetCounters{"eth0": {Rx: netRx, Tx: netTx}},
			PCPUWait:  10,
			PIOWait:   20,
			PMemWait:  1,
		},
	}
}

func TestRateTrackerComputesRates(t *testing.T) {
	var rt rateTracker

	first := snapshot(1000, 800, 50, 1_000_000, 500_000)
	rt.apply("http://n:9100/metrics", first)
	if first.HasCPURates {
		t.Fatal("first sample must not produce rates")
	}

	// Back-date the stored snapshot 10s so the delta interval is real.
	rt.prev["http://n:9100/metrics"].At = time.Now().Add(-10 * time.Second)

	second := snapshot(1040, 820, 54, 11_000_000, 5_500_000)
	second.Counters.PCPUWait = 11 // +1s waiting over ~10s => ~10%
	rt.apply("http://n:9100/metrics", second)

	if !second.HasCPURates {
		t.Fatal("second sample should produce CPU rates")
	}
	// dTotal=40, dIdle=20, dIOWait=4 => busy = 16/40 = 40%, iowait = 10%
	if second.CPUBusyPct < 39.9 || second.CPUBusyPct > 40.1 {
		t.Errorf("CPUBusyPct = %v, want ~40", second.CPUBusyPct)
	}
	if second.CPUIOWaitPct < 9.9 || second.CPUIOWaitPct > 10.1 {
		t.Errorf("CPUIOWaitPct = %v, want ~10", second.CPUIOWaitPct)
	}
	if !second.Nets[0].HasRates {
		t.Fatal("net rates missing")
	}
	// 10 MB over ~10s => ~1 MB/s (allow slack for elapsed time jitter)
	if second.Nets[0].RxBps < 900_000 || second.Nets[0].RxBps > 1_100_000 {
		t.Errorf("RxBps = %v, want ~1e6", second.Nets[0].RxBps)
	}
	if !second.HasPressureRates {
		t.Fatal("pressure rates missing")
	}
	if second.PressureCPUPct < 9 || second.PressureCPUPct > 11 {
		t.Errorf("PressureCPUPct = %v, want ~10", second.PressureCPUPct)
	}
}

func TestRateTrackerCounterReset(t *testing.T) {
	var rt rateTracker
	rt.apply("u", snapshot(1000, 800, 50, 1_000_000, 500_000))
	rt.prev["u"].At = time.Now().Add(-10 * time.Second)

	// Counters went backwards (reboot).
	reset := snapshot(100, 80, 5, 1000, 500)
	reset.Counters.PCPUWait = 0
	reset.Counters.PIOWait = 0
	reset.Counters.PMemWait = 0
	rt.apply("u", reset)

	if reset.HasCPURates {
		t.Error("CPU rates must be skipped after counter reset")
	}
	if reset.Nets[0].HasRates {
		t.Error("net rates must be skipped after counter reset")
	}
}

func TestRateTrackerSubSecondInterval(t *testing.T) {
	var rt rateTracker
	rt.apply("u", snapshot(1000, 800, 50, 1, 1))
	second := snapshot(1001, 800, 50, 2, 2)
	rt.apply("u", second) // immediate re-apply: dt < 1s
	if second.HasCPURates {
		t.Error("sub-second interval must not produce rates")
	}
}

func TestRateTrackerRetain(t *testing.T) {
	var rt rateTracker
	rt.apply("keep", snapshot(1, 0, 0, 0, 0))
	rt.apply("drop", snapshot(1, 0, 0, 0, 0))
	rt.retain(map[string]struct{}{"keep": {}})
	if _, ok := rt.prev["keep"]; !ok {
		t.Error("kept URL was dropped")
	}
	if _, ok := rt.prev["drop"]; ok {
		t.Error("removed URL was retained")
	}
}
