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

func TestExtractPVEExtras(t *testing.T) {
	s := func(name string, v float64, labels ...string) parser.Sample {
		m := map[string]string{}
		for i := 0; i+1 < len(labels); i += 2 {
			m[labels[i]] = labels[i+1]
		}
		return parser.Sample{Name: name, Labels: m, Value: v}
	}
	samples := []parser.Sample{
		s("pve_guest_info", 1, "id", "lxc/307", "name", "docker-runner", "type", "lxc", "template", "0"),
		s("pve_guest_info", 1, "id", "qemu/301", "name", "vm", "type", "qemu", "template", "0"),
		s("pve_up", 0, "id", "qemu/301"),
		s("pve_onboot_status", 1, "id", "qemu/301"),
		s("pve_lock_state", 0, "id", "lxc/307", "state", "migrate"),
		s("pve_lock_state", 1, "id", "lxc/307", "state", "backup"),
		s("pve_not_backed_up_info", 1, "id", "lxc/307"),
		s("pve_disk_usage_bytes", 25, "id", "lxc/307"),
		s("pve_disk_size_bytes", 100, "id", "lxc/307"),
		s("pve_version_info", 1, "version", "9.2.20"),
		s("pve_not_backed_up_total", 1, "id", "cluster/node1"),
		s("pve_storage_info", 1, "id", "storage/node1/local-lvm", "storage", "local-lvm", "plugintype", "lvmthin", "content", "images,rootdir"),
		s("pve_up", 1, "id", "storage/node1/local-lvm"),
		s("pve_disk_usage_bytes", 37, "id", "storage/node1/local-lvm"),
		s("pve_disk_size_bytes", 100, "id", "storage/node1/local-lvm"),
	}
	g := ExtractGuests(samples)
	ct, vm := g[0], g[1]
	if !ct.NoBackup || ct.Lock != "backup" || ct.DiskPct() != 25 {
		t.Errorf("container = %+v", ct)
	}
	if !vm.NeedsStart() || vm.NoBackup || vm.Lock != "" {
		t.Errorf("vm = %+v", vm)
	}
	info := ExtractPVEInfo(samples)
	if info.Version != "9.2.20" || !info.BackupChecked || info.NotBackedUp != 1 || len(info.Storages) != 1 {
		t.Fatalf("info = %+v", info)
	}
	if st := info.Storages[0]; st.Name != "local-lvm" || st.Type != "lvmthin" || !st.Active || st.UsedPct() != 37 {
		t.Errorf("storage = %+v", st)
	}
}
