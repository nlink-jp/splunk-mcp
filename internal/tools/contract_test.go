package tools

import (
	"strings"
	"testing"
)

// A tool description and the usage manual are the only places the model
// learns what this server does, so a mechanism that was withdrawn has to
// disappear from them as completely as it disappeared from the code. It did
// not the first time: v0.2.0 removed the result file (ADR-0004) but shipped a
// run_query description telling the model that large results are written as
// JSONL under workspace_root, and a get_usage error table telling it to retry
// with workspace_root and raise inline_row_threshold — advice the server now
// rejects. Prose drifts silently because nothing compiles it; this test is
// what compiles it.
var retiredTerms = []string{
	"workspace_root",
	"workspaceRoot",
	"workspace_dir",
	"inline_row_threshold",
	"results_file",
	"workspace_required",
	"workspace_error",
	"JSONL",
	"file-mediat", // file-mediated / file-mediation
}

// modelFacingText is every string this server puts in front of the model.
func modelFacingText() map[string]string {
	out := map[string]string{
		"get_usage manual": usageTemplate,
	}
	for _, tl := range []struct {
		name string
		desc string
		sch  string
	}{
		{"run_query", runQueryTool.Description, string(runQueryTool.InputSchema)},
		{"start_query", startQueryTool.Description, string(startQueryTool.InputSchema)},
		{"check_job", checkJobTool.Description, string(checkJobTool.InputSchema)},
		{"get_results", getResultsTool.Description, string(getResultsTool.InputSchema)},
		{"cancel_job", cancelJobTool.Description, string(cancelJobTool.InputSchema)},
		{"list_indexes", listIndexesTool.Description, string(listIndexesTool.InputSchema)},
		{"list_sourcetypes", listSourcetypesTool.Description, string(listSourcetypesTool.InputSchema)},
		{"list_saved_searches", listSavedSearchesTool.Description, string(listSavedSearchesTool.InputSchema)},
		{"run_saved_search", runSavedSearchTool.Description, string(runSavedSearchTool.InputSchema)},
		{"get_usage", getUsageTool.Description, string(getUsageTool.InputSchema)},
	} {
		out[tl.name+" description"] = tl.desc
		out[tl.name+" schema"] = tl.sch
	}
	return out
}

func TestModelFacingTextNamesNoRetiredMechanism(t *testing.T) {
	for where, text := range modelFacingText() {
		for _, term := range retiredTerms {
			if strings.Contains(text, term) {
				t.Errorf("%s still names %q: this server writes no result files "+
					"(ADR-0004) — rows come back capped by max_rows and the drop is counted",
					where, term)
			}
		}
	}
}

// The cap is the replacement mechanism, so the model has to be told about it
// wherever it used to be told about the file.
func TestResultToolsDocumentTheCap(t *testing.T) {
	for name, sch := range map[string]string{
		"run_query":        string(runQueryTool.InputSchema),
		"get_results":      string(getResultsTool.InputSchema),
		"run_saved_search": string(runSavedSearchTool.InputSchema),
	} {
		if !strings.Contains(sch, "max_rows") {
			t.Errorf("%s schema does not offer max_rows", name)
		}
		if !strings.Contains(sch, "omitted_rows") {
			t.Errorf("%s schema does not say what happens to the rows past the cap", name)
		}
	}
	if !strings.Contains(usageTemplate, "max_rows") {
		t.Error("the usage manual does not mention max_rows")
	}
}
