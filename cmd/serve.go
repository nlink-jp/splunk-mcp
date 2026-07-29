package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/nlink-jp/splunk-mcp/internal/client"
	"github.com/nlink-jp/splunk-mcp/internal/config"
	"github.com/nlink-jp/splunk-mcp/internal/logging"
	"github.com/nlink-jp/splunk-mcp/internal/mcpserver"
	"github.com/nlink-jp/splunk-mcp/internal/tools"
	"github.com/nlink-jp/splunk-mcp/internal/transport"
	"github.com/spf13/cobra"
)

// configPath is bound to a persistent flag on rootCmd so both
// `splunk-mcp serve --config=…` and the bare `splunk-mcp --config=…`
// pick it up.
var configPath string

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP stdio server",
	Long:  "Read JSON-RPC messages from stdin and serve MCP tool calls. This is the default when no subcommand is given.",
	RunE:  runServe,
}

func runServe(cmd *cobra.Command, args []string) error {
	cfg, err := resolveConfig(configPath)
	if err != nil {
		return err
	}
	config.ApplyEnvVars(cfg)
	if cfg.Host == "" {
		return errors.New("no Splunk host configured: set [splunk] host in config.toml or SPLUNK_HOST")
	}

	logger, logFile, err := logging.Setup(cfg.LogLevel, cfg.LogFile)
	if err != nil {
		return err
	}
	if logFile != nil {
		defer logFile.Close()
	}

	c, err := client.New(cfg, logger)
	if err != nil {
		return err
	}

	tr := transport.NewStdioTransport(os.Stdin, os.Stdout)
	srv := mcpserver.New("splunk-mcp", Version, tr, logger)
	tools.Register(srv, c, cfg, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("splunk-mcp serving", "host", cfg.Host, "version", Version)
	if err := srv.Serve(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}

// resolveConfig loads the explicit path if given, then $SPLUNK_MCP_CONFIG,
// then searches standard locations. Returns config defaults when no file is
// found.
func resolveConfig(explicit string) (*config.Config, error) {
	if explicit != "" {
		return config.Load(explicit)
	}
	if env := os.Getenv("SPLUNK_MCP_CONFIG"); env != "" {
		return config.Load(env)
	}
	home, _ := os.UserHomeDir()
	for _, c := range []string{
		filepath.Join(home, ".config", "splunk-mcp", "config.toml"),
		"config.toml",
	} {
		if _, err := os.Stat(c); err == nil {
			return config.Load(c)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("stat %s: %w", c, err)
		}
	}
	return config.Default(), nil
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
