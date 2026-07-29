package spl

import (
	"strings"
)

// blockedCommands are SPL commands rejected by default because they write,
// delete, or exfiltrate data instead of reading it. Individual commands can
// be re-allowed via the [server] allow_commands config list.
//
// The last line of defense is Splunk-side RBAC; this guard exists so an
// agent's mistake is caught locally before it reaches the server.
var blockedCommands = map[string]bool{
	"delete":         true,
	"collect":        true,
	"mcollect":       true,
	"meventcollect":  true,
	"outputlookup":   true,
	"outputcsv":      true,
	"sendemail":      true,
	"runshellscript": true,
	"script":         true,
}

// CheckSafe scans spl for blocked commands and returns the first blocked
// command found, or "" if the query is safe. allow lists commands (lower-case)
// that are permitted despite being in the blocked set.
//
// The scan is deliberately conservative: the query is split on "|" without
// parsing quotes or subsearches, and the first token of each segment is
// checked. A quoted string containing "| delete" therefore false-positives —
// erring on the side of blocking is acceptable for a guard whose bypass is a
// one-line config change.
func CheckSafe(spl string, allow []string) string {
	allowed := make(map[string]bool, len(allow))
	for _, a := range allow {
		allowed[strings.ToLower(strings.TrimSpace(a))] = true
	}

	for segment := range strings.SplitSeq(spl, "|") {
		cmd := firstToken(segment)
		if cmd == "" {
			continue
		}
		if blockedCommands[cmd] && !allowed[cmd] {
			return cmd
		}
	}
	return ""
}

// firstToken returns the first command-like token of a pipeline segment,
// lower-cased. Leading whitespace and subsearch openers ("[") are skipped so
// that "[ search ... " and "  outputlookup x ]" both resolve correctly.
func firstToken(segment string) string {
	s := strings.TrimLeft(segment, " \t\r\n[")
	if s == "" {
		return ""
	}
	end := strings.IndexAny(s, " \t\r\n]")
	if end == -1 {
		end = len(s)
	}
	return strings.ToLower(s[:end])
}
