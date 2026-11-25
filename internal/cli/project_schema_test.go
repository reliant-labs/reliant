package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/schemagen"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// The test record's documents are published from their pkg/testreport types,
// and a document those types write validates against the published schema.
func TestProjectSchema_TestDocuments(t *testing.T) {
	id := testreport.TestID{Lang: testreport.LangGo, Package: "example.com/p", Name: "TestA"}
	docs := map[string]any{
		"test-results": &testreport.Results{
			Head:  testreport.Head{Schema: testreport.SchemaResults, OK: true},
			Cases: []testreport.Case{{ID: id, Status: testreport.StatusPass, Attempts: 1}},
		},
		"test-coverage": &testreport.Coverage{
			Head:  testreport.Head{Schema: testreport.SchemaCoverage, OK: true},
			Lanes: []testreport.LaneInfo{{Name: testreport.LaneUnit, Mode: testreport.ModeCount}},
			Files: []testreport.CoverageFile{{Lang: testreport.LangGo, Path: "a.go", Blocks: []testreport.Block{{StartLine: 1, EndLine: 2, Statements: 1, Hits: []int64{3}}}}},
		},
		"test-report": testreport.NewReport(testreport.ReportInput{Runs: []testreport.RunRef{{ID: "r", Lane: testreport.LaneUnit}}}),
		"test-flakes": &testreport.Flakes{
			Head:  testreport.Head{Schema: testreport.SchemaFlakes, OK: true},
			Tests: []testreport.FlakyTest{{ID: id, State: testreport.StateSuspected, Events: []testreport.FlakeEvent{{Test: id, Verdict: testreport.VerdictConfirmed}}}},
		},
	}
	for name, doc := range docs {
		var out bytes.Buffer
		if err := runProjectSchema(&out, name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		compiled, err := schemagen.Compile(out.Bytes())
		if err != nil {
			t.Fatalf("%s: compile: %v", name, err)
		}
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		v, err := schemagen.DecodeJSON(b)
		if err != nil {
			t.Fatal(err)
		}
		if errs := compiled.Validate(v); len(errs) > 0 {
			t.Errorf("%s: a document the types wrote does not validate: %v", name, errs)
		}
	}
	var list bytes.Buffer
	if err := writeSchemaList(&list); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"env", "forge.test.results/v1", "forge.test.coverage/v1", "forge.test.report/v1", "forge.test.flakes/v1"} {
		if !strings.Contains(list.String(), want) {
			t.Errorf("schema list lacks %s:\n%s", want, list.String())
		}
	}
}
