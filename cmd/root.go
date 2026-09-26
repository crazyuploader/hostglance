package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string
var initConfigErr error

var rootCmd = &cobra.Command{
	Use:           "hostglance",
	Short:         "System and storage monitoring with host discovery",
	Long:          `Discover node, ZFS, and SMART exporters on configured hosts and serve a live system and storage dashboard.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command and prints any error to stderr.
func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	return nil
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default ./config.yaml)")
	rootCmd.PersistentFlags().StringSlice("endpoints", nil, "ZFS exporter /metrics URLs (comma-separated or repeated)")
	rootCmd.PersistentFlags().StringSlice("hosts", nil, "hostnames or IPs to discover (comma-separated or repeated)")
	rootCmd.PersistentFlags().String("addr", ":8054", "listen address")
	rootCmd.PersistentFlags().Int("refresh", 300, "auto-refresh interval in seconds")
	rootCmd.PersistentFlags().Bool("debug", false, "enable debug logging")
	rootCmd.PersistentFlags().StringSlice("trusted-proxies", nil, "list of trusted proxy IPs")
	rootCmd.PersistentFlags().Float64("max-usage-percent", 0, "usage threshold for health failure (0 to disable)")
	rootCmd.PersistentFlags().String("log-format", "text", "log format (text or json)")
	rootCmd.PersistentFlags().Bool("history-enabled", false, "enable time-series history storage")
	rootCmd.PersistentFlags().String("history-path", "./data/history.db", "path to history database file")
	rootCmd.PersistentFlags().Duration("history-retention", 0, "history retention period (e.g. 720h = 30 days; 0 uses config default)")
	rootCmd.PersistentFlags().Duration("history-record-interval", 0, "how often to record history samples (e.g. 5m; 0 uses refresh interval)")

	mustBindPFlag("endpoints", "endpoints")
	mustBindPFlag("hosts", "hosts")
	mustBindPFlag("addr", "addr")
	mustBindPFlag("refresh", "refresh")
	mustBindPFlag("debug", "debug")
	mustBindPFlag("trusted_proxies", "trusted-proxies")
	mustBindPFlag("max_usage_percent", "max-usage-percent")
	mustBindPFlag("log_format", "log-format")
	mustBindPFlag("history.enabled", "history-enabled")
	mustBindPFlag("history.path", "history-path")
	mustBindPFlag("history.retention", "history-retention")
	mustBindPFlag("history.record_interval", "history-record-interval")
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")
		viper.AddConfigPath("$HOME/.config/hostglance")
	}
	viper.SetEnvPrefix("HOSTGLANCE")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_")) // HOSTGLANCE_HISTORY_ENABLED
	viper.AutomaticEnv()
	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using config:", viper.ConfigFileUsed())
		viper.OnConfigChange(func(_ fsnotify.Event) {
			_ = syscall.Kill(os.Getpid(), syscall.SIGHUP)
		})
		viper.WatchConfig()
	} else if cfgFile != "" || !errors.As(err, new(viper.ConfigFileNotFoundError)) {
		initConfigErr = err
	}
}

func configInitError() error {
	return initConfigErr
}

func mustBindPFlag(viperKey, flagName string) {
	if err := viper.BindPFlag(viperKey, rootCmd.PersistentFlags().Lookup(flagName)); err != nil {
		panic(fmt.Sprintf("viper.BindPFlag(%q, %q): %v", viperKey, flagName, err))
	}
}
