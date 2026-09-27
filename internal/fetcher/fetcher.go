// Package fetcher discovers and collects Prometheus metrics from configured hosts.
package fetcher

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/crazyuploader/hostglance/internal/config"
	"github.com/crazyuploader/hostglance/internal/model"
	"github.com/crazyuploader/hostglance/internal/parser"
)

const (
	fetchTimeout     = 10 * time.Second
	dialTimeout      = 3 * time.Second
	maxResponseBytes = 10 << 20
	// ponytail: keep one fixed cap; make it configurable only if large fleets need tuning.
	maxHostWorkers = 8
)

// Fetcher caches host snapshots. Each refresh retries automatic discovery;
// exporters marked disabled never produce network requests.
type Fetcher struct {
	client    *http.Client
	mu        sync.RWMutex
	hosts     []config.Host
	gen       uint64
	cacheTTL  time.Duration
	cache     []model.NodeData
	expiresAt time.Time
	rates     rateTracker
	refresh   chan struct{} // serializes scrapes without blocking cancellation
}

// New creates a Fetcher for validated hosts returned by config.Load.
func New(hosts []config.Host, cacheTTL time.Duration) *Fetcher {
	return &Fetcher{
		client: &http.Client{
			Timeout: fetchTimeout,
			Transport: &http.Transport{
				// Fail fast on filtered ports so auto discovery does not stall pages.
				// ponytail: fixed 3s connect timeout; make configurable if slow links need more.
				DialContext:         (&net.Dialer{Timeout: dialTimeout}).DialContext,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		hosts:    slices.Clone(hosts),
		cache:    pendingHosts(hosts),
		cacheTTL: cacheTTL,
		refresh:  make(chan struct{}, 1),
	}
}

// pendingHosts preserves host identity while the first scrape is in progress.
func pendingHosts(hosts []config.Host) []model.NodeData {
	out := make([]model.NodeData, len(hosts))
	for i, host := range hosts {
		out[i] = model.NodeData{
			Label:    host.Label,
			Location: host.Location,
			Parent:   host.Parent,
			Exporters: model.ExporterStatuses{
				Node:     model.ExporterStatus{Mode: string(host.Exporters.Node.Mode)},
				ZFS:      model.ExporterStatus{Mode: string(host.Exporters.ZFS.Mode)},
				Smartctl: model.ExporterStatus{Mode: string(host.Exporters.Smartctl.Mode)},
				PVE:      model.ExporterStatus{Mode: string(host.Exporters.PVE.Mode)},
			},
		}
	}
	return out
}

// SetHosts invalidates discovery on configuration reload. In-flight results
// from the previous configuration cannot replace the new snapshot or rates.
func (f *Fetcher) SetHosts(hosts []config.Host) {
	urls := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		if host.Exporters.Node.Mode != config.ModeDisabled {
			urls[host.Exporters.Node.URL] = struct{}{}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hosts = slices.Clone(hosts)
	f.gen++
	f.cache = pendingHosts(hosts)
	f.expiresAt = time.Time{}
	f.rates.retain(urls)
}

// CacheInfo returns the current cache status.
func (f *Fetcher) CacheInfo() (expiresAt time.Time, ttl time.Duration) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.expiresAt, f.cacheTTL
}

// FetchAll returns results in configuration order and whether the cache was used.
// Inner slices and pointers are shared with the cache and must remain read-only.
func (f *Fetcher) FetchAll(ctx context.Context) ([]model.NodeData, bool) {
	f.mu.RLock()
	if time.Now().Before(f.expiresAt) {
		data := append([]model.NodeData{}, f.cache...)
		f.mu.RUnlock()
		return data, true
	}
	f.mu.RUnlock()
	return f.collect(ctx, false)
}

// Refresh bypasses the cache TTL for the background poller.
func (f *Fetcher) Refresh(ctx context.Context) []model.NodeData {
	nodes, _ := f.collect(ctx, true)
	return nodes
}

// Snapshot returns the last completed scrape without triggering discovery.
// It has the same read-only contract as FetchAll.
func (f *Fetcher) Snapshot() []model.NodeData {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return append([]model.NodeData{}, f.cache...)
}

// collect scrapes all hosts, serialized, unless a fresh cache is available.
func (f *Fetcher) collect(ctx context.Context, force bool) ([]model.NodeData, bool) {
	select {
	case f.refresh <- struct{}{}:
		defer func() { <-f.refresh }()
	case <-ctx.Done():
		return f.Snapshot(), true
	}

	for {
		if ctx.Err() != nil {
			return f.Snapshot(), true
		}
		f.mu.RLock()
		if !force && time.Now().Before(f.expiresAt) {
			data := append([]model.NodeData{}, f.cache...)
			f.mu.RUnlock()
			return data, true
		}
		hosts := slices.Clone(f.hosts)
		gen := f.gen
		f.mu.RUnlock()

		results := f.fetchHosts(ctx, hosts)
		f.mu.Lock()
		if gen != f.gen {
			f.mu.Unlock()
			continue // configuration changed during I/O; fetch the new targets
		}
		if ctx.Err() != nil {
			data := append([]model.NodeData{}, f.cache...)
			f.mu.Unlock()
			return data, true
		}
		for i := range results {
			f.rates.apply(hosts[i].Exporters.Node.URL, results[i].System)
		}
		f.cache = results
		f.expiresAt = time.Now().Add(f.cacheTTL)
		f.mu.Unlock()
		return append([]model.NodeData{}, results...), false
	}
}

// fetchHosts scrapes hosts with a bounded worker pool, preserving order.
func (f *Fetcher) fetchHosts(ctx context.Context, hosts []config.Host) []model.NodeData {
	results := make([]model.NodeData, len(hosts))
	workers := min(len(hosts), maxHostWorkers)
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Go(func() {
			for i := worker; i < len(hosts); i += workers {
				results[i] = f.fetchOne(ctx, hosts[i])
			}
		})
	}
	wg.Wait()
	return results
}

// fetchRaw leaves logging to the caller, which knows whether failure is expected.
func (f *Fetcher) fetchRaw(ctx context.Context, rawURL string) ([]parser.Sample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request metrics: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Debug("close metrics response", "error", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read metrics: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("metrics exceed %d byte limit", maxResponseBytes)
	}
	samples, err := parser.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse metrics: %w", err)
	}
	return samples, nil
}

// exporterResult is one exporter scrape outcome.
type exporterResult struct {
	status  model.ExporterStatus
	samples []parser.Sample
}

// fetchExporter scrapes one exporter unless disabled; only enabled failures set Error.
func (f *Fetcher) fetchExporter(
	ctx context.Context,
	label string,
	exp config.Exporter,
	kind string,
) exporterResult {
	mode := exp.Mode
	result := exporterResult{status: model.ExporterStatus{Mode: string(mode)}}
	if mode == config.ModeDisabled || exp.URL == "" { // nothing to scrape
		return result
	}
	samples, err := f.fetchRaw(ctx, exp.URL)
	if err == nil && recognizesExporter(samples, kind) {
		result.status.Available = true
		result.samples = samples
		return result
	}
	message := "unreachable"
	if err == nil {
		message = "returned no " + kind + " metrics"
	}
	level := slog.LevelDebug
	if mode == config.ModeEnabled {
		level = slog.LevelWarn
		result.status.Error = message
	}
	slog.Log(
		ctx,
		level,
		"exporter scrape failed",
		"label",
		label,
		"exporter",
		kind,
		"reason",
		message,
		"error",
		err,
	)
	return result
}

// recognizesExporter reports whether any sample has the exporter's metric prefix.
func recognizesExporter(samples []parser.Sample, kind string) bool {
	prefix := kind + "_"
	for _, sample := range samples {
		if strings.HasPrefix(sample.Name, prefix) {
			return true
		}
	}
	return false
}

// fetchOne scrapes all exporters on one host concurrently.
func (f *Fetcher) fetchOne(ctx context.Context, host config.Host) model.NodeData {
	var node, zfs, smartctl, pve exporterResult
	var wg sync.WaitGroup
	wg.Go(func() { node = f.fetchExporter(ctx, host.Label, host.Exporters.Node, "node") })
	wg.Go(func() { zfs = f.fetchExporter(ctx, host.Label, host.Exporters.ZFS, "zfs") })
	wg.Go(func() {
		smartctl = f.fetchExporter(ctx, host.Label, host.Exporters.Smartctl, "smartctl")
	})
	wg.Go(func() { pve = f.fetchExporter(ctx, host.Label, host.Exporters.PVE, "pve") })
	wg.Wait()

	// Old endpoints may proxy both ZFS and SMART in one response. New hosts
	// use independent sources, so disabled SMART cannot be collected indirectly.
	if host.LegacyCombinedMetrics && recognizesExporter(zfs.samples, "smartctl") {
		combined := make([]parser.Sample, 0, len(zfs.samples)+len(smartctl.samples))
		combined = append(combined, zfs.samples...)
		combined = append(combined, smartctl.samples...)
		smartctl.samples = combined
		smartctl.status = model.ExporterStatus{Mode: string(config.ModeAuto), Available: true}
	}

	nd := model.NodeData{
		Label:     host.Label,
		Location:  host.Location,
		Parent:    host.Parent,
		FetchedAt: time.Now(),
		Exporters: model.ExporterStatuses{
			Node: node.status, ZFS: zfs.status, Smartctl: smartctl.status, PVE: pve.status,
		},
		Pools: model.ExtractPools(zfs.samples),
	}
	if node.status.Available {
		nd.System = model.ExtractSystem(node.samples)
	}
	if zfs.status.Available {
		nd.ExporterInfo = model.ExtractExporterInfo(zfs.samples)
	}
	nd.Disks = model.ExtractDisks(smartctl.samples)
	nd.SmartctlInfo = model.ExtractSmartctlInfo(smartctl.samples)
	nd.Guests = model.ExtractGuests(pve.samples)
	if pve.status.Available {
		nd.PVE = model.ExtractPVEInfo(pve.samples)
	}

	var problems []string
	for _, source := range []struct {
		name   string
		status model.ExporterStatus
	}{
		{name: "node", status: node.status},
		{name: "zfs", status: zfs.status},
		{name: "smartctl", status: smartctl.status},
		{name: "pve", status: pve.status},
	} {
		if source.status.Error != "" {
			problems = append(problems, source.name+": "+source.status.Error)
		}
	}
	nd.Error = strings.Join(problems, "; ")
	return nd
}
