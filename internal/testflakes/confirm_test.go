package testflakes

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testreport"
)

func ids(cases []testreport.TestID) string {
	var out []string
	for _, c := range cases {
		out = append(out, c.Package+" "+c.Name)
	}
	return strings.Join(out, "\n")
}

// The three attribution rules over real `gh run view --log-failed` lines
// (reliant run 44401763094) plus annotation, cross-step and Windows cases.
func TestLogFailedAttribution(t *testing.T) {
	data, err := os.ReadFile("testdata/log-failed.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := ids(readRunLogs(data, "").failing("github.com/reliant-labs/reliant"))
	want := strings.Join([]string{
		"github.com/reliant-labs/reliant/internal/accesstokenclient TestCall_ReadRetriesADeadKeepAliveConnection",
		"github.com/reliant-labs/reliant/internal/authz TestEveryUserScopedReadIsClassified",
		"github.com/reliant-labs/reliant/internal/sessions TestSessionExpiry",
	}, "\n")
	if got != want {
		t.Errorf("failing =\n%s\nwant\n%s", got, want)
	}
}

func TestLogDirAttributionAndBlocks(t *testing.T) {
	rl := readRunLogs(nil, "testdata/gate-logs")
	got := ids(rl.failing(testModule))
	want := strings.Join([]string{
		"example.com/m/internal/a TestFlaky",
		"example.com/m/internal/b TestRacy",
		"example.com/m/internal/c TestNet",
		"example.com/m/internal/e TestStream",
	}, "\n")
	if got != want {
		t.Errorf("failing =\n%s\nwant\n%s", got, want)
	}
	if b := rl.block("TestRacy"); !testreport.IsRace(b) {
		t.Errorf("the race report precedes --- FAIL and must be in the block:\n%s", b)
	}
	if b := rl.block("TestFlaky"); !strings.Contains(b, "got 1, want 2") {
		t.Errorf("block = %q", b)
	}
	if b := rl.block("TestStream"); !strings.Contains(b, "stream_test.go:9: boom") {
		t.Errorf("json block = %q", b)
	}
}

type confirmFixture struct {
	root  string
	out   *bytes.Buffer
	opts  ConfirmOptions
	clock time.Time
}

func newConfirmFixture(t *testing.T, r *fakeRunner) *confirmFixture {
	root := newModule(t, map[string]string{
		"internal/a/a_test.go": "package a\n\nfunc TestFlaky(t *testing.T) {}\n",
		"internal/e/e_test.go": "//go:build integration\n\npackage e\n\nfunc TestStream(t *testing.T) {}\n",
	})
	out := &bytes.Buffer{}
	clock := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	git := func(_ context.Context, _ string, args ...string) (string, error) {
		if args[0] == "rev-parse" && strings.HasSuffix(args[1], "^{tree}") {
			return "tree1", nil
		}
		if args[0] == "rev-parse" {
			return "commit1", nil
		}
		return "", errors.New("unexpected git")
	}
	return &confirmFixture{root: root, out: out, clock: clock, opts: ConfirmOptions{
		Root: root, Stdout: out, Stderr: out, Environ: []string{"PATH=/bin"}, Getenv: noEnv,
		Now: func() time.Time { return clock }, Git: git, Runner: r.run,
		GH:    func(context.Context, ...string) ([]byte, error) { return nil, errors.New("gh must not run") },
		Begin: begin(root, "commit1", "tree1", clock),
	}}
}

// The log directory has one flake, one race, one host failure and one
// integration-tagged test that fails again: each gets its verdict, only two
// are rerun (under -race, the tagged one with -tags), and the outcome is a
// confirm run that `forge test flakes` reads as a confirmed flake.
func TestConfirmClassifiesAndRecords(t *testing.T) {
	r := &fakeRunner{script: func(argv []string) (string, int) {
		switch argv[len(argv)-1] {
		case "./internal/a":
			return passRun(testModule+"/internal/a", "TestFlaky") + ev("pass", testModule+"/internal/a", "", ""), 0
		case "./internal/e":
			return failRun(testModule+"/internal/e", "TestStream", "boom again\n") + ev("fail", testModule+"/internal/e", "", ""), 1
		}
		return "", 1
	}}
	f := newConfirmFixture(t, r)
	f.opts.LogsDir = "testdata/gate-logs"
	doc := RunConfirm(context.Background(), f.opts)
	if doc.ExitCode != 0 || !doc.OK {
		t.Fatalf("confirm must always exit 0: %+v", doc.Head)
	}
	verdicts := map[string]testreport.Verdict{}
	for _, v := range doc.Tests {
		verdicts[v.Test.Name] = v.Verdict
	}
	want := map[string]testreport.Verdict{
		"TestFlaky": testreport.VerdictConfirmed, "TestRacy": testreport.VerdictRaced,
		"TestNet": testreport.VerdictHost, "TestStream": testreport.VerdictReal,
	}
	for k, v := range want {
		if verdicts[k] != v {
			t.Errorf("%s: %s, want %s\n%s", k, verdicts[k], v, f.out)
		}
	}
	if len(r.calls) != 2 {
		t.Fatalf("reruns = %v", r.calls)
	}
	for _, c := range r.calls {
		if !hasArg(c, "-race") || !hasArg(c, "-count=1") || !hasArg(c, "-json") {
			t.Errorf("rerun argv = %v", c)
		}
	}
	if got := argAfter(r.calls[1], "-tags"); got != "integration" {
		t.Errorf("tags = %q (%v)", got, r.calls[1])
	}
	if got := argAfter(r.calls[0], "-run"); got != "^(TestFlaky)$" {
		t.Errorf("-run = %q", got)
	}
	c := doc.Counts
	if c.Failing != 4 || c.Confirmed != 1 || c.Raced != 1 || c.Host != 1 || c.Real != 1 {
		t.Errorf("counts = %+v", c)
	}
	if doc.Run == "" || doc.Source.Tree != "tree1" || doc.Tree != "tree1" {
		t.Fatalf("run = %q source %+v tree %q", doc.Run, doc.Source, doc.Tree)
	}
	stored, err := ReadConfirm(testreport.LaneDir(doc.Run, LaneConfirm))
	if err != nil || len(stored.Tests) != 4 {
		t.Fatalf("confirm.json: %v %+v", err, stored)
	}
	if len(doc.Suggested) != 1 || !strings.Contains(doc.Suggested[0], "forge test quarantine add go:example.com/m/internal/a:TestFlaky") {
		t.Errorf("suggested = %v", doc.Suggested)
	}

	flakes, warnings := ComputeFlakes(FlakesOptions{Root: f.root, Now: f.clock.Add(time.Hour)})
	if len(warnings) > 0 {
		t.Errorf("warnings: %v", warnings)
	}
	if len(flakes.Tests) != 1 || flakes.Tests[0].ID.Name != "TestFlaky" || flakes.Tests[0].Events[0].Signal != testreport.SignalConfirm {
		t.Errorf("flakes = %+v", flakes.Tests)
	}
	if len(flakes.Host) != 1 || flakes.Host[0].Signature != "network" {
		t.Errorf("host health = %+v", flakes.Host)
	}
}

// Three failures sharing a package are one infra event and are not rerun;
// past --max the rest are inconclusive.
func TestConfirmCorrelatesAndBoundsReruns(t *testing.T) {
	r := &fakeRunner{script: func(argv []string) (string, int) {
		var b strings.Builder
		pkg := testModule + "/" + strings.TrimPrefix(argv[len(argv)-1], "./")
		for _, n := range strings.Split(strings.Trim(argAfter(argv, "-run"), "^()$"), "|") {
			b.WriteString(passRun(pkg, n))
		}
		return b.String(), 0
	}}
	f := newConfirmFixture(t, r)
	var stream strings.Builder
	for _, n := range []string{"TestA", "TestB", "TestC"} {
		stream.WriteString(failRun(testModule+"/internal/x", n, n+" broke differently\n"))
	}
	for i, d := range []string{"p1", "p2", "p3"} {
		write(t, filepath.Join(f.root, "internal", d, "x_test.go"), "package x\n")
		stream.WriteString(failRun(testModule+"/internal/"+d, "TestOnly", strings.Repeat("unique ", i+1)+"\n"))
	}
	from := filepath.Join(t.TempDir(), "results.json")
	write(t, from, stream.String())
	f.opts.From = from
	f.opts.Max = 2
	doc := RunConfirm(context.Background(), f.opts)
	if len(doc.Infra) != 1 || len(doc.Infra[0].Members) != 3 || !strings.HasPrefix(doc.Infra[0].Reason, "package:") {
		t.Fatalf("infra = %+v", doc.Infra)
	}
	// The results file is not a recorded run's, so the failures' tree is
	// unknown: the two reruns that pass are inconclusive too, not flakes.
	c := doc.Counts
	if c.Correlated != 3 || c.Confirmed != 0 || c.Inconclusive != 3 || len(doc.Suggested) != 0 {
		t.Errorf("counts = %+v, suggested %v\n%s", c, doc.Suggested, f.out)
	}
	for _, call := range r.calls {
		if strings.HasSuffix(call[len(call)-1], "internal/x") {
			t.Errorf("a correlated group must not be rerun: %v", call)
		}
	}
}

// A pass on retry confirms a flake only when the failure's tree is known
// and is the tree the retry ran on. Here the source commit's tree cannot be
// read (the commit is not in this clone): the pass is inconclusive, nothing
// is suggested for quarantine, and the record holds no flake.
func TestConfirmNeedsTheFailuresTree(t *testing.T) {
	r := &fakeRunner{script: func(argv []string) (string, int) {
		switch argv[len(argv)-1] {
		case "./internal/a":
			return passRun(testModule+"/internal/a", "TestFlaky") + ev("pass", testModule+"/internal/a", "", ""), 0
		case "./internal/e":
			return failRun(testModule+"/internal/e", "TestStream", "boom again\n") + ev("fail", testModule+"/internal/e", "", ""), 1
		}
		return "", 1
	}}
	f := newConfirmFixture(t, r)
	f.opts.LogsDir, f.opts.SHA = "testdata/gate-logs", "missingcommit"
	f.opts.Git = func(_ context.Context, _ string, args ...string) (string, error) {
		if args[0] == "rev-parse" && args[1] == "missingcommit^{tree}" {
			return "", errors.New("fatal: not a valid object name")
		}
		return "tree1", nil
	}
	doc := RunConfirm(context.Background(), f.opts)
	if doc.Source.Tree != "" || doc.Tree != "tree1" {
		t.Fatalf("source tree %q, rerun tree %q", doc.Source.Tree, doc.Tree)
	}
	for _, v := range doc.Tests {
		if v.Test.Name == "TestFlaky" && (v.Verdict != testreport.VerdictInconclusive || !strings.Contains(v.Reason, "tree is unknown")) {
			t.Errorf("TestFlaky: %s (%s), want inconclusive", v.Verdict, v.Reason)
		}
	}
	if doc.Counts.Confirmed != 0 || len(doc.Suggested) != 0 {
		t.Errorf("counts %+v, suggested %v", doc.Counts, doc.Suggested)
	}
	stored, err := ReadConfirm(testreport.LaneDir(doc.Run, LaneConfirm))
	if err != nil || stored.Counts.Confirmed != 0 {
		t.Fatalf("confirm.json: %v %+v", err, stored)
	}
	flakes, _ := ComputeFlakes(FlakesOptions{Root: f.root, Now: f.clock.Add(time.Hour)})
	for _, ft := range flakes.Tests {
		if ft.ID.Name == "TestFlaky" {
			t.Errorf("a pass on an unproven tree made TestFlaky a flake: %+v", ft)
		}
	}
}

func TestConfirmReadsAGitHubRunThroughGH(t *testing.T) {
	logFailed, _ := os.ReadFile("testdata/log-failed.txt")
	var calls []string
	gh := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch {
		case args[0] == "run" && args[1] == "view" && hasArg(args, "--log-failed"):
			return logFailed, nil
		case args[0] == "run" && args[1] == "view":
			return []byte("abc123\n"), nil
		case args[0] == "api":
			return []byte("gate-logs-unit\ncoverage\n"), nil
		case args[0] == "run" && args[1] == "download":
			return nil, nil
		}
		return nil, errors.New("unexpected gh " + strings.Join(args, " "))
	}
	r := &fakeRunner{script: func([]string) (string, int) { return "", 0 }}
	f := newConfirmFixture(t, r)
	f.opts.GH, f.opts.RunID, f.opts.Repo, f.opts.List = gh, "44401763094", "reliant-labs/reliant", true
	doc := RunConfirm(context.Background(), f.opts)
	if doc.Source.URL != "https://github.com/reliant-labs/reliant/actions/runs/44401763094" || doc.Source.Commit != "abc123" {
		t.Errorf("source = %+v", doc.Source)
	}
	if doc.Counts.Failing != 3 || len(r.calls) != 0 || doc.Run != "" {
		t.Errorf("--list: failing %d, reruns %v, run %q", doc.Counts.Failing, r.calls, doc.Run)
	}
	downloads := 0
	for _, c := range calls {
		if strings.HasPrefix(c, "run download") {
			downloads++
			if !strings.Contains(c, "-n gate-logs-unit") {
				t.Errorf("downloaded %s", c)
			}
		}
	}
	if downloads != 1 {
		t.Errorf("downloads = %d (%v)", downloads, calls)
	}
}

// A source that cannot be read is said, and still exits 0.
func TestConfirmNeverFails(t *testing.T) {
	r := &fakeRunner{script: func([]string) (string, int) { return "", 0 }}
	f := newConfirmFixture(t, r)
	f.opts.RunID, f.opts.Repo = "1", "a/b"
	doc := RunConfirm(context.Background(), f.opts)
	if doc.ExitCode != 0 || len(doc.Messages) == 0 || !strings.Contains(doc.Messages[0], "could not read the failed logs") {
		t.Errorf("%+v", doc)
	}
	f.opts.RunID, f.opts.From = "", filepath.Join(t.TempDir(), "missing.json")
	if doc := RunConfirm(context.Background(), f.opts); doc.ExitCode != 0 {
		t.Errorf("%+v", doc)
	}
}
