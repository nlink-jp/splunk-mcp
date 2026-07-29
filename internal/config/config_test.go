package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoad_MissingFile(t *testing.T) {
	cfg, err := Load("/nonexistent/path/config.toml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if cfg.Host != "" {
		t.Errorf("expected empty Host, got %q", cfg.Host)
	}
	if cfg.InlineRowThreshold != DefaultInlineRowThreshold {
		t.Errorf("InlineRowThreshold default = %d", cfg.InlineRowThreshold)
	}
}

func TestLoad_BasicFields(t *testing.T) {
	path := writeConfig(t, `
[splunk]
host     = "https://splunk.example.com:8089"
token    = "mytoken"
app      = "search"
owner    = "nobody"
insecure = true
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Host != "https://splunk.example.com:8089" {
		t.Errorf("Host = %q", cfg.Host)
	}
	if cfg.Token != "mytoken" {
		t.Errorf("Token = %q", cfg.Token)
	}
	if cfg.App != "search" {
		t.Errorf("App = %q", cfg.App)
	}
	if !cfg.Insecure {
		t.Error("Insecure should be true")
	}
}

func TestLoad_ServerSection(t *testing.T) {
	path := writeConfig(t, `
[server]
inline_row_threshold = 250
job_ttl              = "10m"
allow_commands       = ["Collect", " outputlookup "]
log_level            = "debug"
log_file             = "/tmp/splunk-mcp.log"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.InlineRowThreshold != 250 {
		t.Errorf("InlineRowThreshold = %d", cfg.InlineRowThreshold)
	}
	if cfg.JobTTL != 10*time.Minute {
		t.Errorf("JobTTL = %v", cfg.JobTTL)
	}
	// allow_commands entries are lower-cased and trimmed.
	if len(cfg.AllowCommands) != 2 || cfg.AllowCommands[0] != "collect" || cfg.AllowCommands[1] != "outputlookup" {
		t.Errorf("AllowCommands = %v", cfg.AllowCommands)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
	if cfg.LogFile != "/tmp/splunk-mcp.log" {
		t.Errorf("LogFile = %q", cfg.LogFile)
	}
}

func TestLoad_ZeroThresholdIsExplicit(t *testing.T) {
	// inline_row_threshold = 0 means "always file-mediate", not "use default".
	path := writeConfig(t, `
[server]
inline_row_threshold = 0
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.InlineRowThreshold != 0 {
		t.Errorf("InlineRowThreshold = %d, want 0", cfg.InlineRowThreshold)
	}
}

func TestLoad_NegativeThreshold(t *testing.T) {
	path := writeConfig(t, `
[server]
inline_row_threshold = -1
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for negative inline_row_threshold")
	}
}

func TestLoad_InvalidJobTTL(t *testing.T) {
	path := writeConfig(t, `
[server]
job_ttl = "notaduration"
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid job_ttl")
	}
}

func TestLoad_HTTPTimeout(t *testing.T) {
	path := writeConfig(t, `
[splunk]
http_timeout = "2m30s"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := 2*time.Minute + 30*time.Second
	if cfg.HTTPTimeout != want {
		t.Errorf("HTTPTimeout = %v, want %v", cfg.HTTPTimeout, want)
	}
}

func TestLoad_InvalidHTTPTimeout(t *testing.T) {
	path := writeConfig(t, `
[splunk]
http_timeout = "notaduration"
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid http_timeout")
	}
}

func TestLoad_WhitespaceTrimmed(t *testing.T) {
	path := writeConfig(t, `
[splunk]
host  = "  https://splunk.example.com:8089  "
token = "  abc123  "
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Host != "https://splunk.example.com:8089" {
		t.Errorf("Host not trimmed: %q", cfg.Host)
	}
	if cfg.Token != "abc123" {
		t.Errorf("Token not trimmed: %q", cfg.Token)
	}
}

func TestLoad_PermissionWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[splunk]\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf strings.Builder
	Stderr = &buf
	t.Cleanup(func() { Stderr = os.Stderr })

	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(buf.String(), "Warning:") {
		t.Errorf("expected permission warning, got: %q", buf.String())
	}
}

func TestApplyEnvVars(t *testing.T) {
	t.Setenv("SPLUNK_HOST", "https://env.example.com:8089")
	t.Setenv("SPLUNK_TOKEN", "envtoken")
	t.Setenv("SPLUNK_USER", "alice")
	t.Setenv("SPLUNK_PASSWORD", "secret")
	t.Setenv("SPLUNK_APP", "myapp")

	cfg := &Config{Host: "original"}
	ApplyEnvVars(cfg)

	if cfg.Host != "https://env.example.com:8089" {
		t.Errorf("Host = %q", cfg.Host)
	}
	if cfg.Token != "envtoken" {
		t.Errorf("Token = %q", cfg.Token)
	}
	if cfg.User != "alice" {
		t.Errorf("User = %q", cfg.User)
	}
	if cfg.Password != "secret" {
		t.Errorf("Password = %q", cfg.Password)
	}
	if cfg.App != "myapp" {
		t.Errorf("App = %q", cfg.App)
	}
}

func TestApplyEnvVars_DoesNotOverrideWithEmpty(t *testing.T) {
	cfg := &Config{Host: "original", Token: "tok"}
	ApplyEnvVars(cfg) // no env vars set
	if cfg.Host != "original" {
		t.Errorf("Host should not be overridden: %q", cfg.Host)
	}
}
