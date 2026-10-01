// Package server boots the Fiber v3 web server and serves the system and storage dashboard.
package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/crazyuploader/hostglance/internal/config"
	"github.com/crazyuploader/hostglance/internal/fetcher"
	"github.com/crazyuploader/hostglance/internal/history"
	"github.com/crazyuploader/hostglance/internal/model"
	"github.com/crazyuploader/hostglance/templates"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/limiter"
	"github.com/gofiber/fiber/v3/middleware/logger"
)

// maxSSEClients caps concurrent /events connections to avoid fd exhaustion.
const maxSSEClients = 64

// Hub broadcasts reload signals to connected SSE clients.
// clients is a sync.Map keyed by chan bool.
type Hub struct {
	clients sync.Map
	count   atomic.Int64
}

func newHub() *Hub {
	return &Hub{}
}

// add registers a client channel; returns false when the hub is full.
// The slot is reserved with a CAS loop so concurrent registrations cannot
// exceed maxSSEClients.
func (h *Hub) add(ch chan bool) bool {
	for {
		n := h.count.Load()
		if n >= maxSSEClients {
			return false
		}
		if h.count.CompareAndSwap(n, n+1) {
			break
		}
	}
	h.clients.Store(ch, true)
	return true
}

// remove unregisters a client channel; safe to call more than once.
func (h *Hub) remove(ch chan bool) {
	if _, loaded := h.clients.LoadAndDelete(ch); loaded {
		h.count.Add(-1)
	}
}

func (h *Hub) broadcast() {
	h.clients.Range(func(key, _ any) bool {
		ch := key.(chan bool)
		select {
		case ch <- true:
		default:
			// Channel already holds a pending refresh; drop the duplicate.
			// The stream writer unregisters the client when it disconnects.
		}
		return true
	})
}

const (
	httpReadTimeout    = 15 * time.Second
	httpIdleTimeout    = 60 * time.Second
	httpHandlerTimeout = 15 * time.Second
)

// templateData is the data passed to the dashboard page template.
type templateData struct {
	pageData
	Nodes         []nodeView
	NodesJSON     template.JS // URL-stripped JSON for inline script
	FetchedAt     string
	TotalPools    int
	StorageErrors int
	HealthyPools  int
	DegradedPools int
	ErroredPools  int
	TotalNodes    int
}

// historyData is the data passed to the history page template.
type historyData struct {
	pageData
	RetentionHours int
}

// Start registers routes and begins listening.
func Start(cfg *config.Config) error {
	setupLogger(cfg)

	var cfgPtr atomic.Pointer[config.Config]
	cfgPtr.Store(cfg)

	slog.Debug("starting server in debug mode", "hosts", len(cfg.Hosts))

	f := fetcher.New(cfg.Hosts, cfg.CacheTTL)
	hub := newHub()

	// Graceful shutdown context, cancelled on SIGTERM/SIGINT.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownSigs := make(chan os.Signal, 1)
	signal.Notify(shutdownSigs, syscall.SIGTERM, os.Interrupt)

	// Hot-reload config on SIGHUP
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP)
	go watchConfigReload(sigs, f, &cfgPtr)

	histStore := setupHistory(ctx, cfg, f)
	if histStore != nil {
		defer func() { _ = histStore.Close() }()
	}

	pages, err := templates.Pages(funcMap())
	if err != nil {
		return fmt.Errorf("template parse: %w", err)
	}

	app := newFiberApp(cfg)

	rl := limiter.New(limiter.Config{
		Max:        300,
		Expiration: 1 * time.Minute,
	})

	// Background poller: the only source of SSE refresh broadcasts.
	go runPoller(ctx, f, hub, &cfgPtr)

	registerSSERoute(app, hub)
	registerAPIRoutes(app, f, rl, &cfgPtr)
	registerDashboardRoute(app, f, pages["dashboard"], &cfgPtr, histStore)
	registerSystemRoute(app, f, pages["system"], &cfgPtr, histStore)
	if histStore != nil {
		registerHistoryRoutes(
			app, rl, histStore, pages["history"], &cfgPtr, f,
		)
	}

	app.Get("/health", func(c fiber.Ctx) error {
		return c.SendString("OK")
	})

	// Shutdown on SIGTERM/SIGINT
	go shutdownOnSignal(shutdownSigs, ctx, cancel, app)

	slog.Info("HostGlance started", "url", fmt.Sprintf("http://localhost%s", cfg.Addr))
	return app.Listen(cfg.Addr)
}

// watchConfigReload reloads config and hot-swaps hosts whenever sigs fires.
func watchConfigReload(sigs <-chan os.Signal, f *fetcher.Fetcher, cfgPtr *atomic.Pointer[config.Config]) {
	for range sigs {
		slog.Info("SIGHUP received, reloading config...")
		newCfg, err := config.Load()
		if err != nil {
			slog.Error("config reload failed", "error", err)
			continue
		}
		if changed := restartOnlyChanges(cfgPtr.Load(), newCfg); len(changed) > 0 {
			slog.Warn("restart required to apply changed settings", "settings", changed)
		}
		setupLogger(newCfg)
		f.SetHosts(newCfg.Hosts)
		cfgPtr.Store(newCfg)
		slog.Info("config reloaded successfully")
	}
}

// restartOnlyChanges names settings that are read once at startup.
func restartOnlyChanges(old, cur *config.Config) []string {
	var changed []string
	if old.Addr != cur.Addr {
		changed = append(changed, "addr")
	}
	if old.CacheTTL != cur.CacheTTL {
		changed = append(changed, "cache_ttl")
	}
	if old.History != cur.History {
		changed = append(changed, "history")
	}
	if !slices.Equal(old.TrustedProxies, cur.TrustedProxies) {
		changed = append(changed, "trusted_proxies")
	}
	return changed
}

// setupHistory opens the history store and starts its recorder when enabled.
// It disables cfg.History.Enabled in place if the store fails to open.
func setupHistory(ctx context.Context, cfg *config.Config, f *fetcher.Fetcher) *history.Store {
	if !cfg.History.Enabled {
		return nil
	}
	histStore, err := history.Open(cfg.History.Path, cfg.History.Retention)
	if err != nil {
		slog.Error("history store did not open, so history is off", "error", err, "path", cfg.History.Path)
		cfg.History.Enabled = false
		return nil
	}
	recInterval := cfg.History.RecordInterval
	if recInterval <= 0 {
		recInterval = cfg.Refresh
	}
	rec := history.NewRecorder(histStore, f, recInterval)
	go rec.Run(ctx)
	slog.Info("history enabled", "path", cfg.History.Path, "retention", cfg.History.Retention, "record_interval", recInterval)
	return histStore
}

func newFiberApp(cfg *config.Config) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName:      "HostGlance",
		ReadTimeout:  httpReadTimeout,
		WriteTimeout: 0, // Disable write timeout for SSE streams
		IdleTimeout:  httpIdleTimeout,
		TrustProxy:   len(cfg.TrustedProxies) > 0,
		TrustProxyConfig: fiber.TrustProxyConfig{
			Proxies: cfg.TrustedProxies,
		},
		ProxyHeader: fiber.HeaderXForwardedFor,
	})

	app.Use(logger.New(logger.Config{
		Format: "[${time}] ${status} - ${latency} ${ips} ${method} ${path}\n",
	}))

	app.Use(func(c fiber.Ctx) error {
		c.Set("X-Content-Type-Options", "nosniff")
		c.Set("X-Frame-Options", "SAMEORIGIN")
		c.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'")
		return c.Next()
	})

	return app
}

func registerSSERoute(app *fiber.App, hub *Hub) {
	// Long-lived connections get their own, stricter limiter; the real
	// resource to protect is concurrent connections, capped via hub.add.
	sseLimiter := limiter.New(limiter.Config{
		Max:        60,
		Expiration: 1 * time.Minute,
	})
	app.Get("/events", sseLimiter, func(c fiber.Ctx) error {
		notify := make(chan bool, 1)
		if !hub.add(notify) {
			slog.Warn("SSE client rejected: connection cap reached", "cap", maxSSEClients)
			return fiber.ErrServiceUnavailable
		}

		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("Transfer-Encoding", "chunked")

		clientIP := c.IP()
		c.Response().SetBodyStreamWriter(func(w *bufio.Writer) {
			// The handler has already returned by the time this runs, so the
			// client must be unregistered here, not via defer in the handler
			// (which would remove it before the stream even starts).
			defer hub.remove(notify)
			slog.Debug("SSE client connected", "ip", clientIP)

			// Send initial keep-alive
			_, _ = fmt.Fprintf(w, ":\n\n")
			_ = w.Flush()

			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-notify:
					_, _ = fmt.Fprintf(w, "data: refresh\n\n")
					if err := w.Flush(); err != nil {
						return
					}
				case <-c.Context().Done():
					slog.Debug("SSE client disconnected", "ip", clientIP)
					return
				case <-ticker.C:
					// keep-alive
					_, _ = fmt.Fprintf(w, ":\n\n")
					if err := w.Flush(); err != nil {
						return
					}
				}
			}
		})

		return nil
	})
}

func registerAPIRoutes(app *fiber.App, f *fetcher.Fetcher, rl fiber.Handler, cfgPtr *atomic.Pointer[config.Config]) {
	app.Get("/api/metrics", rl, func(c fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.Context(), httpHandlerTimeout)
		defer cancel()
		nodes, isCached := f.FetchAll(ctx)
		setCacheHeaders(c, f, isCached)
		return c.JSON(nodeViews(nodes))
	})

	app.Get("/api/system", rl, func(c fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.Context(), httpHandlerTimeout)
		defer cancel()
		nodes, isCached := f.FetchAll(ctx)
		setCacheHeaders(c, f, isCached)
		return c.JSON(systemViews(nodes))
	})

	app.Get("/api/health/:label", rl, func(c fiber.Ctx) error {
		curCfg := cfgPtr.Load()
		return serveHealthCheck(c, f, c.Params("label"), "", curCfg)
	})

	app.Get("/api/health/:label/:pool", rl, func(c fiber.Ctx) error {
		curCfg := cfgPtr.Load()
		return serveHealthCheck(c, f, c.Params("label"), c.Params("pool"), curCfg)
	})

	app.Get("/api/health/:label/disk/:disk", rl, func(c fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.Context(), httpHandlerTimeout)
		defer cancel()
		nodes, isCached := f.FetchAll(ctx)
		setCacheHeaders(c, f, isCached)
		node, err := findNodeByLabel(nodes, c.Params("label"))
		if err != nil {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"status": "not_found", "label": c.Params("label")})
		}
		return diskHealthResponse(c, node, c.Params("disk"))
	})
}

// diskHealthResponse checks one disk by serial number or by-id device name.
// A disk that smartctl cannot reach (exit status bits 0-2) counts as missing.
// smartctl_exporter keeps the last good data of a removed disk until it
// restarts, so presence comes from node_disk_info. Without those serials the
// check fails: SMART data alone cannot show that the disk is still there.
func diskHealthResponse(c fiber.Ctx, node *model.NodeData, id string) error {
	if node.FetchedAt.IsZero() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"status": "unknown", "reason": "discovery_pending", "label": node.Label,
		})
	}
	res := fiber.Map{"status": "down", "label": node.Label, "location": node.Location, "disk": id}
	if node.Exporters.Smartctl.Error != "" {
		res["reason"] = "exporter_unavailable"
		return c.Status(fiber.StatusServiceUnavailable).JSON(res)
	}
	i := slices.IndexFunc(node.Disks, func(d model.DiskInfo) bool {
		return d.SerialNumber == id || d.Device == id
	})
	if i < 0 {
		res["reason"] = "disk_not_found"
		return c.Status(fiber.StatusServiceUnavailable).JSON(res)
	}
	d := node.Disks[i]
	res["device"], res["serial_number"], res["model_name"] = d.Device, d.SerialNumber, d.ModelName
	switch {
	case node.System == nil || len(node.System.DiskSerials) == 0:
		res["status"], res["reason"] = "unknown", "presence_unknown"
	case !slices.Contains(node.System.DiskSerials, d.SerialNumber):
		res["reason"] = "disk_not_found"
	case d.HasExitStatus && int(d.ExitStatus)&0b111 != 0:
		res["reason"] = "disk_unreachable"
	case !d.SmartPassed:
		res["reason"] = "smart_failed"
	default:
		res["status"] = "up"
		return c.JSON(res)
	}
	return c.Status(fiber.StatusServiceUnavailable).JSON(res)
}

func registerDashboardRoute(app *fiber.App, f *fetcher.Fetcher, tmpl *template.Template, cfgPtr *atomic.Pointer[config.Config], histStore *history.Store) {
	handler := func(c fiber.Ctx) error {
		curCfg := cfgPtr.Load()
		reqCtx, cancel := context.WithTimeout(c.Context(), httpHandlerTimeout)
		defer cancel()

		nodes, isCached := f.FetchAll(reqCtx)
		data := buildTemplateData(nodes)
		data.pageData = newPageData(
			"storage", curCfg, histStore != nil, nodes,
		)

		setCacheHeaders(c, f, isCached)
		c.Set("Cache-Control", "no-store")
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "base", data); err != nil {
			slog.Error("template execution failed", "error", err)
			return fiber.ErrInternalServerError
		}

		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.Send(buf.Bytes())
	}
	app.Get("/storage", handler)
	app.Get("/pools", handler)
}

func registerSystemRoute(app *fiber.App, f *fetcher.Fetcher, tmpl *template.Template, cfgPtr *atomic.Pointer[config.Config], histStore *history.Store) {
	handler := func(c fiber.Ctx) error {
		curCfg := cfgPtr.Load()
		reqCtx, cancel := context.WithTimeout(c.Context(), httpHandlerTimeout)
		defer cancel()

		nodes, isCached := f.FetchAll(reqCtx)
		data := buildSystemPageData(hostViews(nodes))
		data.pageData = newPageData(
			"system", curCfg, histStore != nil, nodes,
		)

		setCacheHeaders(c, f, isCached)
		c.Set("Cache-Control", "no-store")
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "base", data); err != nil {
			slog.Error("system template execution failed", "error", err)
			return fiber.ErrInternalServerError
		}
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.Send(buf.Bytes())
	}
	app.Get("/", handler)
	app.Get("/system", handler)
}

func registerHistoryRoutes(
	app *fiber.App,
	rl fiber.Handler,
	histStore *history.Store,
	histTmpl *template.Template,
	cfgPtr *atomic.Pointer[config.Config],
	f *fetcher.Fetcher,
) {
	app.Get("/history", func(c fiber.Ctx) error {
		curCfg := cfgPtr.Load()
		var buf bytes.Buffer
		data := historyData{
			pageData: newPageData(
				"history", curCfg, true, f.Snapshot(),
			),
			RetentionHours: int(histStore.Retention().Hours()),
		}
		if err := histTmpl.ExecuteTemplate(&buf, "base", data); err != nil {
			slog.Error("history template execution failed", "error", err)
			return fiber.ErrInternalServerError
		}
		c.Set("Content-Type", "text/html; charset=utf-8")
		c.Set("Cache-Control", "no-store")
		return c.Send(buf.Bytes())
	})

	app.Get("/api/history/series", rl, func(c fiber.Ctx) error {
		series, err := histStore.ListSeries()
		if err != nil {
			slog.Error("history list series failed", "error", err)
			return fiber.ErrInternalServerError
		}
		filtered := series[:0]
		for _, s := range series {
			if s.Kind == "fs" && history.SkipFSMount(s.Name) {
				continue // hide boot/EFI mounts recorded before this filter existed
			}
			filtered = append(filtered, s)
		}
		if filtered == nil {
			filtered = []history.SeriesInfo{}
		}
		return c.JSON(filtered)
	})

	app.Get("/api/history/query", rl, func(c fiber.Ctx) error {
		key := c.Query("key")
		if key == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "key required"})
		}
		fromUnix, toUnix, bucketSecs, err := parseHistoryQueryParams(c)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}

		now := time.Now()
		to := now
		if toUnix > 0 {
			to = time.Unix(toUnix, 0)
		}
		from := to.Add(-24 * time.Hour) // default: 24h before to
		if fromUnix > 0 {
			from = time.Unix(fromUnix, 0)
		}
		if from.After(to) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "from must be before to"})
		}

		points, err := histStore.Query(key, from, to, bucketSecs)
		if err != nil {
			slog.Error("history query failed", "error", err, "key", key)
			return fiber.ErrInternalServerError
		}
		if points == nil {
			points = []history.Point{}
		}
		return c.JSON(points)
	})
}

// parseHistoryQueryParams parses and validates the from/to/bucket query params
// shared by the /api/history/query endpoint.
func parseHistoryQueryParams(c fiber.Ctx) (fromUnix, toUnix, bucketSecs int64, err error) {
	if s := c.Query("from"); s != "" {
		if fromUnix, err = strconv.ParseInt(s, 10, 64); err != nil {
			return 0, 0, 0, fmt.Errorf("invalid from")
		}
	}
	if s := c.Query("to"); s != "" {
		if toUnix, err = strconv.ParseInt(s, 10, 64); err != nil {
			return 0, 0, 0, fmt.Errorf("invalid to")
		}
	}
	if s := c.Query("bucket"); s != "" {
		if bucketSecs, err = strconv.ParseInt(s, 10, 64); err != nil {
			return 0, 0, 0, fmt.Errorf("invalid bucket")
		}
	}
	if fromUnix < 0 || toUnix < 0 {
		return 0, 0, 0, fmt.Errorf("timestamps must be non-negative")
	}
	if bucketSecs < 0 {
		return 0, 0, 0, fmt.Errorf("bucket must be non-negative")
	}
	return fromUnix, toUnix, bucketSecs, nil
}

func shutdownOnSignal(shutdownSigs <-chan os.Signal, ctx context.Context, cancel context.CancelFunc, app *fiber.App) {
	select {
	case <-shutdownSigs:
		slog.Info("shutdown signal received")
		cancel()
		_ = app.Shutdown()
	case <-ctx.Done():
	}
}

func setupLogger(cfg *config.Config) {
	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}

	var handler slog.Handler
	if cfg.LogFormat == "json" {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	} else {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	}

	slog.SetDefault(slog.New(handler))
}

func buildTemplateData(nodes []model.NodeData) templateData {
	storage := make([]model.NodeData, 0, len(nodes))
	for _, node := range nodes {
		if showOnStorage(node) {
			storage = append(storage, node)
		}
	}
	views := nodeViews(storage)
	d := templateData{
		Nodes:      views,
		NodesJSON:  template.JS(toJSON(views)), //nolint:gosec // skipcq: GSC-G203 -- json.Marshal escapes <, >, & for inline scripts
		FetchedAt:  time.Now().Format("15:04:05"),
		TotalNodes: len(views),
	}
	for _, node := range views {
		if node.Exporters.ZFS.Error != "" || node.Exporters.Smartctl.Error != "" {
			d.StorageErrors++
		}
		for _, pool := range node.Pools {
			d.TotalPools++
			switch pool.Health {
			case model.HealthOnline:
				d.HealthyPools++
			case model.HealthDegraded:
				d.DegradedPools++
			default:
				d.ErroredPools++
			}
		}
	}
	return d
}

func serveHealthCheck(c fiber.Ctx, f *fetcher.Fetcher, label, poolName string, cfg *config.Config) error {
	slog.Debug("health check", "label", label, "pool", poolName)

	ctx, cancel := context.WithTimeout(c.Context(), httpHandlerTimeout)
	defer cancel()

	nodes, isCached := f.FetchAll(ctx)
	setCacheHeaders(c, f, isCached)

	node, err := findNodeByLabel(nodes, label)
	if err != nil {
		slog.Debug("node not found", "label", label)
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"status": "not_found",
			"label":  label,
		})
	}

	return healthResponse(c, node, label, poolName, cfg)
}

// healthResponse reports a fetched node; pool checks depend only on ZFS.
func healthResponse(c fiber.Ctx, node *model.NodeData, label, poolName string, cfg *config.Config) error {
	if node.FetchedAt.IsZero() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"status": "unknown", "reason": "discovery_pending", "label": node.Label,
		})
	}
	failed := node.Exporters.HasErrors()
	if poolName != "" {
		failed = node.Exporters.ZFS.Error != ""
	}
	if failed {
		slog.Debug("required exporter unavailable", "label", label)
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"status":    "down",
			"reason":    "exporter_unavailable",
			"label":     node.Label,
			"location":  node.Location,
			"exporters": node.Exporters,
		})
	}

	if poolName == "" {
		return nodeHealthResponse(c, node, label, cfg)
	}
	return poolHealthResponse(c, node, label, poolName, cfg)
}

func nodeHealthResponse(c fiber.Ctx, node *model.NodeData, label string, cfg *config.Config) error {
	badPools := []string{} // skipcq: GO-W1027 -- JSON must encode [] not null
	var overThreshold []string
	for _, pool := range node.Pools {
		if pool.Health != model.HealthOnline {
			badPools = append(badPools, pool.Name)
		} else if cfg.MaxUsagePercent > 0 && pool.UsedPercent > cfg.MaxUsagePercent {
			overThreshold = append(overThreshold, pool.Name)
		}
	}
	// Proxmox storages that the ZFS exporter does not cover (LVM thin, PBS, NFS, dir).
	var inactiveStorages, fullStorages []string
	for _, st := range pveStorages(*node) {
		if !st.Active {
			inactiveStorages = append(inactiveStorages, st.Name)
		} else if cfg.MaxUsagePercent > 0 && st.UsedPct() > cfg.MaxUsagePercent {
			fullStorages = append(fullStorages, st.Name)
		}
	}

	status := fiber.StatusOK
	state := "up"
	reason := ""
	switch {
	case node.Exporters.ZFS.Required() && len(node.Pools) == 0:
		status = fiber.StatusServiceUnavailable
		state = "no_pools"
		slog.Debug("node has 0 pools", "label", label)
	case len(badPools) > 0:
		status = fiber.StatusServiceUnavailable
		state = "degraded"
		reason = "unhealthy_pools"
		slog.Debug("node has unhealthy pools", "label", label, "pools", badPools)
	case len(overThreshold) > 0:
		status = fiber.StatusServiceUnavailable
		state = "degraded"
		reason = "pool_over_threshold"
		slog.Debug("node has pools over threshold", "label", label, "pools", overThreshold, "threshold", cfg.MaxUsagePercent)
	case len(inactiveStorages) > 0:
		status = fiber.StatusServiceUnavailable
		state = "degraded"
		reason = "storage_inactive"
	case len(fullStorages) > 0:
		status = fiber.StatusServiceUnavailable
		state = "degraded"
		reason = "storage_over_threshold"
	case !node.Exporters.AnyAvailable():
		state = "unknown"
		reason = "no_exporters_detected"
	}

	res := fiber.Map{
		"status":          state,
		"label":           node.Label,
		"location":        node.Location,
		"pool_count":      len(node.Pools),
		"unhealthy_pools": badPools,
		"exporters":       node.Exporters,
	}
	if reason != "" {
		res["reason"] = reason
	}
	if len(overThreshold) > 0 {
		res["over_threshold_pools"] = overThreshold
	}
	if len(inactiveStorages) > 0 {
		res["inactive_storages"] = inactiveStorages
	}
	if len(fullStorages) > 0 {
		res["over_threshold_storages"] = fullStorages
	}

	return c.Status(status).JSON(res)
}

func poolHealthResponse(c fiber.Ctx, node *model.NodeData, label, poolName string, cfg *config.Config) error {
	pool, err := findPoolByName(node.Pools, poolName)
	if err != nil {
		slog.Debug("pool not found", "label", label, "pool", poolName)
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"status": "down",
			"label":  node.Label,
			"pool":   poolName,
			"reason": "pool_not_found",
		})
	}

	status := fiber.StatusOK
	state := "up"
	reason := ""
	if pool.Health != model.HealthOnline {
		status = fiber.StatusServiceUnavailable
		state = "degraded"
		reason = "pool_unhealthy"
		slog.Debug("pool health is not ONLINE", "label", label, "pool", poolName, "health", pool.Health)
	} else if cfg.MaxUsagePercent > 0 && pool.UsedPercent > cfg.MaxUsagePercent {
		status = fiber.StatusServiceUnavailable
		state = "degraded"
		reason = "pool_over_threshold"
		slog.Debug("pool is over threshold", "label", label, "pool", poolName, "used_percent", pool.UsedPercent, "threshold", cfg.MaxUsagePercent)
	}

	res := fiber.Map{
		"status":   state,
		"label":    node.Label,
		"location": node.Location,
		"pool":     pool.Name,
		"health":   pool.Health,
	}
	if reason != "" {
		res["reason"] = reason
	}
	if status != fiber.StatusOK {
		res["used_percent"] = pool.UsedPercent
	}

	return c.Status(status).JSON(res)
}

func findNodeByLabel(nodes []model.NodeData, label string) (*model.NodeData, error) {
	for i := range nodes {
		if nodes[i].Label == label {
			return &nodes[i], nil
		}
	}
	return nil, fmt.Errorf("label %q not found", label)
}

func findPoolByName(pools []model.Pool, name string) (*model.Pool, error) {
	for i := range pools {
		if pools[i].Name == name {
			return &pools[i], nil
		}
	}
	return nil, fmt.Errorf("pool %q not found", name)
}

func setCacheHeaders(c fiber.Ctx, f *fetcher.Fetcher, isCached bool) {
	if isCached {
		c.Set("X-Cache", "HIT")
		expiresAt, _ := f.CacheInfo()
		if time.Now().Before(expiresAt) {
			c.Set("X-Cache-Expires-In", time.Until(expiresAt).Round(time.Second).String())
		}
	} else {
		c.Set("X-Cache", "MISS")
	}
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"humanBytes":     model.HumanBytes,
		"fmtRate":        fmtRate,
		"fmtUptime":      fmtUptime,
		"loadClass":      loadClass,
		"pctClass":       pctClass,
		"healthClass":    healthClass,
		"fmtNodeTime":    fmtNodeTime,
		"toJSON":         toJSON,
		"hostTitle":      hostTitle,
		"plural":         plural,
		"fmtSpeed":       fmtSpeed,
		"exitStatusDesc": exitStatusDesc,
		"diskHasIssues":  diskHasIssues,
		"tempBarPct":     tempBarPct,
		"fmtHours":       fmtHours,
		"maskSerial":     maskSerial,
		"diskTypeLabel":  diskTypeLabel,
		"diskTypeClass":  diskTypeClass,
		"tempClass":      tempClass,
		"gt0":            func(f float64) bool { return f > 0 },
		"gte":            func(a, b float64) bool { return a >= b },
		"mul100":         func(f float64) float64 { return f * 100 },
		"mul512":         func(f float64) float64 { return f * 512 },
		"add":            func(a, b float64) float64 { return a + b },
		"sub":            func(a, b float64) float64 { return a - b },
		"percent": func(part, total float64) float64 {
			if total <= 0 {
				return 0
			}
			return part / total
		},
		"memUsed": func(s *model.SystemInfo) float64 { return s.MemTotal - s.MemAvailable },
		"join":    strings.Join,
		"dict":    dict,
	}
}

// fmtRate renders a bytes-per-second rate with IEC units.
func fmtRate(bps float64) string {
	return model.HumanBytes(bps) + "/s"
}

// fmtUptime renders seconds as "12d 4h" / "4h 23m" / "23m".
func fmtUptime(secs float64) string {
	total := int(secs)
	days := total / 86400
	hrs := (total % 86400) / 3600
	mins := (total % 3600) / 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hrs)
	case hrs > 0:
		return fmt.Sprintf("%dh %dm", hrs, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// loadClass colors a load average relative to core count.
func loadClass(load float64, cores int) string {
	if cores <= 0 {
		return ""
	}
	ratio := load / float64(cores)
	switch {
	case ratio >= 1.0:
		return "bad"
	case ratio >= 0.7:
		return "warn"
	default:
		return "ok"
	}
}

// pctClass colors a usage percentage with the standard thresholds.
func pctClass(pct float64) string {
	switch {
	case pct >= 90:
		return "bad"
	case pct >= 75:
		return "warn"
	default:
		return ""
	}
}

func healthClass(h model.PoolHealth) string {
	switch h {
	case model.HealthOnline:
		return "health-online"
	case model.HealthDegraded:
		return "health-degraded"
	default:
		return "health-faulted"
	}
}

func fmtNodeTime(t time.Time) string {
	return t.Format("15:04:05")
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func fmtSpeed(bps float64) string {
	switch {
	case bps >= 1e9:
		return fmt.Sprintf("%.0f Gb/s", bps/1e9)
	case bps >= 1e6:
		return fmt.Sprintf("%.0f Mb/s", bps/1e6)
	default:
		return fmt.Sprintf("%.0f b/s", bps)
	}
}

// exitStatusBits names the smartctl exit status bits, from bit 0, as smartctl(8) lists them.
var exitStatusBits = []string{
	"command line error",
	"device open failed",
	"SMART command failed",
	"disk failing",
	"prefail attributes",
	"prev failed attributes",
	"error log has errors",
	"self-test errors",
}

func exitStatusDesc(code float64) string {
	n := int(code)
	var parts []string
	for i, name := range exitStatusBits {
		if n&(1<<i) != 0 {
			parts = append(parts, name)
		}
	}
	if len(parts) == 0 && n != 0 {
		return fmt.Sprintf("code %d", n)
	}
	return strings.Join(parts, ", ")
}

func diskHasIssues(d model.DiskInfo) bool {
	return d.PendingSectors > 0 || d.OfflineUncorrectable > 0 || d.ReportedUncorrect > 0 ||
		d.ProgramFailCount > 0 || d.EraseFailCount > 0 ||
		(d.HasExitStatus && d.ExitStatus > 0)
}

func tempBarPct(temp, maxTemp float64) string {
	if maxTemp <= 0 {
		maxTemp = 70
	}
	pct := (temp / maxTemp) * 100
	if pct > 100 {
		pct = 100
	} else if pct < 0 {
		pct = 0
	}
	return fmt.Sprintf("%.1f", pct)
}

func fmtHours(h float64) string {
	total := int(h)
	days := total / 24
	hrs := total % 24
	if days >= 365 {
		y := days / 365
		d := days % 365
		return fmt.Sprintf("%dy %dd", y, d)
	}
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hrs)
	}
	return fmt.Sprintf("%dh", total)
}

func maskSerial(s string) string {
	const maskLen = 5
	if len(s) <= maskLen {
		return strings.Repeat("x", len(s))
	}
	return s[:len(s)-maskLen] + strings.Repeat("x", maskLen)
}

func diskTypeLabel(iface string, rpm int) string {
	switch {
	case iface == "nvme":
		return "NVMe"
	case rpm > 0:
		return "HDD"
	default:
		return "SSD"
	}
}

func diskTypeClass(iface string, rpm int) string {
	switch {
	case iface == "nvme":
		return "nvme"
	case rpm > 0:
		return "hdd"
	default:
		return "ssd"
	}
}

func tempClass(c float64) string {
	switch {
	case c > 55:
		return "hot"
	case c > 45:
		return "warm"
	default:
		return "cool"
	}
}

func dict(values ...any) (map[string]any, error) {
	if len(values)%2 != 0 {
		return nil, fmt.Errorf("invalid dict call: expected even number of arguments")
	}
	d := make(map[string]any, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict keys must be strings")
		}
		d[key] = values[i+1]
	}
	return d, nil
}
