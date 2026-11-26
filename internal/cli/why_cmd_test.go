package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/benefits"
)

// runWhyRoot runs args through the real root command in dir and returns stdout.
func runWhyRoot(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Chdir(dir)
	root := newTestRootCmd(t)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// The report carries every reason in registry order, each with its cited
// benefits expanded and its show command runnable as printed.
func TestBuildWhyReport(t *testing.T) {
	reg, err := benefits.Load()
	if err != nil {
		t.Fatal(err)
	}
	rep, err := buildWhyReport(reg, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Schema != whySchema || len(rep.Reasons) != len(reg.Reasons) {
		t.Fatalf("schema %q with %d reasons; want %q with %d", rep.Schema, len(rep.Reasons), whySchema, len(reg.Reasons))
	}
	for i, rs := range rep.Reasons {
		src := reg.Reasons[i]
		if rs.ID != src.ID || len(rs.Benefits) != len(src.Benefits) {
			t.Errorf("reason %d: got %q with %d benefits, want %q with %d", i, rs.ID, len(rs.Benefits), src.ID, len(src.Benefits))
			continue
		}
		if src.Show != "" && rs.Show != Name()+" "+src.Show {
			t.Errorf("reason %q: show = %q, want the full command", rs.ID, rs.Show)
		}
		for _, b := range rs.Benefits {
			if b.Title == "" || b.Claim == "" || b.Category == "" {
				t.Errorf("reason %q: benefit %q not expanded: %+v", rs.ID, b.ID, b)
			}
			if b.Status == benefits.StatusInProgress {
				t.Errorf("reason %q cites in-progress %q", rs.ID, b.ID)
			}
		}
	}

	one, err := buildWhyReport(reg, reg.Reasons[0].ID)
	if err != nil || len(one.Reasons) != 1 || one.Reasons[0].ID != reg.Reasons[0].ID {
		t.Errorf("one reason: %+v, %v", one.Reasons, err)
	}
	if _, err := buildWhyReport(reg, "no-such-reason"); err == nil || !strings.Contains(err.Error(), reg.Reasons[0].ID) {
		t.Errorf("an unknown reason id: err = %v, want one listing the ids", err)
	}
}

// `forge why` works with no forge.yaml, prints every reason short by
// default, and --verbose adds each thesis and cited claim.
func TestWhyCmdText(t *testing.T) {
	reg, err := benefits.Load()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	short, err := runWhyRoot(t, dir, "why")
	if err != nil {
		t.Fatal(err)
	}
	verbose, err := runWhyRoot(t, dir, "why", "--verbose")
	if err != nil {
		t.Fatal(err)
	}
	first := reg.Reasons[0]
	firstClaim, _ := reg.Benefit(first.Benefits[0])
	for _, rs := range reg.Reasons {
		for name, out := range map[string]string{"short": short, "verbose": verbose} {
			if !strings.Contains(out, rs.Title) || !strings.Contains(out, "id: "+rs.ID) {
				t.Errorf("%s output lacks reason %q", name, rs.ID)
			}
		}
	}
	if !strings.Contains(short, "Backed by: "+first.Benefits[0]) {
		t.Errorf("short output does not list the cited ids:\n%s", short)
	}
	thesisStart := strings.Join(strings.Fields(first.Thesis)[:5], " ")
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if strings.Contains(flat(short), thesisStart) {
		t.Error("short output prints the thesis; it belongs to --verbose")
	}
	if !strings.Contains(flat(verbose), thesisStart) {
		t.Errorf("verbose output lacks the thesis %q", thesisStart)
	}
	if words := strings.Fields(firstClaim.Claim); !strings.Contains(flat(verbose), strings.Join(words[:4], " ")) {
		t.Errorf("verbose output lacks the claim of %q", firstClaim.ID)
	}
	for _, line := range strings.Split(short, "\n") {
		// Wrapped prose stays inside the width; only a single word longer
		// than it may overflow. Headings and the footer are not wrapped.
		if strings.HasPrefix(line, "   ") && len(line) > whyWidth && strings.Contains(strings.TrimSpace(line), " ") {
			t.Errorf("line over %d columns: %q", whyWidth, line)
		}
	}

	one, err := runWhyRoot(t, dir, "why", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(flat(one), thesisStart) || strings.Contains(one, reg.Reasons[1].Title) {
		t.Errorf("`forge why %s` should print that reason in full, alone:\n%s", first.ID, one)
	}
	if _, err := runWhyRoot(t, dir, "why", "no-such-reason"); err == nil {
		t.Error("`forge why no-such-reason` succeeded")
	}
}

// --json and `project benefits --why --json` print the same document, in a
// directory with no forge.yaml.
func TestWhyCmdJSONAndBenefitsWhy(t *testing.T) {
	dir := t.TempDir()
	a, err := runWhyRoot(t, dir, "why", "--json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := runWhyRoot(t, dir, "project", "benefits", "--why", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("`project benefits --why --json` differs from `why --json`")
	}
	var rep whyReport
	if err := json.Unmarshal([]byte(a), &rep); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, a)
	}
	if rep.Schema != whySchema || len(rep.Reasons) == 0 || rep.Reasons[0].Mechanism == "" || len(rep.Reasons[0].Benefits) == 0 {
		t.Errorf("incomplete document: %+v", rep)
	}
	text, err := runWhyRoot(t, dir, "project", "benefits", "--why")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := runWhyRoot(t, dir, "why")
	if err != nil {
		t.Fatal(err)
	}
	if text != plain {
		t.Error("`project benefits --why` differs from `why`")
	}
	if _, err := runWhyRoot(t, dir, "project", "benefits", "--why", "--all"); err == nil {
		t.Error("--why with --all was accepted; they select different lists")
	}
}

// The default `project benefits` output does not change shape because
// reasons exist: no reason text leaks into the benefits list or document.
func TestBenefitsOutputCarriesNoReasons(t *testing.T) {
	reg, err := benefits.Load()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	out, err := runWhyRoot(t, dir, "project", "benefits", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	for k := range doc {
		switch k {
		case "forge_project", "benefits", "more", "next":
		default:
			t.Errorf("`project benefits --json` gained key %q", k)
		}
	}
	if strings.Contains(out, reg.Reasons[0].Mechanism[:30]) {
		t.Error("`project benefits --json` carries reason text")
	}
}

// The MCP why tool is `forge why --json`, for every reason or one.
func TestMCP_Why(t *testing.T) {
	reg, err := benefits.Load()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cs := connectMCP(t, dir)
	out, isErr := callMCP(t, cs, "why", map[string]any{"dir": dir})
	if isErr {
		t.Fatalf("why failed:\n%s", out)
	}
	var rep whyReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("why did not print JSON: %v\n%s", err, out)
	}
	if len(rep.Reasons) != len(reg.Reasons) {
		t.Errorf("%d reasons, want %d", len(rep.Reasons), len(reg.Reasons))
	}
	out, isErr = callMCP(t, cs, "why", map[string]any{"dir": dir, "reason": reg.Reasons[1].ID})
	if isErr {
		t.Fatalf("why reason=%s failed:\n%s", reg.Reasons[1].ID, out)
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil || len(rep.Reasons) != 1 || rep.Reasons[0].ID != reg.Reasons[1].ID {
		t.Errorf("why reason=%s: %v %+v", reg.Reasons[1].ID, err, rep.Reasons)
	}
}
