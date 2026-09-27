package server

import (
	"net/netip"
	"slices"
	"strconv"
	"time"

	"github.com/crazyuploader/hostglance/internal/config"
	"github.com/crazyuploader/hostglance/internal/model"
)

// pageData carries the fields every page template needs (topbar/nav state).
// Page-specific data structs embed it.
type pageData struct {
	ActiveTab      string // "storage" | "system" | "history"
	HistoryEnabled bool
	StorageEnabled bool
	RefreshSecs    int
}

// newPageData builds the shared page fields for the given active tab.
func newPageData(
	tab string,
	cfg *config.Config,
	historyEnabled bool,
	nodes []model.NodeData,
) pageData {
	data := pageData{
		ActiveTab:      tab,
		HistoryEnabled: historyEnabled,
		RefreshSecs:    int(cfg.Refresh.Seconds()),
	}
	for _, node := range nodes {
		if node.Exporters.StorageVisible() {
			data.StorageEnabled = true
			break
		}
	}
	return data
}

// nodeView is the browser-facing subset of NodeData, used both for the
// page's inline JS and the /api/metrics response.
// Scrape URLs stay in config and are never exposed to browsers or API consumers.
type nodeView struct {
	Label        string                 `json:"label"`
	Location     string                 `json:"location,omitempty"`
	FetchedAt    time.Time              `json:"fetched_at"`
	Error        string                 `json:"error,omitempty"`
	Exporters    model.ExporterStatuses `json:"exporters"`
	ExporterInfo model.ExporterInfo     `json:"exporter_info,omitempty"`
	SmartctlInfo model.SmartctlInfo     `json:"smartctl_info,omitempty"`
	Pools        []model.Pool           `json:"pools"`
	Disks        []model.DiskInfo       `json:"disks,omitempty"`
	System       *model.SystemInfo      `json:"system,omitempty"`
}

// systemView is the /api/system response row for one endpoint with a
// node_exporter available or explicitly required.
type systemView struct {
	Label     string                 `json:"label"`
	Location  string                 `json:"location,omitempty"`
	Parent    string                 `json:"parent,omitempty"`
	FetchedAt time.Time              `json:"fetched_at"`
	Error     string                 `json:"error,omitempty"`
	System    *model.SystemInfo      `json:"system,omitempty"`
	Exporters model.ExporterStatuses `json:"exporters"`
	Guests    []model.Guest          `json:"guests,omitempty"`
	PoolCount int                    `json:"pool_count"`
	DiskCount int                    `json:"disk_count"`
}

// systemPageData is the data passed to the system page template.
type systemPageData struct {
	pageData
	Nodes     []systemView
	FetchedAt string

	// Fleet KPI aggregates
	TotalNodes     int
	GuestNodes     int // hosts with a parent; excluded from resource totals
	ExporterErrors int
	TotalCores     int
	AvgCPUPct      float64
	HasCPU         bool
	MemUsedBytes   float64
	MemTotal       float64
	MaxTempC       float64
}

// buildSystemPageData aggregates fleet KPIs over the system views.
func buildSystemPageData(views []systemView) systemPageData {
	d := systemPageData{
		Nodes:      views,
		FetchedAt:  time.Now().Format("15:04:05"),
		TotalNodes: len(views),
	}
	var cpuSum float64
	var cpuN int
	for _, v := range views {
		if v.Exporters.HasErrors() {
			d.ExporterErrors++
		}
		if v.Parent != "" {
			d.GuestNodes++
		}
		if v.System == nil {
			continue
		}
		for _, t := range v.System.Temps {
			d.MaxTempC = max(d.MaxTempC, t.Celsius)
		}
		if v.Parent != "" {
			continue // the parent already counts the guest's cores and memory
		}
		s := v.System
		d.TotalCores += s.Cores
		d.MemTotal += s.MemTotal
		d.MemUsedBytes += s.MemTotal - s.MemAvailable
		if s.HasCPURates {
			cpuSum += s.CPUBusyPct
			cpuN++
		}
	}
	if cpuN > 0 {
		d.AvgCPUPct = cpuSum / float64(cpuN)
		d.HasCPU = true
	}
	return d
}

// systemViews includes available and required node exporters in /api/system.
func systemViews(nodes []model.NodeData) []systemView {
	out := make([]systemView, 0, len(nodes))
	for _, node := range nodes {
		if node.Exporters.Node.Visible() {
			out = append(out, hostView(node))
		}
	}
	return out
}

// hostViews keeps all configured hosts on the homepage, including hosts for
// which automatic discovery has not found any exporters yet. Guests follow
// their parent; otherwise configuration order is kept.
func hostViews(nodes []model.NodeData) []systemView {
	out := make([]systemView, 0, len(nodes))
	for _, node := range nodes {
		if node.Parent != "" {
			continue
		}
		out = append(out, hostView(node))
		for _, guest := range nodes {
			if guest.Parent == node.Label {
				out = append(out, hostView(guest))
			}
		}
	}
	return out
}

// hostView builds the system row for one host.
func hostView(node model.NodeData) systemView {
	sys := node.System
	if sys != nil && node.Exporters.ZFS.Available {
		// Pools already cover ZFS space. Copy so the shared cache stays read-only.
		filtered := *sys
		filtered.Filesystems = slices.DeleteFunc(slices.Clone(sys.Filesystems), func(fs model.FSInfo) bool {
			return fs.FSType == "zfs"
		})
		sys = &filtered
	}
	return systemView{
		Label:     node.Label,
		Location:  node.Location,
		Parent:    node.Parent,
		Guests:    node.Guests,
		FetchedAt: node.FetchedAt,
		Error:     node.Exporters.Node.Error,
		System:    sys,
		Exporters: node.Exporters,
		PoolCount: len(node.Pools),
		DiskCount: len(node.Disks),
	}
}

// nodeViews converts fetched node data into its URL-stripped view form.
func nodeViews(nodes []model.NodeData) []nodeView {
	views := make([]nodeView, len(nodes))
	for i, n := range nodes {
		views[i] = nodeView{
			Label:        n.Label,
			Location:     n.Location,
			FetchedAt:    n.FetchedAt,
			Error:        n.Error,
			Exporters:    n.Exporters,
			ExporterInfo: n.ExporterInfo,
			SmartctlInfo: n.SmartctlInfo,
			Pools:        n.Pools,
			Disks:        n.Disks,
			System:       n.System,
		}
	}
	return views
}

// hostTitle prefers the reported hostname over a bare IP label.
// The label itself stays the stable history key.
func hostTitle(label string, sys *model.SystemInfo) string {
	if _, err := netip.ParseAddr(label); err == nil && sys != nil && sys.Hostname != "" {
		return sys.Hostname
	}
	return label
}

// plural formats a count with a naively pluralized noun.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
