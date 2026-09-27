package model

import (
	"testing"

	"github.com/crazyuploader/hostglance/internal/parser"
)

func TestExtractGuests(t *testing.T) {
	s := func(name, id string, v float64, extra ...string) parser.Sample {
		labels := map[string]string{"id": id}
		for i := 0; i+1 < len(extra); i += 2 {
			labels[extra[i]] = extra[i+1]
		}
		return parser.Sample{Name: name, Labels: labels, Value: v}
	}
	samples := []parser.Sample{
		s("pve_guest_info", "qemu/502", 1, "name", "server08", "type", "qemu", "template", "0"),
		s("pve_guest_info", "lxc/302", 1, "name", "Vaultwarden", "type", "lxc", "template", "0"),
		s("pve_guest_info", "qemu/900", 1, "name", "debian-tpl", "type", "qemu", "template", "1"),
		s("pve_up", "qemu/502", 1),
		s("pve_up", "lxc/302", 0),
		s("pve_up", "node/PVE01", 1), // node and storage rows are not guests
		s("pve_cpu_usage_ratio", "qemu/502", 0.25),
		s("pve_cpu_usage_limit", "qemu/502", 2),
		s("pve_memory_usage_bytes", "qemu/502", 1024),
		s("pve_memory_size_bytes", "qemu/502", 4096),
	}
	guests := ExtractGuests(samples)
	if len(guests) != 2 {
		t.Fatalf("got %d guests, want 2 (template skipped): %+v", len(guests), guests)
	}
	vm, ct := guests[0], guests[1] // sorted by name, case-insensitive
	if vm.Name != "server08" || !vm.Running || vm.CPUPct != 25 || vm.Cores != 2 || vm.MemPct() != 25 || vm.Kind() != "VM" {
		t.Errorf("vm = %+v", vm)
	}
	if ct.Name != "Vaultwarden" || ct.Running || ct.Kind() != "LXC" || ct.MemPct() != 0 {
		t.Errorf("container = %+v", ct)
	}
}
