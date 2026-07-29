package toolerr_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nlink-jp/splunk-mcp/internal/toolerr"
)

func TestErrorString(t *testing.T) {
	e := toolerr.New(toolerr.CodeUnsafeSPL, "blocked command: delete")
	if got := e.Error(); got != "unsafe_spl: blocked command: delete" {
		t.Errorf("got %q", got)
	}
}

func TestErrorIsByCode(t *testing.T) {
	sentinel := toolerr.New(toolerr.CodeUnsafeSPL, "")
	actual := toolerr.Newf(toolerr.CodeUnsafeSPL, "blocked: %s", "delete")
	if !errors.Is(actual, sentinel) {
		t.Errorf("errors.Is should match by Code")
	}
	other := toolerr.New(toolerr.CodeJobNotDone, "")
	if errors.Is(actual, other) {
		t.Errorf("errors.Is should not match a different Code")
	}
}

func TestErrorWrappedIs(t *testing.T) {
	inner := toolerr.New(toolerr.CodeJobFailed, "dispatch FAILED")
	wrapped := fmt.Errorf("run_query: %w", inner)
	if !errors.Is(wrapped, toolerr.New(toolerr.CodeJobFailed, "")) {
		t.Errorf("errors.Is should walk wrapper chain")
	}
}

func TestErrorJSONMarshal(t *testing.T) {
	e := toolerr.New(toolerr.CodeJobFailed, "boom").WithDetails(map[string]any{
		"sid":   "sid123",
		"state": "FAILED",
	})
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"code":"job_failed"`, `"message":"boom"`, `"sid":"sid123"`} {
		if !strings.Contains(s, want) {
			t.Errorf("marshaled error missing %q: %s", want, s)
		}
	}
}

func TestWithDetailsDoesNotMutate(t *testing.T) {
	e := toolerr.New(toolerr.CodeUnsafeSPL, "x")
	_ = e.WithDetails(map[string]any{"k": "v"})
	if e.Details != nil {
		t.Errorf("WithDetails should not mutate receiver")
	}
}
