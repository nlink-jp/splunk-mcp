package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "splunk-mcp",
	Short: "Splunk REST API MCP server",
	Long: `splunk-mcp exposes Splunk search over the REST API as a local MCP server.

Searches run as asynchronous Splunk jobs (never oneshot/preview), so result
counts are always exact and large result sets are delivered as JSONL files
instead of being truncated.

One server instance connects to one Splunk host; register the binary multiple
times with different --config paths for multiple destinations.

When invoked with no subcommand, behaves like ` + "`splunk-mcp serve`" + ` and reads JSON-RPC messages from stdin.`,
	// Don't dump the usage help on RunE errors; cobra still prints "Error: ..." to stderr.
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServe(cmd, args)
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "",
		"Path to config.toml (default: $SPLUNK_MCP_CONFIG, then ~/.config/splunk-mcp/config.toml, then ./config.toml)")
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		// cobra has already printed "Error: ..." to stderr.
		_ = err
		os.Exit(1)
	}
}
