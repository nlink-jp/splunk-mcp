package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
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
// This covers the client half only. The server half — the decoder that refuses
// a typo a client forwarded anyway — is TestEveryToolRefusesAnUnknownArgument.
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

// callTool drives one real tools/call through a server that Register
// populated, and returns the reply's content text and isError flag.
//
// The client is nil on purpose. Every handler decodes its arguments before it
// touches Splunk, so a call this test expects to be refused must come back as
// an error without ever reaching the client — and if that ordering is ever
// broken, the nil dereference says so loudly instead of the test quietly
// asserting the wrong thing. Only calls that must be refused go through here.
func callTool(t *testing.T, name, args string) (string, bool) {
	t.Helper()
	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`+"\n", name, args)
	in := bytes.NewBufferString(req)
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
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode tools/call reply %q: %v", out.String(), err)
	}
	if len(resp.Result.Content) == 0 {
		t.Fatalf("tools/call reply carries no content: %s", out.String())
	}
	return resp.Result.Content[0].Text, resp.Result.IsError
}

// TestEveryToolRefusesAnUnknownArgument is the enforcing half of org ADR-021
// §4. It replaces TestParseArgsAcceptsUnknownFields, which pinned the
// opposite: that a typo reaching parseArgs was accepted and ignored. Closing
// that gap is the deliberate behaviour change that test said would be decided
// on its own, so the test it left behind is inverted here rather than kept
// beside its contradiction.
//
// The tool list comes from the registry, not from contract_test.go's
// hand-written allTools(): a tool added to Register but not to that list would
// otherwise be exempt from this assertion with nothing failing. Every
// registered tool is covered, including the argument-less ones, because
// "no arguments" means none rather than any.
func TestEveryToolRefusesAnUnknownArgument(t *testing.T) {
	tools := registeredTools(t)
	// Vacuity guard: with an empty list this test examines nothing.
	if len(tools) == 0 {
		t.Fatal("tools/list advertises no tools, so this test proves nothing")
	}
	for _, tl := range tools {
		t.Run(tl.Name, func(t *testing.T) {
			const field = "no_such_argument"
			text, isErr := callTool(t, tl.Name, `{"`+field+`":1}`)
			if !isErr {
				t.Fatalf("%s accepted an undeclared argument: %s", tl.Name, text)
			}
			// The message must name the offending field: a caller told only
			// "invalid arguments" has to re-read the schema to find its own
			// typo. Match the decoder's phrasing, not just the field name — a
			// message naming a required argument can contain it by coincidence.
			if want := `unknown field \"` + field + `\"`; !strings.Contains(text, want) {
				t.Errorf("%s: error does not name the offending argument: want %s, got %s", tl.Name, want, text)
			}
			if !strings.Contains(text, "invalid_arguments") {
				t.Errorf("%s: error does not carry the invalid_arguments code: %s", tl.Name, text)
			}
		})
	}
}

// TestMisspelledMaxRowsIsRefused is the specific case that made this change
// worth its breakage. max_rows is the caller's cap on a response, and the
// exact count is what this server exists to give: a dropped `max_rowz` left
// max_rows unset, so the call silently fell back to the configured default
// while reading as though the caller's number had been honoured.
func TestMisspelledMaxRowsIsRefused(t *testing.T) {
	text, isErr := callTool(t, "get_results", `{"sid":"abc","max_rowz":10}`)
	if !isErr {
		t.Fatalf("get_results accepted max_rowz: %s", text)
	}
	if !strings.Contains(text, `unknown field \"max_rowz\"`) {
		t.Errorf("error does not name max_rowz: %s", text)
	}
	// And the correct spelling still binds, which is what makes the rejection
	// above a fix rather than a wall.
	var a getResultsArgs
	if err := parseArgs(json.RawMessage(`{"sid":"abc","max_rows":10}`), &a); err != nil {
		t.Fatalf("the correct spelling was refused: %v", err)
	}
	if a.MaxRows == nil || *a.MaxRows != 10 {
		t.Fatalf("max_rows did not bind: %v", a.MaxRows)
	}
}

// TestMalformedArgumentsAreRefused pins the other half of the decode: a
// wrong-typed argument must be refused rather than left at its zero value,
// which would make the call run as though the argument had been absent.
func TestMalformedArgumentsAreRefused(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args string
	}{
		{"number for string", "get_results", `{"sid":1}`},
		{"string for integer", "get_results", `{"sid":"abc","max_rows":"all"}`},
		{"array for object", "get_results", `["abc"]`},
		{"number for string", "run_query", `{"query":1}`},
		{"string for number", "run_query", `{"query":"search index=x","wait_seconds":"forever"}`},
	}
	for _, tc := range cases {
		t.Run(tc.tool+"/"+tc.name, func(t *testing.T) {
			text, isErr := callTool(t, tc.tool, tc.args)
			if !isErr {
				t.Fatalf("%s accepted malformed arguments: %s", tc.tool, text)
			}
			if !strings.Contains(text, "invalid arguments") {
				t.Errorf("%s: error is not a decode error: %s", tc.tool, text)
			}
		})
	}
}

// TestOmittedArgumentsStillMeanNone pins the boundary of the change: strict
// decoding must not turn a legitimately argument-less call into an error.
// Checked at parseArgs, because a tool that gets past the decode goes on to
// call Splunk.
func TestOmittedArgumentsStillMeanNone(t *testing.T) {
	for _, args := range []string{``, `{}`, `null`} {
		if err := parseArgs(json.RawMessage(args), &struct{}{}); err != nil {
			t.Errorf("arguments %q were refused: %v", args, err)
		}
	}
}
