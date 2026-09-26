package cmd

import (
	"fmt"

	"github.com/crazyuploader/hostglance/internal/config"
	"github.com/crazyuploader/hostglance/internal/server"
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the system and storage dashboard",
	RunE: func(_ *cobra.Command, _ []string) error {
		if err := configInitError(); err != nil {
			return fmt.Errorf("read config: %w", err)
		}

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if len(cfg.Hosts) == 0 {
			return fmt.Errorf("no hosts configured; use --hosts, --endpoints, or config.yaml")
		}
		return server.Start(cfg)
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
