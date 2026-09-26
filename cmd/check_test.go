package cmd

import (
	"strings"
	"testing"

	"github.com/crazyuploader/hostglance/internal/model"
)

func TestWriteCheck(t *testing.T) {
	nodes := []model.NodeData{
		{Label: "good", Exporters: model.ExporterStatuses{
			Node:     model.ExporterStatus{Mode: "auto", Available: true},
			ZFS:      model.ExporterStatus{Mode: "auto"},
			Smartctl: model.ExporterStatus{Mode: "disabled"},
		}},
		{Label: "bad", Exporters: model.ExporterStatuses{
			ZFS: model.ExporterStatus{Mode: "enabled", Error: "exporter unavailable"},
		}},
	}
	var out strings.Builder
	if failed := writeCheck(&out, nodes); failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	for _, want := range []string{"ok", "not found", "disabled", "MISSING"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}
