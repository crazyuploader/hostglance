package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crazyuploader/hostglance/internal/config"
	"github.com/crazyuploader/hostglance/internal/model"
	"github.com/crazyuploader/hostglance/templates"
	"github.com/gofiber/fiber/v3"
)

func testNodes() []model.NodeData {
	return []model.NodeData{{
		Label: "n1",
		Error: "boom",
	}}
}

func TestParseHistoryQueryParams(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantErr bool
		from    int64
		to      int64
		bucket  int64
	}{
		{"empty", "", false, 0, 0, 0},
		{"valid range", "from=100&to=200&bucket=60", false, 100, 200, 60},
		{"invalid from", "from=abc", true, 0, 0, 0},
		{"invalid to", "to=abc", true, 0, 0, 0},
		{"invalid bucket", "bucket=abc", true, 0, 0, 0},
		{"negative from", "from=-1", true, 0, 0, 0},
		{"negative bucket", "bucket=-60", true, 0, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := fiber.New()
			var from, to, bucket int64
			var err error
			app.Get("/t", func(c fiber.Ctx) error {
				from, to, bucket, err = parseHistoryQueryParams(c)
				return nil
			})
			req := httptest.NewRequest("GET", "/t?"+tt.query, http.NoBody)
			if _, terr := app.Test(req); terr != nil {
				t.Fatalf("app.Test: %v", terr)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if err == nil {
				if from != tt.from || to != tt.to || bucket != tt.bucket {
					t.Errorf("got (%d,%d,%d), want (%d,%d,%d)", from, to, bucket, tt.from, tt.to, tt.bucket)
				}
			}
		})
	}
}

func TestNodeViewsStripURL(t *testing.T) {
	views := nodeViews(testNodes())
	if len(views) != 1 {
		t.Fatalf("expected 1 view, got %d", len(views))
	}
	v := views[0]
	if v.Label != "n1" || v.Error != "boom" {
		t.Errorf("unexpected view: %+v", v)
	}

	// The serialized form must not contain a url field or the endpoint value.
	b, err := json.Marshal(views)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if strings.Contains(s, `"url"`) {
		t.Errorf("serialized view contains a url field: %s", s)
	}
}

func TestNodeHealthResponse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		node       model.NodeData
		maxUsage   float64
		wantStatus int
		wantState  string
		wantReason string
	}{
		{
			name:       "no optional exporters detected",
			node:       model.NodeData{Label: "host", Exporters: automaticExporters()},
			wantStatus: http.StatusOK,
			wantState:  "unknown",
			wantReason: "no_exporters_detected",
		},
		{
			name: "system only host is up",
			node: model.NodeData{
				Label: "host",
				Exporters: model.ExporterStatuses{
					Node: model.ExporterStatus{Mode: "auto", Available: true},
				},
			},
			wantStatus: http.StatusOK,
			wantState:  "up",
		},
		{
			name: "required ZFS has no pools",
			node: model.NodeData{
				Label: "host",
				Exporters: model.ExporterStatuses{
					ZFS: model.ExporterStatus{Mode: "enabled", Available: true},
				},
			},
			wantStatus: http.StatusServiceUnavailable,
			wantState:  "no_pools",
		},
		{
			name: "automatic ZFS reports degraded pool",
			node: model.NodeData{
				Label: "host",
				Exporters: model.ExporterStatuses{
					ZFS: model.ExporterStatus{Mode: "auto", Available: true},
				},
				Pools: []model.Pool{{Name: "tank", Health: model.HealthDegraded}},
			},
			wantStatus: http.StatusServiceUnavailable,
			wantState:  "degraded",
			wantReason: "unhealthy_pools",
		},
		{
			name: "pool exceeds usage threshold",
			node: model.NodeData{
				Label: "host",
				Exporters: model.ExporterStatuses{
					ZFS: model.ExporterStatus{Mode: "auto", Available: true},
				},
				Pools: []model.Pool{{
					Name: "tank", Health: model.HealthOnline, UsedPercent: 91,
				}},
			},
			maxUsage:   90,
			wantStatus: http.StatusServiceUnavailable,
			wantState:  "degraded",
			wantReason: "pool_over_threshold",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			status, body := callNodeHealth(t, tt.node, tt.maxUsage)
			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			if got := body["status"]; got != tt.wantState {
				t.Errorf("state = %v, want %q", got, tt.wantState)
			}
			if got := body["reason"]; got != tt.wantReason && tt.wantReason != "" {
				t.Errorf("reason = %v, want %q", got, tt.wantReason)
			}
		})
	}
}

func automaticExporters() model.ExporterStatuses {
	return model.ExporterStatuses{
		Node:     model.ExporterStatus{Mode: "auto"},
		ZFS:      model.ExporterStatus{Mode: "auto"},
		Smartctl: model.ExporterStatus{Mode: "auto"},
	}
}

func callNodeHealth(
	t *testing.T,
	node model.NodeData,
	maxUsage float64,
) (int, map[string]any) {
	t.Helper()
	app := fiber.New()
	app.Get("/", func(c fiber.Ctx) error {
		return nodeHealthResponse(
			c,
			&node,
			node.Label,
			&config.Config{MaxUsagePercent: maxUsage},
		)
	})
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close response: %v", err)
		}
	}()
	body := map[string]any{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.StatusCode, body
}

func TestTemplatesRenderHostDiscoveryStates(t *testing.T) {
	t.Parallel()
	pages, err := templates.Pages(funcMap())
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	now := time.Now()
	nodes := []model.NodeData{
		{
			Label:     "system-host",
			FetchedAt: now,
			Exporters: model.ExporterStatuses{
				Node: model.ExporterStatus{Mode: "auto", Available: true},
				ZFS:  model.ExporterStatus{Mode: "auto", Available: true},
			},
			System: &model.SystemInfo{Cores: 4, MemTotal: 1024, MemAvailable: 512},
			Pools:  []model.Pool{{Name: "tank", Health: model.HealthOnline}},
		},
		{
			Label:     "required-host",
			FetchedAt: now,
			Exporters: model.ExporterStatuses{
				Node: model.ExporterStatus{
					Mode: "enabled", Error: "unreachable",
				},
				Smartctl: model.ExporterStatus{
					Mode: "enabled", Error: "unreachable",
				},
			},
		},
	}
	cfg := &config.Config{Refresh: 5 * time.Minute}

	tests := []struct {
		name string
		data any
	}{
		{
			name: "dashboard",
			data: func() templateData {
				data := buildTemplateData(nodes)
				data.pageData = newPageData("storage", cfg, true, nodes)
				return data
			}(),
		},
		{
			name: "system",
			data: func() systemPageData {
				data := buildSystemPageData(hostViews(nodes))
				data.pageData = newPageData("system", cfg, true, nodes)
				return data
			}(),
		},
		{
			name: "history",
			data: historyData{
				pageData:       newPageData("history", cfg, true, nodes),
				RetentionHours: 720,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			if err := pages[tt.name].ExecuteTemplate(&output, "base", tt.data); err != nil {
				t.Fatalf("render template: %v", err)
			}
			if !strings.Contains(output.String(), "HostGlance") {
				t.Error("rendered page is missing product title")
			}
		})
	}
}

func TestHealthResponsePreChecks(t *testing.T) {
	t.Parallel()
	failed := model.ExporterStatus{Mode: "enabled", Error: "unreachable"}
	tank := []model.Pool{{Name: "tank", Health: model.HealthOnline}}
	tests := []struct {
		name       string
		node       model.NodeData
		pool       string
		wantStatus int
		wantReason string
	}{
		{
			name:       "discovery pending is not healthy",
			node:       model.NodeData{Label: "host", Exporters: automaticExporters()},
			wantStatus: http.StatusServiceUnavailable,
			wantReason: "discovery_pending",
		},
		{
			name: "pool check reports failed ZFS exporter",
			node: model.NodeData{
				Label: "host", FetchedAt: time.Now(),
				Exporters: model.ExporterStatuses{ZFS: failed},
			},
			pool:       "tank",
			wantStatus: http.StatusServiceUnavailable,
			wantReason: "exporter_unavailable",
		},
		{
			name: "pool check ignores failed SMART exporter",
			node: model.NodeData{
				Label: "host", FetchedAt: time.Now(), Pools: tank,
				Exporters: model.ExporterStatuses{
					ZFS:      model.ExporterStatus{Mode: "auto", Available: true},
					Smartctl: failed,
				},
			},
			pool:       "tank",
			wantStatus: http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			app := fiber.New()
			app.Get("/", func(c fiber.Ctx) error {
				return healthResponse(c, &tt.node, tt.node.Label, tt.pool, &config.Config{})
			})
			resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/", http.NoBody))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			body := map[string]any{}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if tt.wantReason != "" && body["reason"] != tt.wantReason {
				t.Errorf("reason = %v, want %q", body["reason"], tt.wantReason)
			}
		})
	}
}

func TestRestartOnlyChanges(t *testing.T) {
	t.Parallel()
	old := &config.Config{Addr: ":8054", CacheTTL: time.Minute, TrustedProxies: []string{"10.0.0.1"}}
	same := *old
	same.Refresh = time.Hour // hot-reloadable
	if got := restartOnlyChanges(old, &same); len(got) != 0 {
		t.Errorf("hot-reloadable change flagged: %v", got)
	}
	cur := &config.Config{Addr: ":9000", History: config.HistoryConfig{Enabled: true}}
	got := strings.Join(restartOnlyChanges(old, cur), ",")
	if got != "addr,cache_ttl,history,trusted_proxies" {
		t.Errorf("changes = %q", got)
	}
}

func TestHostViewHidesZFSMountsWhenPoolsShown(t *testing.T) {
	t.Parallel()
	sys := &model.SystemInfo{Filesystems: []model.FSInfo{
		{Mountpoint: "/", FSType: "ext4"},
		{Mountpoint: "/tank", FSType: "zfs"},
	}}
	node := model.NodeData{System: sys, Exporters: model.ExporterStatuses{
		ZFS: model.ExporterStatus{Mode: "auto", Available: true},
	}}
	if got := hostView(node).System.Filesystems; len(got) != 1 || got[0].FSType != "ext4" {
		t.Errorf("filesystems = %+v, want only ext4", got)
	}
	if len(sys.Filesystems) != 2 {
		t.Error("hostView mutated the shared cache")
	}
	node.Exporters.ZFS.Available = false
	if got := hostView(node).System.Filesystems; len(got) != 2 {
		t.Errorf("ZFS mounts hidden without a ZFS exporter: %+v", got)
	}
}

func TestHostTitleAndPlural(t *testing.T) {
	t.Parallel()
	sys := &model.SystemInfo{Hostname: "PVE01"}
	for _, tt := range []struct {
		label string
		sys   *model.SystemInfo
		want  string
	}{
		{"100.64.0.27", sys, "PVE01"},
		{"2001:db8::1", sys, "PVE01"},
		{"nas", sys, "nas"},
		{"100.64.0.27", nil, "100.64.0.27"},
	} {
		if got := hostTitle(tt.label, tt.sys); got != tt.want {
			t.Errorf("hostTitle(%q) = %q, want %q", tt.label, got, tt.want)
		}
	}
	if plural(1, "pool") != "1 pool" || plural(0, "disk") != "0 disks" {
		t.Error("plural formatting wrong")
	}
}
