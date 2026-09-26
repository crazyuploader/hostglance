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
	Short:         "Monitor the system and storage health of your hosts",
	Long:          `HostGlance finds node, ZFS, and SMART exporters on the configured hosts and serves a live dashboard of their system and storage metrics.`,
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

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "configuration file (default ./config.yaml, then ~/.config/hostglance/config.yaml)")
	rootCmd.PersistentFlags().StringSlice("endpoints", nil, "old format: ZFS exporter /metrics URLs, separated by commas or repeated")
	rootCmd.PersistentFlags().StringSlice("hosts", nil, "hostnames or IP addresses to monitor, separated by commas or repeated")
	rootCmd.PersistentFlags().String("addr", ":8054", "address to listen on")
	rootCmd.PersistentFlags().Int("refresh", 300, "seconds between two metric collections")
	rootCmd.PersistentFlags().Bool("debug", false, "write debug messages to the log")
	rootCmd.PersistentFlags().StringSlice("trusted-proxies", nil, "IP addresses or CIDR ranges of trusted reverse proxies")
	rootCmd.PersistentFlags().Float64("max-usage-percent", 0, "pool usage percent above which health checks fail (0 turns the check off)")
	rootCmd.PersistentFlags().String("log-format", "text", "log format (text or json)")
	rootCmd.PersistentFlags().Bool("history-enabled", false, "record metrics for the history charts")
	rootCmd.PersistentFlags().String("history-path", "./data/history.db", "file for the history database")
	rootCmd.PersistentFlags().Duration("history-retention", 0, "how long to keep history, for example 720h for 30 days (0 uses the configuration value)")
	rootCmd.PersistentFlags().Duration("history-record-interval", 0, "time between two history samples, for example 5m (0 uses the refresh interval)")

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
