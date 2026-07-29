package tools

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nlink-jp/splunk-mcp/internal/toolerr"
)

// writeJSONL writes rows as JSON Lines under workspaceRoot and returns the
// file path. workspaceRoot must be an absolute path; it is created if missing.
func writeJSONL(workspaceRoot, sid string, rows []json.RawMessage) (string, error) {
	if workspaceRoot == "" {
		return "", toolerr.New(toolerr.CodeWorkspaceRequired, "workspace_root is required for file-mediated results")
	}
	if !filepath.IsAbs(workspaceRoot) {
		return "", toolerr.Newf(toolerr.CodeInvalidArguments, "workspace_root must be an absolute path, got %q", workspaceRoot)
	}
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		return "", toolerr.Newf(toolerr.CodeWorkspaceError, "create workspace_root: %v", err)
	}

	name := fmt.Sprintf("splunk_%s_%d.jsonl", sanitizeSID(sid), time.Now().UnixMilli())
	path := filepath.Join(workspaceRoot, name)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return "", toolerr.Newf(toolerr.CodeWorkspaceError, "create results file: %v", err)
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriter(f)
	for _, row := range rows {
		if _, err := w.Write(row); err != nil {
			return "", toolerr.Newf(toolerr.CodeWorkspaceError, "write results file: %v", err)
		}
		if err := w.WriteByte('\n'); err != nil {
			return "", toolerr.Newf(toolerr.CodeWorkspaceError, "write results file: %v", err)
		}
	}
	if err := w.Flush(); err != nil {
		return "", toolerr.Newf(toolerr.CodeWorkspaceError, "flush results file: %v", err)
	}
	return path, nil
}

// sanitizeSID keeps SID characters safe for a file name.
func sanitizeSID(sid string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, sid)
}
