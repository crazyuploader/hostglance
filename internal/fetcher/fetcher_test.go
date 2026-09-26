package fetcher

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crazyuploader/hostglance/internal/config"
	"github.com/crazyuploader/hostglance/internal/model"
)

func TestFetcher_Cache(t *testing.T) {
	var callCount int64
	f := New(
		[]config.Host{zfsHost("http://zfs.test/metrics", "test-node")},
		1*time.Second,
	)
	f.client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		atomic.AddInt64(&callCount, 1)
		return metricsResponse("zfs_pool_health{pool=\"tank\"} 0\n", http.StatusOK), nil
	})
	ctx := context.Background()

	// First call - should hit the server
	_, isCached := f.FetchAll(ctx)
	if isCached {
		t.Fatal("expected first call to be a cache miss")
	}
	if atomic.LoadInt64(&callCount) != 1 {
		t.Fatalf("expected 1 call, got %d", atomic.LoadInt64(&callCount))
	}

	// Second call - should be cached
	_, isCached = f.FetchAll(ctx)
	if !isCached {
		t.Fatal("expected second call to be a cache hit")
	}
	if atomic.LoadInt64(&callCount) != 1 {
		t.Fatalf("expected cached result, but server was hit again (call count: %d)", atomic.LoadInt64(&callCount))
	}

	// Wait for cache to expire
	time.Sleep(1100 * time.Millisecond)

	// Third call - should hit the server again
	_, isCached = f.FetchAll(ctx)
	if isCached {
		t.Fatal("expected call after expiry to be a cache miss")
	}
	if atomic.LoadInt64(&callCount) != 2 {
		t.Fatalf("expected 2 calls after expiry, got %d", atomic.LoadInt64(&callCount))
	}
}

func TestFetcher_SetHosts(t *testing.T) {
	var callCount int64
	f := New(
		[]config.Host{zfsHost("http://first.test/metrics", "node1")},
		1*time.Minute,
	)
	f.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt64(&callCount, 1)
		pool := "tank1"
		if req.URL.Hostname() == "second.test" {
			pool = "tank2"
		}
		return metricsResponse(
			fmt.Sprintf("zfs_pool_health{pool=%q} 0\n", pool),
			http.StatusOK,
		), nil
	})
	ctx := context.Background()

	// Initial fetch
	nodes, isCached := f.FetchAll(ctx)
	if isCached {
		t.Fatal("expected first fetch to be a cache miss")
	}
	if len(nodes) != 1 || nodes[0].Label != "node1" {
		t.Fatalf("expected node1, got %+v", nodes)
	}

	// Update endpoints
	f.SetHosts([]config.Host{zfsHost("http://second.test/metrics", "node2")})

	// Fetch again - should hit server2 and ignore old cache
	nodes, isCached = f.FetchAll(ctx)
	if isCached {
		t.Fatal("expected fetch after SetHosts to be a cache miss")
	}
	if len(nodes) != 1 || nodes[0].Label != "node2" {
		t.Fatalf("expected node2 after hot-reload, got %+v", nodes)
	}
}

func zfsHost(rawURL, label string) config.Host {
	return config.Host{
		Label: label,
		Exporters: config.Exporters{
			ZFS:      config.Exporter{Mode: config.ModeEnabled, URL: rawURL},
			Node:     config.Exporter{Mode: config.ModeDisabled},
			Smartctl: config.Exporter{Mode: config.ModeDisabled},
		},
	}
}

// roundTripFunc keeps discovery tests deterministic and independent of open ports.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func metricsResponse(body string, status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func autoHost() config.Host {
	return config.Host{
		Label: "test-host",
		Exporters: config.Exporters{
			Node:     config.Exporter{Mode: config.ModeAuto, URL: "http://node.test/metrics"},
			ZFS:      config.Exporter{Mode: config.ModeAuto, URL: "http://zfs.test/metrics"},
			Smartctl: config.Exporter{Mode: config.ModeAuto, URL: "http://smartctl.test/metrics"},
		},
	}
}

func TestFetcher_ExporterModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		mode         config.ExporterMode
		body         string
		status       int
		available    bool
		wantError    bool
		wantRequests int64
	}{
		{name: "auto present", mode: config.ModeAuto, body: "node_memory_MemTotal_bytes 1024\n", status: 200, available: true, wantRequests: 1},
		{name: "auto absent", mode: config.ModeAuto, status: 503, wantRequests: 1},
		{name: "auto unrelated service", mode: config.ModeAuto, body: "go_goroutines 5\n", status: 200, wantRequests: 1},
		{name: "auto HTML", mode: config.ModeAuto, body: "<html>hello</html>", status: 200, wantRequests: 1},
		{name: "enabled present", mode: config.ModeEnabled, body: "node_memory_MemTotal_bytes 1024\n", status: 200, available: true, wantRequests: 1},
		{name: "enabled absent", mode: config.ModeEnabled, status: 503, wantError: true, wantRequests: 1},
		{name: "enabled wrong metrics", mode: config.ModeEnabled, body: "zfs_pool_health{pool=\"tank\"} 0\n", status: 200, wantError: true, wantRequests: 1},
		{name: "disabled", mode: config.ModeDisabled, body: "node_memory_MemTotal_bytes 1024\n", status: 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			host := autoHost()
			host.Exporters.Node.Mode = tt.mode
			host.Exporters.ZFS.Mode = config.ModeDisabled
			host.Exporters.Smartctl.Mode = config.ModeDisabled
			f := New([]config.Host{host}, time.Minute)
			var requests atomic.Int64
			f.client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				requests.Add(1)
				return metricsResponse(tt.body, tt.status), nil
			})
			nodes := f.Refresh(t.Context())
			node := nodes[0]
			if node.Exporters.Node.Available != tt.available || (node.Error != "") != tt.wantError {
				t.Errorf("node status = %+v, error = %q", node.Exporters.Node, node.Error)
			}
			if (node.System != nil) != tt.available {
				t.Errorf("unexpected system availability: %+v", node.System)
			}
			if got := requests.Load(); got != tt.wantRequests {
				t.Errorf("made %d requests, want %d", got, tt.wantRequests)
			}
		})
	}
}

func TestFetcher_IndependentExporters(t *testing.T) {
	t.Parallel()
	host := autoHost()
	host.Exporters.ZFS.Mode = config.ModeEnabled
	f := New([]config.Host{host}, time.Minute)
	f.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Hostname() {
		case "node.test":
			return metricsResponse("node_uname_info{nodename=\"test-host\"} 1\n", 200), nil
		case "smartctl.test":
			return metricsResponse(
				"smartctl_device_temperature{device=\"/dev/sda\",temperature_type=\"current\"} 35\n",
				200,
			), nil
		default:
			return nil, fmt.Errorf("cannot reach http://private-host:9134/metrics?token=private")
		}
	})
	node := f.Refresh(t.Context())[0]
	if node.System == nil || len(node.Disks) != 1 || node.Disks[0].Temperature != 35 {
		t.Fatalf("ZFS failure hid working exporters: %+v", node)
	}
	if node.Exporters.ZFS.Error == "" || node.Exporters.Node.Error != "" || node.Exporters.Smartctl.Error != "" {
		t.Fatalf("errors were not isolated: %+v", node.Exporters)
	}
	if strings.Contains(node.Error, "private") || strings.Contains(node.Exporters.ZFS.Error, "http") {
		t.Fatal("public exporter errors leaked scrape details")
	}
}

func TestFetcher_Rediscovery(t *testing.T) {
	t.Parallel()
	host := autoHost()
	f := New([]config.Host{host}, time.Minute)
	var available atomic.Bool
	f.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() == "node.test" && available.Load() {
			return metricsResponse("node_memory_MemTotal_bytes 4096\n", 200), nil
		}
		return metricsResponse("", 404), nil
	})
	missing := f.Refresh(t.Context())[0]
	if missing.Error != "" || missing.Exporters.AnyAvailable() {
		t.Fatalf("optional absence became an error: %+v", missing)
	}
	available.Store(true)
	found := f.Refresh(t.Context())[0]
	if found.System == nil || !found.Exporters.Node.Available {
		t.Fatalf("new exporter was not discovered: %+v", found)
	}
	available.Store(false)
	lost := f.Refresh(t.Context())[0]
	if lost.System != nil || lost.Error != "" || lost.Exporters.Node.Available {
		t.Fatalf("lost auto exporter retained stale data or became an error: %+v", lost)
	}
}

func TestFetcher_DisabledBundledMetrics(t *testing.T) {
	t.Parallel()
	for _, legacy := range []bool{false, true} {
		name := "host"
		if legacy {
			name = "legacy endpoint"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			host := zfsHost("http://zfs.test/metrics", "nas")
			host.LegacyCombinedMetrics = legacy
			f := New([]config.Host{host}, time.Minute)
			f.client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return metricsResponse(
					"zfs_pool_health{pool=\"tank\"} 0\nsmartctl_device_temperature{device=\"/dev/sda\",temperature_type=\"current\"} 35\n",
					200,
				), nil
			})
			node := f.Refresh(t.Context())[0]
			if (len(node.Disks) > 0) != legacy {
				t.Fatalf("bundled disks=%d legacy=%v", len(node.Disks), legacy)
			}
			if node.Exporters.Smartctl.Available != legacy {
				t.Fatalf("wrong bundled SMART status: %+v", node.Exporters.Smartctl)
			}
		})
	}
}

func TestFetcher_ReloadDuringScrape(t *testing.T) {
	t.Parallel()
	first := zfsHost("http://first.test/metrics", "first")
	second := zfsHost("http://second.test/metrics", "second")
	f := New([]config.Host{first}, time.Minute)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	f.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() == "first.test" {
			close(started)
			select {
			case <-release:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
		return metricsResponse("zfs_pool_health{pool=\"tank\"} 0\n", 200), nil
	})
	done := make(chan []model.NodeData, 1)
	go func() { done <- f.Refresh(t.Context()) }()
	<-started
	f.SetHosts([]config.Host{second})
	once.Do(func() { close(release) })
	got := <-done
	if len(got) != 1 || got[0].Label != "second" {
		t.Fatalf("returned obsolete configuration: %+v", got)
	}
	cached, hit := f.FetchAll(t.Context())
	if !hit || cached[0].Label != "second" {
		t.Fatalf("stale scrape replaced current cache: %+v", cached)
	}
}

func TestFetcher_CancelledScrape(t *testing.T) {
	t.Parallel()
	f := New([]config.Host{zfsHost("http://zfs.test/metrics", "nas")}, time.Minute)
	started := make(chan struct{})
	f.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan []model.NodeData, 1)
	go func() { done <- f.Refresh(ctx) }()
	<-started
	cancel()
	got := <-done
	if len(got) != 1 || got[0].Label != "nas" || !got[0].FetchedAt.IsZero() {
		t.Fatalf("cancelled collection returned partial results: %+v", got)
	}
	pending := f.Snapshot()
	if len(pending) != 1 || pending[0].Label != "nas" || !pending[0].FetchedAt.IsZero() || pending[0].Error != "" {
		t.Fatalf("cancelled collection poisoned pending snapshot: %+v", pending)
	}
}
