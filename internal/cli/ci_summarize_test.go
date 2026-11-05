package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSummaryDoc(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The verdict line comes from the F0 envelope; scalars become a field table
// and row lists become tables.
func TestCISummarize_RendersVerdictFieldsAndRows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ok := writeSummaryDoc(t, dir, "wait.json", `{"ok":true,"exit_code":0,"env":"prod","phase":"SUCCEEDED","workloads":[{"name":"api","phase":"ready"}]}`)
	bad := writeSummaryDoc(t, dir, "promote.json", `{"ok":false,"exit_code":3,"error":"promotion_conflict: prod moved\nsecond line","env":"prod"}`)

	var out bytes.Buffer
	if err := runCISummarize([]string{ok, bad}, &out); err != nil {
		t.Fatalf("summarize: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"### wait.json", "✅ ok (exit 0)", "| phase | SUCCEEDED |",
		"**workloads**", "| api | ready |",
		"### promote.json", "❌ exit 3 — promotion_conflict: prod moved",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
	// One table cell per value: an error's second line must not break the row.
	if strings.Contains(got, "second line") {
		t.Errorf("the verdict line must carry only the error's first line:\n%s", got)
	}
}

// It decides nothing: a failed document still exits 0 — the step that wrote
// it already exited with the verdict, and failing here would fail the job a
// second time in a step whose name says nothing about why.
func TestCISummarize_FailedDocumentStillExitsZero(t *testing.T) {
	t.Parallel()
	p := writeSummaryDoc(t, t.TempDir(), "smoke.json", `{"ok":false,"exit_code":1,"error":"2 probes failed"}`)
	if err := runCISummarize([]string{p}, &bytes.Buffer{}); err != nil {
		t.Fatalf("a failed document is content, not a summarize failure: %v", err)
	}
}

// A document with no envelope says so rather than implying a pass.
func TestCISummarize_NoEnvelopeIsNotAPass(t *testing.T) {
	t.Parallel()
	p := writeSummaryDoc(t, t.TempDir(), "x.json", `{"env":"prod"}`)
	var out bytes.Buffer
	if err := runCISummarize([]string{p}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "✅") || !strings.Contains(out.String(), "no ok/exit_code") {
		t.Errorf("an envelope-less document must not read as ok:\n%s", out.String())
	}
}

// It exits non-zero only when an input cannot be READ — and still renders
// the readable ones.
func TestCISummarize_UnreadableInputIsAnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	good := writeSummaryDoc(t, dir, "good.json", `{"ok":true,"exit_code":0}`)
	notJSON := writeSummaryDoc(t, dir, "bad.json", `not json`)
	missing := filepath.Join(dir, "missing.json")

	var out bytes.Buffer
	err := runCISummarize([]string{good, notJSON, missing}, &out)
	if err == nil {
		t.Fatal("unreadable inputs must fail the summarize")
	}
	for _, want := range []string{"bad.json", "missing.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %s: %v", want, err)
		}
	}
	if !strings.Contains(out.String(), "✅ ok") {
		t.Errorf("the readable document must still be rendered:\n%s", out.String())
	}
}

// A pipe in a value stays inside its cell.
func TestMDCell_EscapesPipesAndNewlines(t *testing.T) {
	t.Parallel()
	if got := mdCell("a|b\r\nc"); got != `a\|b c` {
		t.Errorf("mdCell = %q", got)
	}
}
