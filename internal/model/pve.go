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
}

// Kind returns the short label shown in the UI.
func (g Guest) Kind() string {
	if g.Type == "lxc" {
		return "LXC"
	}
	return "VM"
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
