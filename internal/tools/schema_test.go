package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nlink-jp/splunk-mcp/internal/config"
	"github.com/nlink-jp/splunk-mcp/internal/mcpserver"
	"github.com/nlink-jp/splunk-mcp/internal/transport"
)

// listedTool is one entry of a tools/list reply.
type listedTool struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"inputSchema"`
}

// registeredTools drives a real tools/list through a server that Register
// populated, so what comes back is the production registry itself rather than
// a second copy of it.
//
// That distinction is the point: contract_test.go's allTools() is a
// hand-written list, and a tool added to Register but not to allTools() would
// be exempt from every assertion over there with nothing failing.
// TestContractToolListMatchesTheRegistry below pins the two together.
//
// A nil *client.Client is enough here — registration only records the
// descriptors, and tools/list never enters a handler.
func registeredTools(t *testing.T) []listedTool {
	t.Helper()
	in := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := mcpserver.New("splunk-mcp", "test", transport.NewStdioTransport(in, &out), logger)
	Register(srv, nil, config.Default(), logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Serve(ctx); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var resp struct {
		Result struct {
			Tools []listedTool `json:"tools"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode tools/list reply %q: %v", out.String(), err)
	}
	if resp.Error != nil {
		t.Fatalf("tools/list returned an error: %s", resp.Error.Message)
	}
	return resp.Result.Tools
}

// TestEveryToolSchemaIsClosed is the arch test organization ADR-021 §10
// requires: every registered tool's input schema sets
// additionalProperties:false, so a client validating arguments against the
// schema refuses a mistyped parameter instead of sending it on.
//
// This covers the client half only. The server half is not in place here;
// TestParseArgsAcceptsUnknownFields records that.
func TestEveryToolSchemaIsClosed(t *testing.T) {
	tools := registeredTools(t)
	// Vacuity guard: with an empty list every assertion below passes without
	// having examined anything.
	if len(tools) == 0 {
		t.Fatal("tools/list advertises no tools, so this test proves nothing")
	}
	for _, tl := range tools {
		var schema struct {
			Type                 string `json:"type"`
			AdditionalProperties *bool  `json:"additionalProperties"`
		}
		if err := json.Unmarshal(tl.Schema, &schema); err != nil {
			t.Errorf("tool %q: input schema is not valid JSON: %v", tl.Name, err)
			continue
		}
		if schema.Type != "object" {
			t.Errorf("tool %q: schema type = %q, want object", tl.Name, schema.Type)
		}
		if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
			got := "absent"
			if schema.AdditionalProperties != nil {
				got = "true"
			}
			t.Errorf("tool %q: input schema does not set additionalProperties:false "+
				"(%s) — a validating client would pass an agent's mistyped argument "+
				"through unnoticed (organization ADR-021 §10)", tl.Name, got)
		}
	}
}

// TestContractToolListMatchesTheRegistry closes the divergence that made
// registeredTools necessary. contract_test.go asserts schema shape over a
// hand-written allTools(); if that list and Register disagree, the assertions
// there quietly cover the wrong set. Neither direction is allowed to drift.
func TestContractToolListMatchesTheRegistry(t *testing.T) {
	registered := map[string]bool{}
	for _, tl := range registeredTools(t) {
		registered[tl.Name] = true
	}
	if len(registered) == 0 {
		t.Fatal("the registry is empty, so this test proves nothing")
	}
	handWritten := map[string]bool{}
	for _, tl := range allTools() {
		handWritten[tl.name] = true
	}
	for name := range registered {
		if !handWritten[name] {
			t.Errorf("tool %q is registered but absent from contract_test.go's "+
				"allTools(): every assertion there skips it silently", name)
		}
	}
	for name := range handWritten {
		if !registered[name] {
			t.Errorf("contract_test.go's allTools() names %q, which Register does "+
				"not register", name)
		}
	}
}

// TestParseArgsAcceptsUnknownFields records the half of the contract this
// server does NOT have, so the gap is visible in code and not only in a
// report.
//
// parseArgs decodes with plain json.Unmarshal, without
// DisallowUnknownFields, so an unknown argument is silently ignored rather
// than named in an error. The closed schemas above bind clients that validate
// against them; a client that does not validate — or any caller speaking
// JSON-RPC directly — still gets its typo accepted, and a misspelled max_rows
// then falls back to the configured default while looking like it took effect,
// which is exactly the cap this server exists to make explicit.
//
// Closing that gap changes what existing callers get back (a silently-ignored
// argument becomes an error), so it is a deliberate behaviour change and not
// part of the schema sweep. When it is made, this test should fail and be
// replaced by its opposite.
func TestParseArgsAcceptsUnknownFields(t *testing.T) {
	var a getResultsArgs
	if err := parseArgs(json.RawMessage(`{"sid":"abc","max_rowz":10}`), &a); err != nil {
		t.Fatalf("expected the unknown field to be ignored, got an error: %v", err)
	}
	if a.SID != "abc" {
		t.Fatalf("sid = %q, want abc", a.SID)
	}
	if a.MaxRows != nil {
		t.Fatalf("max_rows = %v, want nil: the misspelling should not have bound", *a.MaxRows)
	}
	t.Log("unknown field 'max_rowz' was accepted and ignored, and max_rows " +
		"stayed unset: the closed schema binds validating clients only, not parseArgs")
}
