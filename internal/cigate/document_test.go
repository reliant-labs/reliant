package cigate

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/*.golden.json")

type gateFailedErr struct{ code int }

func (e gateFailedErr) Error() string { return "the CI gate failed (see the summary above)" }
func (e gateFailedErr) ExitCode() int { return e.code }

// The document a check run reads, pinned field for field: every status, the
// counts, a failed check's findings read from its log, and the head.
// Changing this golden is changing forge.ci.gate/v1: only additions are allowed.
func TestDocument_Golden(t *testing.T) {
	root := findingsRoot(t, "internal/orders/orders.go")
	rep := Report{
		Lane: "all", Head: "1a2b3c4", Wall: 95 * time.Second,
		Results: []Result{
			{Name: "verify-generated", Lane: LaneChecks, Status: Pass, Cached: "1a2b3c4", Note: "this tree passed before"},
			{Name: "golangci-lint", Lane: LaneChecks, Status: Fail, Duration: 41200 * time.Millisecond, Queued: 3 * time.Second,
				Note: "exit status 1; log: tmp/ci/all/golangci-lint.log", Log: "tmp/ci/all/golangci-lint.log",
				Tail: "internal/orders/orders.go:23:9: Error return value of `rows.Close` is not checked (errcheck)"},
			{Name: "go-build", Lane: LaneChecks, Status: Pass, Duration: 12 * time.Second},
			{Name: "vuln-go", Lane: LaneChecks, Status: Skip, Allowed: true, Note: "not run"},
			{Name: "license-check", Lane: LaneChecks, Status: Skip, Note: "go-licenses is not on PATH"},
			{Name: "migration-safety", Lane: LaneChecks, Status: NA, Note: "no db/migrations/"},
			{Name: UnitTests, Lane: LaneTest, Status: Fail, Duration: 30 * time.Second, Queued: 1500 * time.Millisecond,
				Note: "exit status 1; full suite: --tier main", Log: "tmp/ci/all/unit-tests.log",
				Tail: "FAIL\texample.com/shop/internal/orders\t0.031s"},
			{Name: "integration-tests", Lane: LaneIntegration, Status: Deferred, Note: "the main tier does not run the integration lane; pr, queue, full do"},
		},
	}
	logs := map[string]string{
		filepath.Join(root, "tmp/ci/all/golangci-lint.log"): "internal/orders/orders.go:23:9: Error return value of `rows.Close` is not checked (errcheck)\n" +
			"\tdefer rows.Close()\n\t      ^\n1 issues:\n* errcheck: 1\n",
		filepath.Join(root, "tmp/ci/all/unit-tests.log"): "--- FAIL: TestOrders (0.00s)\n    orders_test.go:9: want 2 rows\nFAIL\nFAIL\texample.com/shop/internal/orders\t0.031s\n",
	}
	doc := NewDocument(rep, DocumentInput{
		Tier: TierMain, Err: gateFailedErr{1}, Ran: true, Root: root, Module: testModule,
		ReadLog: func(p string) ([]byte, error) {
			if s, ok := logs[p]; ok {
				return []byte(s), nil
			}
			return nil, os.ErrNotExist
		},
	})
	assertGolden(t, "document_fail.golden.json", doc)
}

// A gate that never ran (a bad flag, an unreadable forge.yaml) still prints a
// document: verdict error, no checks, the exit code the process exits with.
func TestDocument_GoldenError(t *testing.T) {
	doc := NewDocument(Report{Lane: "all"}, DocumentInput{Err: errors.New(`unknown tier "nightly": use one of pr, queue, main, full`)})
	assertGolden(t, "document_error.golden.json", doc)
}

// A passing run: ok, exit 0, verdict pass; waived and deferred rows do not
// fail it.
func TestDocument_Pass(t *testing.T) {
	doc := NewDocument(Report{Lane: "test", Results: []Result{
		{Name: UnitTests, Lane: LaneTest, Status: Pass, Duration: time.Second},
		{Name: "vuln-go", Lane: LaneChecks, Status: Skip, Allowed: true},
		{Name: "integration-tests", Lane: LaneIntegration, Status: Deferred},
	}}, DocumentInput{Tier: TierMain, Ran: true})
	if !doc.OK || doc.ExitCode != 0 || doc.Verdict != VerdictPass || doc.Schema != DocumentSchema {
		t.Errorf("head %+v verdict %s", doc.Head, doc.Verdict)
	}
	want := DocumentCounts{Pass: 1, Waived: 1, Deferred: 1}
	if doc.Counts != want {
		t.Errorf("counts %+v, want %+v", doc.Counts, want)
	}
	for _, c := range doc.Checks {
		if c.Blocking || c.Findings == nil {
			t.Errorf("%s: blocking=%v findings=%v", c.Name, c.Blocking, c.Findings)
		}
	}
}

func assertGolden(t *testing.T, name string, doc Document) {
	t.Helper()
	got, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs (run with -update after checking the change is additive):\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}
