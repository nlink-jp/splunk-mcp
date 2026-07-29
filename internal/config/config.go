// Package config loads splunk-mcp runtime configuration.
//
// One MCP server instance connects to exactly one Splunk host. Multiple
// destinations are handled by registering the server multiple times with
// different --config paths — there is deliberately no profile mechanism.
//
// Ported from nlink-jp/splunk-cli internal/config with an added [server]
// section for MCP-specific settings.
package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/nlink-jp/splunk-mcp/internal/spl"
)

// Stderr is the writer for warnings. Overridable in tests.
var Stderr io.Writer = os.Stderr

// DefaultInlineRowThreshold is the row count above which results are written
// to a workspace file instead of returned inline.
const DefaultInlineRowThreshold = 100

// Config holds all runtime configuration for splunk-mcp.
// Fields are populated in priority order: config file → env vars.
type Config struct {
	// [splunk] — connection settings (same shape as splunk-cli).
	Host        string
	Token       string
	User        string
	Password    string
	App         string
	Owner       string
	Insecure    bool
	HTTPTimeout time.Duration
	Debug       bool
	Prepend     spl.PrependMode

	// [server] — MCP-specific settings.
	InlineRowThreshold int
	JobTTL             time.Duration
	AllowCommands      []string
	LogLevel           string
	LogFile            string
}

// tomlConfig mirrors the TOML file structure.
type tomlConfig struct {
	Splunk tomlSplunk `toml:"splunk"`
	Server tomlServer `toml:"server"`
}

type tomlSplunk struct {
	Host        string `toml:"host"`
	Token       string `toml:"token"`
	User        string `toml:"user"`
	Password    string `toml:"password"`
	App         string `toml:"app"`
	Owner       string `toml:"owner"`
	Insecure    bool   `toml:"insecure"`
	HTTPTimeout string `toml:"http_timeout"`
	Prepend     string `toml:"prepend"`
}

type tomlServer struct {
	InlineRowThreshold *int     `toml:"inline_row_threshold"`
	JobTTL             string   `toml:"job_ttl"`
	AllowCommands      []string `toml:"allow_commands"`
	LogLevel           string   `toml:"log_level"`
	LogFile            string   `toml:"log_file"`
}

// DefaultPath returns the default config file path.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "splunk-mcp", "config.toml")
}

// Default returns a Config with all defaults and no connection settings.
func Default() *Config {
	return &Config{
		Prepend:            spl.DefaultMode,
		InlineRowThreshold: DefaultInlineRowThreshold,
	}
}

// Load reads the config file at path (or the default path if empty).
// A missing file is not an error — the returned Config has defaults only.
func Load(path string) (*Config, error) {
	cfg := Default()

	if path == "" {
		path = DefaultPath()
	}

	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("config: stat %s: %w", path, err)
	}

	checkPermissions(path, info)

	var raw tomlConfig
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return cfg, fmt.Errorf("config: parse %s: %w", path, err)
	}

	s := raw.Splunk
	cfg.Host = strings.TrimSpace(s.Host)
	cfg.Token = strings.TrimSpace(s.Token)
	cfg.User = strings.TrimSpace(s.User)
	cfg.Password = strings.TrimSpace(s.Password)
	cfg.App = strings.TrimSpace(s.App)
	cfg.Owner = strings.TrimSpace(s.Owner)
	cfg.Insecure = s.Insecure

	if s.HTTPTimeout != "" {
		d, err := time.ParseDuration(s.HTTPTimeout)
		if err != nil {
			return cfg, fmt.Errorf("config: invalid http_timeout %q: %w", s.HTTPTimeout, err)
		}
		cfg.HTTPTimeout = d
	}

	mode, err := spl.ParseMode(s.Prepend)
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	cfg.Prepend = mode

	sv := raw.Server
	if sv.InlineRowThreshold != nil {
		if *sv.InlineRowThreshold < 0 {
			return cfg, fmt.Errorf("config: inline_row_threshold must be >= 0, got %d", *sv.InlineRowThreshold)
		}
		cfg.InlineRowThreshold = *sv.InlineRowThreshold
	}
	if sv.JobTTL != "" {
		d, err := time.ParseDuration(sv.JobTTL)
		if err != nil {
			return cfg, fmt.Errorf("config: invalid job_ttl %q: %w", sv.JobTTL, err)
		}
		cfg.JobTTL = d
	}
	for _, c := range sv.AllowCommands {
		c = strings.ToLower(strings.TrimSpace(c))
		if c != "" {
			cfg.AllowCommands = append(cfg.AllowCommands, c)
		}
	}
	cfg.LogLevel = strings.TrimSpace(sv.LogLevel)
	cfg.LogFile = strings.TrimSpace(sv.LogFile)

	return cfg, nil
}

// ApplyEnvVars overrides cfg fields with values from environment variables.
// Uses the same variable names as splunk-cli so credentials can be shared.
func ApplyEnvVars(cfg *Config) {
	if v := os.Getenv("SPLUNK_HOST"); v != "" {
		cfg.Host = v
	}
	if v := os.Getenv("SPLUNK_TOKEN"); v != "" {
		cfg.Token = v
	}
	if v := os.Getenv("SPLUNK_USER"); v != "" {
		cfg.User = v
	}
	if v := os.Getenv("SPLUNK_PASSWORD"); v != "" {
		cfg.Password = v
	}
	if v := os.Getenv("SPLUNK_APP"); v != "" {
		cfg.App = v
	}
}

func checkPermissions(path string, info os.FileInfo) {
	// NTFS does not support Unix permission bits; reported mode is always
	// 0666 regardless of ACL settings, making this check meaningless.
	// On Windows, users should restrict access via NTFS ACLs instead.
	if runtime.GOOS == "windows" {
		return
	}
	if info.Mode().Perm()&0077 != 0 {
		_, _ = fmt.Fprintf(Stderr,
			"Warning: config file %s has permissions %#o; expected 0600.\n"+
				"  The file may contain credentials. Run: chmod 600 %s\n",
			path, info.Mode().Perm(), path,
		)
	}
}
