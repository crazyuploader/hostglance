package cmd

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/crazyuploader/hostglance/internal/config"
	"github.com/crazyuploader/hostglance/internal/fetcher"
	"github.com/crazyuploader/hostglance/internal/model"
	"github.com/spf13/cobra"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Test the configuration and connect to each host one time",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := configInitError(); err != nil {
			return fmt.Errorf("read config: %w", err)
		}
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if len(cfg.Hosts) == 0 {
			return fmt.Errorf("no hosts configured: add hosts to config.yaml, or use --hosts or --endpoints")
		}
		nodes := fetcher.New(cfg.Hosts, 0).Refresh(cmd.Context())
		if failed := writeCheck(cmd.OutOrStdout(), nodes); failed > 0 {
			return fmt.Errorf("%d host(s) have a required exporter that did not respond", failed)
		}
		return nil
	},
}

// init registers the check command.
func init() {
	rootCmd.AddCommand(checkCmd)
}

// writeCheck prints one row per host and returns how many hosts failed.
func writeCheck(out io.Writer, nodes []model.NodeData) int {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "HOST\tNODE\tZFS\tSMARTCTL")
	failed := 0
	for _, n := range nodes {
		e := n.Exporters
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", n.Label, checkState(e.Node), checkState(e.ZFS), checkState(e.Smartctl))
		if e.HasErrors() {
			failed++
		}
	}
	_ = w.Flush()
	return failed
}

// checkState renders one exporter status as a table cell.
func checkState(s model.ExporterStatus) string {
	switch {
	case s.Available:
		return "ok"
	case s.Required():
		return "MISSING"
	case s.Mode == string(config.ModeDisabled):
		return "disabled"
	default:
		return "not found"
	}
}
