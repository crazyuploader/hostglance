package model

// ExporterStatus describes availability without exposing scrape addresses.
// Error is populated only for an explicitly enabled exporter.
type ExporterStatus struct {
	Mode      string `json:"mode"`
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

// Required reports whether a missing exporter should fail the host health check.
func (s ExporterStatus) Required() bool { return s.Mode == "enabled" }

// Visible keeps required exporters visible even when their scrape fails.
func (s ExporterStatus) Visible() bool { return s.Available || s.Required() }

// ExporterStatuses keeps each host's metrics sources independent.
type ExporterStatuses struct {
	Node     ExporterStatus `json:"node"`
	ZFS      ExporterStatus `json:"zfs"`
	Smartctl ExporterStatus `json:"smartctl"`
}

// AnyAvailable reports whether discovery found at least one exporter.
func (s ExporterStatuses) AnyAvailable() bool {
	return s.Node.Available || s.ZFS.Available || s.Smartctl.Available
}

// HasErrors excludes absent automatic exporters from error counts.
func (s ExporterStatuses) HasErrors() bool {
	return s.Node.Error != "" || s.ZFS.Error != "" || s.Smartctl.Error != ""
}

// StorageVisible reports whether storage data or an explicit requirement exists.
func (s ExporterStatuses) StorageVisible() bool {
	return s.ZFS.Visible() || s.Smartctl.Visible()
}
