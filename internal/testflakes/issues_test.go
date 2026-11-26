package testflakes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testreport"
)

// fakeGH serves issue lists and records every write.
type fakeGH struct {
	open   string            // `issue list --state open` JSON
	search map[string]string // test name -> `issue list --state all` JSON
	writes []string
}

func (f *fakeGH) run(_ context.Context, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(joined, "issue list") && hasArg(args, "open"):
		return []byte(f.open), nil
	case strings.HasPrefix(joined, "issue list"):
		for name, js := range f.search {
			if strings.Contains(argAfter(args, "--search"), name) {
				return []byte(js), nil
			}
		}
		return []byte("[]"), nil
	case strings.HasPrefix(joined, "label create"):
		return nil, nil
	case strings.HasPrefix(joined, "issue create"):
		f.writes = append(f.writes, joined)
		return []byte("https://github.com/acme/m/issues/99\n"), nil
	}
	f.writes = append(f.writes, joined)
	return nil, nil
}

func flaky(name string, st testreport.State, runs ...string) testreport.FlakyTest {
	ft := testreport.FlakyTest{ID: testreport.TestID{Lang: testreport.LangGo, Package: testModule + "/internal/a", Name: name}, State: st, N: 10}
	for i, r := range runs {
		ft.Events = append(ft.Events, testreport.FlakeEvent{Test: ft.ID, Run: r, Commit: "c" + r, Lane: "unit", Signal: testreport.SignalRetryPass, Verdict: testreport.VerdictConfirmed, Signature: "sig", At: day0.Add(time.Duration(i) * time.Hour)})
	}
	return ft
}

// healed is a test that was suspected and went quiet: healthy, with the time
// it healed, as ComputeFlakes reports it.
func healed(name string, runs ...string) testreport.FlakyTest {
	ft := flaky(name, testreport.StateHealthy, runs...)
	since := day0.Add(15 * 24 * time.Hour)
	ft.Since = &since
	return ft
}

func TestSyncIssues(t *testing.T) {
	root := newModule(t, map[string]string{
		"internal/a/a_test.go": "package a\n\nfunc TestNew(t *testing.T) {\n\tif x() {\n\t\tt.Fatal(\"boom\")\n\t}\n}\n\nfunc TestOther(t *testing.T) {}\n",
	})
	gh := &fakeGH{
		open: `[{"number":7,"title":"flake:suspected: TestHealed (internal/a)","state":"OPEN","url":"https://github.com/acme/m/issues/7"},
		        {"number":8,"title":"flake:suspected: TestUnknown (internal/a)","state":"OPEN","url":"https://github.com/acme/m/issues/8"}]`,
		search: map[string]string{
			"TestKnown": `[{"number":5,"title":"flake:suspected: TestKnown (internal/a)","state":"CLOSED","url":"https://github.com/acme/m/issues/5","body":"run ` + "`r1`" + `","comments":[]}]`,
		},
	}
	doc := &testreport.Flakes{Tests: []testreport.FlakyTest{
		flaky("TestNew", testreport.StateSuspected, "r1"),
		flaky("TestKnown", testreport.StateFlaky, "r1", "r2"),
		healed("TestHealed", "r0"),
	}}
	out := &bytes.Buffer{}
	res := SyncIssues(context.Background(), IssueOptions{Root: root, Repo: "acme/m", GH: gh.run, Stdout: out, Getenv: noEnv}, doc)

	writes := strings.Join(gh.writes, "\n")
	if len(res.Opened) != 1 || !strings.Contains(writes, "issue create -R acme/m --title flake:suspected: TestNew (internal/a) --label flake:suspected") {
		t.Errorf("open: %v\n%s", res.Opened, writes)
	}
	// The body carries the test's source, the signature and the runs.
	for _, want := range []string{"func TestNew(t *testing.T) {", "t.Fatal(\"boom\")", "`sig`", "run `r1`"} {
		if !strings.Contains(writes, want) {
			t.Errorf("issue body lacks %q", want)
		}
	}
	if strings.Contains(writes, "func TestOther") {
		t.Error("the body must stop at the test's closing brace")
	}
	// The closed issue is reopened, and only the run it does not mention is
	// added.
	if !strings.Contains(writes, "issue reopen 5 -R acme/m") || !strings.Contains(writes, "issue comment 5") ||
		!strings.Contains(writes, "run `r2`") {
		t.Errorf("update:\n%s", writes)
	}
	for _, w := range gh.writes {
		if strings.HasPrefix(w, "issue comment 5") && strings.Contains(w, "run `r1`") {
			t.Errorf("an already-mentioned run is not repeated: %s", w)
		}
	}
	// A test that left the state has its issue closed; one the record does
	// not know is left alone.
	if !strings.Contains(writes, "issue close 7 -R acme/m --reason not planned") || strings.Contains(writes, "issue close 8") {
		t.Errorf("close:\n%s", writes)
	}
	if res.URLs[doc.Tests[0].ID.String()] != "https://github.com/acme/m/issues/99" {
		t.Errorf("urls = %v", res.URLs)
	}
}

func TestSyncIssuesDryRunWritesNothing(t *testing.T) {
	gh := &fakeGH{open: `[{"number":7,"title":"flake:suspected: TestHealed (internal/a)","state":"OPEN"}]`}
	doc := &testreport.Flakes{Tests: []testreport.FlakyTest{flaky("TestNew", testreport.StateSuspected, "r1"), flaky("TestHealed", testreport.StateFixed, "r0")}}
	res := SyncIssues(context.Background(), IssueOptions{Root: newModule(t, nil), Repo: "acme/m", GH: gh.run, Stdout: &bytes.Buffer{}, DryRun: true}, doc)
	if len(gh.writes) != 0 || len(res.Messages) != 2 {
		t.Errorf("writes %v, messages %v", gh.writes, res.Messages)
	}
}

// A test that failed again when rerun (real) is not a flake: the record gives
// it no issue, and the open issue an earlier flake of it filed is kept, not
// closed as "has not flaked again".
func TestSyncIssuesLeavesARealFailureAlone(t *testing.T) {
	root := newModule(t, map[string]string{"internal/a/a_test.go": "package a\n\nfunc TestReal(t *testing.T) {}\n"})
	pkg := testModule + "/internal/a"
	fail := ev("fail", pkg, "", "")
	addRun(t, root, "c1", "t1", day0, "unit", failRun(pkg, "TestReal", "no\n")+fail, 1)
	addRun(t, root, "c1", "t1", day0.Add(time.Hour), LaneConfirm, failRun(pkg, "TestReal", "no again\n")+fail, 1)
	doc, warnings := ComputeFlakes(FlakesOptions{Root: root, Now: day0.Add(2 * time.Hour), States: []testreport.State{
		testreport.StateHealthy, testreport.StateSuspected, testreport.StateFlaky,
		testreport.StateFixed, testreport.StateQuarantined, testreport.StateRemoved,
	}})
	if len(warnings) > 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if got := stateOf(doc, "TestReal"); got != testreport.StateHealthy {
		t.Fatalf("TestReal: %q, want healthy (real failures only)", got)
	}
	gh := &fakeGH{open: `[{"number":4,"title":"flake:suspected: TestReal (internal/a)","state":"OPEN","url":"https://github.com/acme/m/issues/4"}]`}
	res := SyncIssues(context.Background(), IssueOptions{Root: root, Repo: "acme/m", GH: gh.run, Stdout: &bytes.Buffer{}, Getenv: noEnv}, doc)
	if len(gh.writes) != 0 || len(res.Opened)+len(res.Closed)+len(res.Updated) != 0 {
		t.Errorf("a real failure must neither open nor close an issue: writes %v, result %+v", gh.writes, res)
	}
}

// openIssue is one entry of an `issue list --json number,title,…,createdAt,comments`
// answer, comments given as body/createdAt pairs.
func openIssue(num int, test string, created time.Time, comments ...any) string {
	var cs []string
	for i := 0; i+1 < len(comments); i += 2 {
		body, _ := json.Marshal(comments[i].(string))
		cs = append(cs, fmt.Sprintf(`{"body":%s,"createdAt":%q}`, body, comments[i+1].(time.Time).UTC().Format(time.RFC3339)))
	}
	return fmt.Sprintf(`{"number":%d,"title":"flake:suspected: %s (internal/a)","state":"OPEN","url":"https://github.com/acme/m/issues/%d","createdAt":%q,"comments":[%s]}`,
		num, test, num, created.UTC().Format(time.RFC3339), strings.Join(cs, ","))
}

// The sweep reads the evidence from the issues alone, so it closes stale
// issues on a CI runner whose local record is empty.
func TestSyncIssuesStaleAfter(t *testing.T) {
	now := day0.Add(30 * 24 * time.Hour)
	days := func(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }
	evidence := "Failed then passed on retry again:\n\n- run `r9`"
	tree := map[string]string{"internal/a/a_test.go": "package a\n\nfunc TestA(t *testing.T) {}\n"}
	for _, tc := range []struct {
		name       string
		issue      string
		staleAfter time.Duration
		dryRun     bool
		record     *testreport.Flakes // nil: computed from the empty local record
		search     map[string]string
		wantClose  string // a substring of the close comment; "" means nothing is closed
		wantSay    string
		extra      func(t *testing.T, root string)
	}{
		{name: "stale is closed", issue: openIssue(1, "TestA", days(10)), staleAfter: 7 * 24 * time.Hour,
			wantClose: "it has not flaked again for 10 days (last seen " + days(10).Format("2006-01-02") + "), past --stale-after 7d. A new flake of this test reopens it."},
		{name: "fresh is kept", issue: openIssue(1, "TestA", days(3)), staleAfter: 7 * 24 * time.Hour},
		{name: "a forge evidence comment resets the age", issue: openIssue(1, "TestA", days(20), evidence, days(2)), staleAfter: 7 * 24 * time.Hour},
		{name: "an old forge evidence comment does not", issue: openIssue(1, "TestA", days(20), evidence, days(9)), staleAfter: 7 * 24 * time.Hour,
			wantClose: "for 9 days"},
		{name: "other comments do not reset the age", issue: openIssue(1, "TestA", days(20), "looking into it", days(1)), staleAfter: 7 * 24 * time.Hour,
			wantClose: "for 20 days"},
		{name: "a gone test is closed however fresh", issue: openIssue(1, "TestGone", days(1)), staleAfter: 7 * 24 * time.Hour,
			wantClose: "`TestGone` no longer exists in `internal/a`"},
		{name: "a missing package dir is indeterminate", issue: strings.Replace(openIssue(1, "TestGone", days(1)), "(internal/a)", "(internal/missing)", 1), staleAfter: 7 * 24 * time.Hour},
		{name: "an unreadable test file is indeterminate", issue: openIssue(1, "TestGone", days(1)), staleAfter: 7 * 24 * time.Hour,
			extra: func(t *testing.T, root string) {
				// A directory under a test file's name: the glob sees it, reading fails (as root too).
				if err := os.Mkdir(filepath.Join(root, "internal/a/b_test.go"), 0o755); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "an unparsable test file is indeterminate", issue: openIssue(1, "TestGone", days(1)), staleAfter: 7 * 24 * time.Hour,
			extra: func(t *testing.T, root string) {
				write(t, filepath.Join(root, "internal/a/b_test.go"), "package a\n\nfunc helper( {\n")
			}},
		{name: "a hours duration", issue: openIssue(1, "TestA", now.Add(-60*time.Hour)), staleAfter: 50 * time.Hour,
			wantClose: "past --stale-after 50h0m0s"},
		{name: "dry-run closes nothing", issue: openIssue(1, "TestA", days(10)), staleAfter: 7 * 24 * time.Hour, dryRun: true,
			wantSay: "--dry-run: would close #1: it has not flaked again for 10 days"},
		{name: "no --stale-after sweeps nothing", issue: openIssue(1, "TestA", days(10))},
		{name: "a hand-filed issue is left alone", staleAfter: 7 * 24 * time.Hour,
			issue: fmt.Sprintf(`{"number":1,"title":"flaky CI on main","state":"OPEN","createdAt":%q,"comments":[]}`, days(60).Format(time.RFC3339))},
		{name: "a test the record still calls a flake is left to it", issue: openIssue(1, "TestA", days(10)), staleAfter: 7 * 24 * time.Hour,
			record: &testreport.Flakes{Tests: []testreport.FlakyTest{flaky("TestA", testreport.StateSuspected, "r1")}},
			search: map[string]string{"TestA": `[{"number":1,"title":"flake:suspected: TestA (internal/a)","state":"OPEN","url":"u","body":"run ` + "`r1`" + `","comments":[]}]`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newModule(t, tree)
			if tc.extra != nil {
				tc.extra(t, root)
			}
			doc := tc.record
			if doc == nil {
				var warnings []string
				doc, warnings = ComputeFlakes(FlakesOptions{Root: root, Now: now, States: []testreport.State{
					testreport.StateHealthy, testreport.StateSuspected, testreport.StateFlaky,
					testreport.StateFixed, testreport.StateQuarantined, testreport.StateRemoved,
				}})
				if len(warnings) > 0 || len(doc.Tests) != 0 {
					t.Fatalf("the record must be empty: %v %v", doc.Tests, warnings)
				}
			}
			gh := &fakeGH{open: "[" + tc.issue + "]", search: tc.search}
			out := &bytes.Buffer{}
			res := SyncIssues(context.Background(), IssueOptions{Root: root, Repo: "acme/m", GH: gh.run, Stdout: out, Getenv: noEnv,
				DryRun: tc.dryRun, StaleAfter: tc.staleAfter, Now: now}, doc)
			writes := strings.Join(gh.writes, "\n")
			if tc.wantClose == "" {
				if len(gh.writes) != 0 || len(res.Closed) != 0 {
					t.Errorf("nothing may be written: %s", writes)
				}
			} else if len(gh.writes) != 1 || !strings.HasPrefix(writes, "issue close 1 -R acme/m --reason not planned --comment Closed by `forge test flakes --issues`: ") ||
				!strings.Contains(writes, tc.wantClose) || len(res.Closed) != 1 {
				t.Errorf("want one close carrying %q, got:\n%s", tc.wantClose, writes)
			}
			if tc.wantSay != "" && !strings.Contains(out.String(), tc.wantSay) {
				t.Errorf("output lacks %q:\n%s", tc.wantSay, out.String())
			}
		})
	}
}

// The sweep needs the issues' dates, so only it asks gh for them.
func TestSyncIssuesAsksForDatesOnlyToSweep(t *testing.T) {
	for _, stale := range []time.Duration{0, time.Hour} {
		var listed string
		gh := func(_ context.Context, args ...string) ([]byte, error) {
			if hasArg(args, "open") {
				listed = argAfter(args, "--json")
			}
			return []byte("[]"), nil
		}
		SyncIssues(context.Background(), IssueOptions{Root: newModule(t, nil), Repo: "acme/m", GH: gh, Stdout: &bytes.Buffer{}, StaleAfter: stale}, &testreport.Flakes{})
		if got := strings.Contains(listed, "createdAt,comments"); got != (stale > 0) {
			t.Errorf("stale-after %v: listed fields %q", stale, listed)
		}
	}
}

func TestParseStaleAfter(t *testing.T) {
	for in, want := range map[string]time.Duration{"7d": 7 * 24 * time.Hour, "168h": 168 * time.Hour, " 1d ": 24 * time.Hour, "90m": 90 * time.Minute} {
		if got, err := ParseStaleAfter(in); err != nil || got != want {
			t.Errorf("%q: %v %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0d", "-1d", "1.5d", "seven", "0s"} {
		if _, err := ParseStaleAfter(in); err == nil {
			t.Errorf("%q must be refused", in)
		}
	}
	if FormatStaleAfter(14*24*time.Hour) != "14d" || FormatStaleAfter(36*time.Hour) != "36h0m0s" {
		t.Error("FormatStaleAfter")
	}
}
