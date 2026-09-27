package model

import (
	"cmp"
	"slices"
	"strings"

	"github.com/crazyuploader/hostglance/internal/parser"
)

// Guest is one Proxmox VM or LXC container from prometheus-pve-exporter.
type Guest struct {
	ID         string  `json:"id"`   // "qemu/501" or "lxc/302"
	Name       string  `json:"name"` // Proxmox guest name
	Type       string  `json:"type"` // "qemu" or "lxc"
	Running    bool    `json:"running"`
	CPUPct     float64 `json:"cpu_pct"` // share of the guest's own cores
	Cores      float64 `json:"cores"`
	MemUsed    float64 `json:"mem_used"`
	MemTotal   float64 `json:"mem_total"`
	UptimeSecs float64 `json:"uptime_secs"`
	// DiskUsed is real for LXC. VMs report 0 without the QEMU guest agent.
	DiskUsed  float64 `json:"disk_used"`
	DiskTotal float64 `json:"disk_total"`
	OnBoot    bool    `json:"onboot"`
	Lock      string  `json:"lock,omitempty"` // "backup", "migrate", ... while locked
	// NoBackup is true when no Proxmox backup job includes this guest.
	NoBackup bool `json:"no_backup,omitempty"`
}

// DiskPct returns root disk use in percent. It is 0 when Proxmox has no value.
func (g Guest) DiskPct() float64 {
	if g.DiskTotal <= 0 || g.DiskUsed <= 0 {
		return 0
	}
	return g.DiskUsed / g.DiskTotal * 100
}

// NeedsStart reports a guest that is set to start at boot but is stopped.
func (g Guest) NeedsStart() bool { return g.OnBoot && !g.Running }

// PVEStorage is one Proxmox storage (a pool, a directory, NFS, PBS, ...).
type PVEStorage struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"`    // Proxmox plugin type: dir, lvmthin, zfspool, nfs, pbs, ...
	Content string  `json:"content"` // what the storage holds: images, backup, iso, ...
	Active  bool    `json:"active"`
	Used    float64 `json:"used"`
	Total   float64 `json:"total"`
}

// UsedPct returns the storage fill level in percent.
func (s PVEStorage) UsedPct() float64 {
	if s.Total <= 0 {
		return 0
	}
	return s.Used / s.Total * 100
}

// PVEInfo is the host-level data from prometheus-pve-exporter.
type PVEInfo struct {
	Version string `json:"version,omitempty"`
	// BackupChecked is true when the exporter reported backup coverage,
	// so a guest without NoBackup is known to be backed up.
	BackupChecked bool         `json:"backup_checked"`
	NotBackedUp   int          `json:"not_backed_up"`
	Storages      []PVEStorage `json:"storages,omitempty"`
}

// ExtractPVEInfo reads the version, backup coverage, and storages.
func ExtractPVEInfo(samples []parser.Sample) *PVEInfo {
	info := &PVEInfo{}
	storages := map[string]*PVEStorage{}
	for _, s := range samples {
		switch s.Name {
		case "pve_version_info":
			info.Version = s.Labels["version"]
		case "pve_not_backed_up_total":
			info.BackupChecked = true
			info.NotBackedUp += int(s.Value)
		case "pve_storage_info":
			id := s.Labels["id"]
			storages[id] = &PVEStorage{Name: s.Labels["storage"], Type: s.Labels["plugintype"], Content: s.Labels["content"]}
		}
	}
	for _, s := range samples {
		st := storages[s.Labels["id"]]
		if st == nil {
			continue
		}
		switch s.Name {
		case "pve_up":
			st.Active = s.Value == 1
		case "pve_disk_usage_bytes":
			st.Used = s.Value
		case "pve_disk_size_bytes":
			st.Total = s.Value
		}
	}
	for _, st := range storages {
		info.Storages = append(info.Storages, *st)
	}
	slices.SortFunc(info.Storages, func(a, b PVEStorage) int { return strings.Compare(a.Name, b.Name) })
	return info
}

// Kind returns the short label shown in the UI.
func (g Guest) Kind() string {
	if g.Type == "lxc" {
		return "LXC"
	}
	return "VM"
}

// VMID returns the Proxmox guest number, for example "502" for "qemu/502".
func (g Guest) VMID() string {
	_, id, _ := strings.Cut(g.ID, "/")
	return id
}

// MemPct returns memory use as a percentage of the guest's allocation.
func (g Guest) MemPct() float64 {
	if g.MemTotal <= 0 {
		return 0
	}
	return g.MemUsed / g.MemTotal * 100
}

// ExtractGuests reads VMs and containers from prometheus-pve-exporter samples.
// Templates are skipped. Guests are sorted by name.
func ExtractGuests(samples []parser.Sample) []Guest {
	byID := map[string]*Guest{}
	for _, s := range samples {
		if s.Name == "pve_guest_info" && s.Labels["template"] != "1" {
			id := s.Labels["id"]
			byID[id] = &Guest{ID: id, Name: s.Labels["name"], Type: s.Labels["type"]}
		}
	}
	for _, s := range samples {
		g := byID[s.Labels["id"]]
		if g == nil {
			continue
		}
		switch s.Name {
		case "pve_up":
			g.Running = s.Value == 1
		case "pve_onboot_status":
			g.OnBoot = s.Value == 1
		case "pve_lock_state":
			if s.Value == 1 {
				g.Lock = s.Labels["state"]
			}
		case "pve_not_backed_up_info":
			g.NoBackup = s.Value == 1
		case "pve_disk_usage_bytes":
			g.DiskUsed = s.Value
		case "pve_disk_size_bytes":
			g.DiskTotal = s.Value
		case "pve_cpu_usage_ratio":
			g.CPUPct = s.Value * 100
		case "pve_cpu_usage_limit":
			g.Cores = s.Value
		case "pve_memory_usage_bytes":
			g.MemUsed = s.Value
		case "pve_memory_size_bytes":
			g.MemTotal = s.Value
		case "pve_uptime_seconds":
			g.UptimeSecs = s.Value
		}
	}
	out := make([]Guest, 0, len(byID))
	for _, g := range byID {
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b Guest) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.ID, b.ID))
	})
	return out
}
