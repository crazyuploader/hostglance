package server

import (
	"cmp"
	"net/netip"
	"slices"
	"strconv"
	"strings"
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
	data.StorageEnabled = slices.ContainsFunc(nodes, showOnStorage)
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
	// PVEStorages are Proxmox storages that the ZFS exporter does not cover.
	PVEStorages []model.PVEStorage `json:"pve_storages,omitempty"`
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
	Guests    []guestView            `json:"guests,omitempty"`
	PVE       *model.PVEInfo         `json:"pve,omitempty"`
	// Proxmox is this host's own guest record when a Proxmox host reports it.
	Proxmox   *model.Guest `json:"proxmox,omitempty"`
	PoolCount int          `json:"pool_count"`
	DiskCount int          `json:"disk_count"`
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

// guestView is one Proxmox guest row. Card is the label of the configured
// host that is this guest, so the row links to that card instead of repeating it.
type guestView struct {
	model.Guest
	Card string `json:"card,omitempty"`
}

// systemViews includes available and required node exporters in /api/system.
func systemViews(nodes []model.NodeData) []systemView {
	return slices.DeleteFunc(linkedViews(nodes), func(v systemView) bool {
		return !v.Exporters.Node.Visible()
	})
}

// hostViews keeps all configured hosts on the homepage, including hosts for
// which automatic discovery has not found any exporters yet. Guests follow
// their parent; otherwise configuration order is kept.
func hostViews(nodes []model.NodeData) []systemView {
	views := linkedViews(nodes)
	out := make([]systemView, 0, len(views))
	for _, v := range views {
		if v.Parent != "" {
			continue
		}
		out = append(out, v)
		for _, guest := range views {
			if guest.Parent == v.Label {
				out = append(out, guest)
			}
		}
	}
	return out
}

// linkedViews builds host views in configuration order and joins them with
// Proxmox guest data: a configured host whose label or hostname matches a
// guest name gets that Proxmox host as its parent (unless parent is set in
// the configuration) and its guest record. The guest row links to its card.
// ponytail: name match only; add a vmid setting if names ever collide.
func linkedViews(nodes []model.NodeData) []systemView {
	type guestRef struct {
		host  string
		guest model.Guest
	}
	guests := map[string]guestRef{} // lowercase guest name
	for _, n := range nodes {
		for _, g := range n.Guests {
			guests[strings.ToLower(g.Name)] = guestRef{host: n.Label, guest: g}
		}
	}
	match := func(n model.NodeData) (guestRef, bool) {
		names := []string{n.Label}
		if n.System != nil && n.System.Hostname != "" {
			names = append(names, n.System.Hostname)
		}
		for _, name := range names {
			ref, ok := guests[strings.ToLower(name)]
			if ok && ref.host != n.Label {
				return ref, true
			}
		}
		return guestRef{}, false
	}
	cards := map[string]string{} // guest id on a host -> configured card label
	views := make([]systemView, len(nodes))
	for i, n := range nodes {
		views[i] = hostView(n)
		if ref, ok := match(n); ok {
			g := ref.guest
			views[i].Proxmox = &g
			views[i].Parent = cmp.Or(n.Parent, ref.host)
			cards[ref.host+"/"+g.ID] = n.Label
		}
	}
	for i := range views {
		for j := range views[i].Guests {
			views[i].Guests[j].Card = cards[views[i].Label+"/"+views[i].Guests[j].ID]
		}
	}
	return views
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
		Guests:    guestViews(node.Guests),
		PVE:       node.PVE,
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
			PVEStorages:  pveStorages(n),
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

// guestViews copies guests into rows that can carry a card link.
func guestViews(guests []model.Guest) []guestView {
	if len(guests) == 0 {
		return nil
	}
	out := make([]guestView, len(guests))
	for i, g := range guests {
		out[i] = guestView{Guest: g}
	}
	return out
}

// pveStorages returns the Proxmox storages to show on the Storage page.
// A zfspool storage repeats a ZFS exporter pool, so it is left out when the
// ZFS exporter answers.
func pveStorages(n model.NodeData) []model.PVEStorage {
	if n.PVE == nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(n.PVE.Storages), func(s model.PVEStorage) bool {
		return s.Type == "zfspool" && n.Exporters.ZFS.Available
	})
}

// showOnStorage reports whether a host has a section on the Storage page.
func showOnStorage(n model.NodeData) bool {
	return n.Exporters.StorageVisible() || len(pveStorages(n)) > 0
}
