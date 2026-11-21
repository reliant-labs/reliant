package testflakes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// Confirming a red run: the port of scripts/flake-report.sh. Each failing Go
// test is rerun once, alone, under -race, and the result classified with
// pkg/testreport's taxonomy. It is the net for EXISTING tests (new ones meet
// the flake gate): it turns "failed, then passed on the same commit" into a
// record somebody acts on, instead of a red run somebody re-runs until it is
// green.
//
// It never changes the verdict of the run it read, and it never fails: every
// problem is reported and Confirm returns normally. The outcome is recorded
// as a run of its own, lane `confirm`, so flake detection reads it like any
// other run: the reruns as the lane's stream, and the failures it read, with
// every verdict, in <run>/confirm/confirm.json.
//
// What changed from the script, deliberately:
//
//   - Host signatures are matched BEFORE anything is rerun (amendment 7): a
//     failure the host caused is a host event, reported as host health, and
//     never spends a rerun or scores.
//   - The `broad` cut-off (more than MAX_TESTS failures: rerun nothing) is
//     replaced by correlated grouping: three or more failures sharing a
//     package, a harness or a signature are one infra event and are not
//     rerun; up to Max of the rest are, and any beyond are inconclusive.
//   - A rerun that could not run (its package failed to build, it timed out,
//     it hit a host failure) is inconclusive, not real.
//   - The reruns run with -json, so their result is read from the stream
//     rather than grepped from -v text.

// SchemaConfirm names the confirm document.
const SchemaConfirm = "forge.test.confirm/v1"

// ConfirmFile is the confirm document's name in the confirm lane directory.
const ConfirmFile = "confirm.json"

// DefaultMaxReruns is how many distinct tests one confirm reruns.
const DefaultMaxReruns = 15

// The sources a confirm reads.
const (
	SourceRun  = "run"  // a GitHub Actions run (gh run view --log-failed)
	SourceLogs = "logs" // a local gate's log directory
	SourceFrom = "from" // a results file (go test -json, JUnit, Vitest)
)

// ConfirmSource is the red run a confirm read.
type ConfirmSource struct {
	Kind string `json:"kind"`
	// Ref is what was named: the run id, the directory, the file.
	Ref string `json:"ref"`
	URL string `json:"url,omitempty"`
	// Commit and Tree are what the red run tested, when known.
	Commit string `json:"commit,omitempty"`
	Tree   string `json:"tree,omitempty"`
	// Lane is the lane the failures came from ("ci" for a CI run's logs).
	Lane string `json:"lane"`
	// LocalRun is the local run the failures are already recorded in
	// (--from a lane of .forge/tests/runs/), so readers do not count them
	// twice.
	LocalRun string `json:"local_run,omitempty"`
}

// ConfirmedTest is one failing test's verdict.
type ConfirmedTest struct {
	Test    testreport.TestID  `json:"test"`
	Verdict testreport.Verdict `json:"verdict"`
	// Reason says why, in a few words: the host rule, "reran: passed", …
	Reason     string `json:"reason,omitempty"`
	InfraGroup string `json:"infra_group,omitempty"`
	Signature  string `json:"signature,omitempty"`
	// Reran says the test was rerun.
	Reran bool `json:"reran,omitempty"`
}

// ConfirmCounts tallies the verdicts, as flake-report.sh's --verdict-out
// does, plus the two verdicts it did not have.
type ConfirmCounts struct {
	Failing      int `json:"failing"`
	Confirmed    int `json:"confirmed"`
	Real         int `json:"real"`
	Raced        int `json:"raced"`
	Inconclusive int `json:"inconclusive"`
	Unattributed int `json:"unattributed"`
	Host         int `json:"host"`
	Correlated   int `json:"correlated"`
}

// Confirm is the forge.test.confirm/v1 document: what a confirm read, what it
// reran, and every failure's verdict. Its exit code is always 0.
type Confirm struct {
	testreport.Head
	Source ConfirmSource `json:"source"`
	// Run is the run the reruns were recorded in; "" when nothing was rerun
	// or recording was off.
	Run string `json:"run,omitempty"`
	// Commit and Tree are what the reruns ran on.
	Commit string          `json:"commit,omitempty"`
	Tree   string          `json:"tree,omitempty"`
	Counts ConfirmCounts   `json:"counts"`
	Tests  []ConfirmedTest `json:"tests"`
	// Failures are the failures as read, output bounded.
	Failures []testreport.Case            `json:"failures"`
	Infra    []testreport.CorrelatedGroup `json:"infra,omitempty"`
	// Messages are what the confirm said, line by line.
	Messages  []string `json:"messages"`
	Suggested []string `json:"suggested,omitempty"`
}

// ConfirmOptions configures RunConfirm. Exactly one of RunID, LogsDir and
// From names the source.
type ConfirmOptions struct {
	// Root is the module root at the red run's commit: the reruns run here.
	Root string
	// RunID is a GitHub Actions run; Repo is its owner/name (default
	// $GITHUB_REPOSITORY, else the current repository).
	RunID string
	Repo  string
	// LogsDir is a local gate's log directory; SHA and RunURL describe it.
	LogsDir string
	SHA     string
	RunURL  string
	// From is a results file.
	From string
	// Max bounds the distinct tests rerun. Default DefaultMaxReruns.
	Max int
	// List stops after naming the failing tests: nothing is rerun or
	// recorded.
	List bool
	// Command is the project's own `go test`. Default ["go", "test"].
	Command []string
	Stdout  io.Writer
	Stderr  io.Writer
	Environ []string
	Getenv  func(string) string
	Now     func() time.Time
	Git     Git
	GH      GH
	Runner  Runner
	// Begin opens the run the reruns are recorded in.
	Begin testcapture.BeginOptions
}

func (o *ConfirmOptions) defaults() {
	if len(o.Command) == 0 {
		o.Command = []string{"go", "test"}
	}
	if o.Max <= 0 {
		o.Max = DefaultMaxReruns
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Environ == nil {
		o.Environ = os.Environ()
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Git == nil {
		o.Git = ExecGit
	}
	if o.GH == nil {
		o.GH = ExecGH
	}
	if o.Runner == nil {
		o.Runner = ExecRunner
	}
	if o.Begin.Getenv == nil {
		o.Begin.Getenv = o.Getenv
	}
}

type confirmer struct {
	o   ConfirmOptions
	doc *Confirm
}

func (c *confirmer) say(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	c.doc.Messages = append(c.doc.Messages, msg)
	fmt.Fprintf(c.o.Stdout, "forge test flakes confirm: %s\n", msg)
}

// RunConfirm reads the red run, reruns its failing tests and records the
// outcome. It always returns a document with exit code 0.
func RunConfirm(ctx context.Context, o ConfirmOptions) *Confirm {
	o.defaults()
	doc := &Confirm{Head: testreport.Head{Schema: SchemaConfirm, OK: true}, Tests: []ConfirmedTest{}, Failures: []testreport.Case{}, Messages: []string{}}
	c := &confirmer{o: o, doc: doc}
	module, _ := ModulePath(o.Root)

	failures, ok := c.read(ctx, module)
	if !ok {
		return doc
	}
	where := doc.Source.URL
	if where == "" {
		where = doc.Source.Ref
	}
	if len(failures) == 0 {
		c.say("no failing Go test in %s (a build, lint or non-Go failure): nothing to rerun", where)
		return doc
	}
	doc.Counts.Failing = len(failures)
	c.say("%d failing test(s) in %s:", len(failures), where)
	for _, f := range failures {
		fmt.Fprintf(o.Stdout, "  %s\t%s\n", f.ID.Package, displayName(f.ID))
	}
	if o.List {
		for _, f := range failures {
			doc.Failures = append(doc.Failures, stored(f))
		}
		return doc
	}

	verdicts, rerun := c.triage(failures, module)
	var stream []byte
	laneExit := 0
	if len(rerun) > 0 {
		stream, laneExit = c.rerun(ctx, rerun, module, verdicts)
	}
	results := parseStream(stream, LaneConfirm)
	for _, f := range rerun {
		v := verdicts[f.ID]
		if v.Verdict != "" {
			continue // decided before the rerun (no directory, …)
		}
		*v = c.judge(f, results)
	}
	laneDir := ""
	if len(rerun) > 0 {
		laneDir = c.record(ctx, stream, laneExit)
	}
	c.requireSameTree(rerun, verdicts)
	for _, f := range failures {
		v := verdicts[f.ID]
		doc.Tests = append(doc.Tests, *v)
		doc.Failures = append(doc.Failures, stored(f))
		count(&doc.Counts, v.Verdict)
	}
	sort.Slice(doc.Tests, func(i, j int) bool { return doc.Tests[i].Test.String() < doc.Tests[j].Test.String() })
	if doc.Counts.Confirmed == 0 {
		c.say("no test passed on retry")
	}
	for _, t := range doc.Tests {
		if t.Verdict == testreport.VerdictConfirmed {
			doc.Suggested = append(doc.Suggested, quarantineSuggestion(t.Test, ""))
		}
	}
	if laneDir != "" {
		c.writeDoc(laneDir)
	}
	return doc
}

// requireSameTree keeps "failed, then passed on retry" a confirmed flake
// only when the failure's tree and the reruns' tree are both known and
// equal. A pass on another snapshot, or on one nobody can name (the
// source commit is not in this clone, its metadata could not be read, the
// reruns were not recorded), proves nothing about the failure: it could be
// a real regression the checkout no longer has. Those are inconclusive.
func (c *confirmer) requireSameTree(rerun []testreport.Case, verdicts map[testreport.TestID]*ConfirmedTest) {
	src, ran := c.doc.Source.Tree, c.doc.Tree
	if src != "" && src == ran {
		return
	}
	reason := "passed on retry, but the failure's tree is unknown, so the retry proves nothing"
	switch {
	case src != "" && ran != "":
		reason = fmt.Sprintf("passed on retry on tree %s, but the failure came from tree %s", short(ran), short(src))
	case src != "":
		reason = "passed on retry, but the retry's tree is unknown (not recorded), so it proves nothing"
	}
	downgraded := 0
	for _, f := range rerun {
		v := verdicts[f.ID]
		if v == nil || v.Verdict != testreport.VerdictConfirmed {
			continue
		}
		v.Verdict, v.Reason = testreport.VerdictInconclusive, reason
		downgraded++
	}
	if downgraded > 0 {
		c.say("%d test(s) passed on retry, but not on the failure's tree: inconclusive, not flakes (%s; rerun from a checkout of %s)", downgraded, reason, orNone(c.doc.Source.Commit))
	}
}

// stored is a failure as the document keeps it: output bounded.
func stored(f testreport.Case) testreport.Case {
	f.Output, f.OutputTruncated = boundOutput(f.Output)
	return f
}

func displayName(id testreport.TestID) string {
	if id.Name == "" {
		return "(package)"
	}
	return id.Name
}

func count(c *ConfirmCounts, v testreport.Verdict) {
	switch v {
	case testreport.VerdictConfirmed:
		c.Confirmed++
	case testreport.VerdictReal:
		c.Real++
	case testreport.VerdictRaced:
		c.Raced++
	case testreport.VerdictInconclusive:
		c.Inconclusive++
	case testreport.VerdictUnattributed:
		c.Unattributed++
	case testreport.VerdictHost:
		c.Host++
	case testreport.VerdictCorrelated:
		c.Correlated++
	}
}

// read loads the failures from the named source and fills doc.Source. ok is
// false when the source could not be read (said, not returned as an error).
func (c *confirmer) read(ctx context.Context, module string) ([]testreport.Case, bool) {
	o := c.o
	switch {
	case o.From != "":
		return c.readFrom()
	case o.LogsDir != "":
		src := ConfirmSource{Kind: SourceLogs, Ref: o.LogsDir, URL: o.RunURL, Commit: o.SHA, Lane: "logs"}
		if src.URL == "" {
			src.URL = "the local gate run in " + o.LogsDir
		}
		if src.Commit == "" {
			src.Commit, _ = o.Git(ctx, o.Root, "rev-parse", "HEAD")
		}
		src.Tree = c.treeOf(ctx, src.Commit)
		c.doc.Source = src
		return logFailures(readRunLogs(nil, o.LogsDir), module), true
	case o.RunID != "":
		return c.readRun(ctx, module)
	}
	c.say("nothing to confirm: name a CI run (--run), a log directory (--logs) or a results file (--from)")
	return nil, false
}

func (c *confirmer) readFrom() ([]testreport.Case, bool) {
	o := c.o
	src := ConfirmSource{Kind: SourceFrom, Ref: o.From, Lane: SourceFrom}
	abs, _ := filepath.Abs(o.From)
	laneDir := filepath.Dir(abs)
	runDir := filepath.Dir(laneDir)
	if m, err := testreport.ReadRunManifest(runDir); err == nil {
		src.Lane, src.LocalRun, src.Commit, src.Tree = filepath.Base(laneDir), m.ID, m.Commit, m.TreeHash
	}
	c.doc.Source = src
	f, err := os.Open(o.From)
	if err != nil {
		c.say("could not read %s: %v", o.From, err)
		return nil, false
	}
	defer func() { _ = f.Close() }()
	res, err := testreport.ParseResults(f, testreport.ResultsOptions{Lane: testreport.Lane(src.Lane)})
	if err != nil {
		c.say("could not read the results in %s: %v", o.From, err)
		return nil, false
	}
	return resultsFailures(res), true
}

var artifactRe = regexp.MustCompile(`gate-logs|^test-short-`)

func (c *confirmer) readRun(ctx context.Context, module string) ([]testreport.Case, bool) {
	o := c.o
	repo := o.Repo
	if repo == "" {
		repo = o.Getenv("GITHUB_REPOSITORY")
	}
	if repo == "" {
		if out, err := o.GH(ctx, "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"); err == nil {
			repo = strings.TrimSpace(string(out))
		}
	}
	server := o.Getenv("GITHUB_SERVER_URL")
	if server == "" {
		server = "https://github.com"
	}
	src := ConfirmSource{Kind: SourceRun, Ref: o.RunID, URL: fmt.Sprintf("%s/%s/actions/runs/%s", server, repo, o.RunID), Lane: "ci"}
	c.doc.Source = src
	failed, err := o.GH(ctx, "run", "view", o.RunID, "-R", repo, "--log-failed")
	if err != nil {
		c.say("could not read the failed logs of %s: %s", src.URL, truncate(err.Error(), 300))
		return nil, false
	}
	if sha, err := o.GH(ctx, "run", "view", o.RunID, "-R", repo, "--json", "headSha", "-q", ".headSha"); err == nil {
		src.Commit = strings.TrimSpace(string(sha))
	}
	src.Tree = c.treeOf(ctx, src.Commit)
	c.doc.Source = src

	art, err := os.MkdirTemp("", "forge-flakes-confirm-*")
	if err != nil {
		c.say("cannot make a directory for the run's artifacts: %v", err)
		return logFailures(readRunLogs(failed, ""), module), true
	}
	defer func() { _ = os.RemoveAll(art) }()
	names, _ := o.GH(ctx, "api", fmt.Sprintf("repos/%s/actions/runs/%s/artifacts", repo, o.RunID), "-q", ".artifacts[] | select(.expired == false) | .name")
	for _, name := range strings.Fields(string(names)) {
		if !artifactRe.MatchString(name) {
			continue
		}
		if _, err := o.GH(ctx, "run", "download", o.RunID, "-R", repo, "-n", name, "-D", filepath.Join(art, name)); err != nil {
			c.say("could not download artifact %s", name)
		}
	}
	return logFailures(readRunLogs(failed, art), module), true
}

func (c *confirmer) treeOf(ctx context.Context, commit string) string {
	if commit == "" {
		return ""
	}
	tree, err := c.o.Git(ctx, c.o.Root, "rev-parse", commit+"^{tree}")
	if err != nil {
		return ""
	}
	return tree
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// triage decides every failure that needs no rerun — a race, a host
// signature, a failure no test owns, a correlated group, one past the rerun
// budget — and returns the rest, to rerun. Every failure gets an entry in the
// map; a rerun one's verdict is empty until judged.
func (c *confirmer) triage(failures []testreport.Case, module string) (map[testreport.TestID]*ConfirmedTest, []testreport.Case) {
	verdicts := map[testreport.TestID]*ConfirmedTest{}
	var pending []testreport.Case
	for _, f := range failures {
		v := &ConfirmedTest{Test: f.ID, Signature: f.Signature}
		verdicts[f.ID] = v
		switch {
		case testreport.IsRace(f.Output):
			v.Verdict, v.Reason = testreport.VerdictRaced, "reported a data race"
			c.say("  %s reported a data race: a real failure, not retried or reported as a flake", displayName(f.ID))
		default:
			if rule, ok := testreport.MatchHost(f.Output, nil); ok {
				v.Verdict, v.Reason = testreport.VerdictHost, "host: "+rule
				c.say("  %s failed on the host (%s): host health, not retried or scored", displayName(f.ID), rule)
				continue
			}
			if f.ID.Name == "" {
				v.Verdict, v.Reason = testreport.VerdictUnattributed, "no test owns the failure"
				c.say("  %s failed with no failing test to show for it: unattributed", f.ID.Package)
				continue
			}
			if f.ID.Lang != testreport.LangGo {
				v.Verdict, v.Reason = testreport.VerdictInconclusive, "only Go tests are rerun"
				continue
			}
			pending = append(pending, f)
		}
	}
	groups, rest := testreport.GroupCorrelated(pending, testreport.CorrelateOptions{})
	for _, g := range groups {
		c.doc.Infra = append(c.doc.Infra, g)
		members := map[testreport.TestID]bool{}
		for _, m := range g.Members {
			members[m] = true
		}
		for _, f := range pending {
			if members[f.ID.TopLevel()] {
				v := verdicts[f.ID]
				v.Verdict, v.Reason, v.InfraGroup = testreport.VerdictCorrelated, "fails with others: "+g.Reason, g.Key
			}
		}
		c.say("  %d tests failed together (%s): one infra event, %s; not retried", len(g.Members), g.Reason, g.Key)
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].ID.String() < rest[j].ID.String() })
	if len(rest) > c.o.Max {
		for _, f := range rest[c.o.Max:] {
			v := verdicts[f.ID]
			v.Verdict, v.Reason = testreport.VerdictInconclusive, fmt.Sprintf("over the rerun limit of %d", c.o.Max)
		}
		c.say("%d tests to rerun, more than %d: rerunning the first %d, the rest are inconclusive", len(rest), c.o.Max, c.o.Max)
		rest = rest[:c.o.Max]
	}
	return verdicts, rest
}

// rerun reruns the tests once per package, under -race, and returns the
// concatenated -json streams and the last non-zero exit status.
func (c *confirmer) rerun(ctx context.Context, tests []testreport.Case, module string, verdicts map[testreport.TestID]*ConfirmedTest) ([]byte, int) {
	o := c.o
	byPkg := map[string][]string{}
	var pkgs []string
	for _, t := range tests {
		if byPkg[t.ID.Package] == nil {
			pkgs = append(pkgs, t.ID.Package)
		}
		byPkg[t.ID.Package] = append(byPkg[t.ID.Package], t.ID.Name)
	}
	sort.Strings(pkgs)
	var stream bytes.Buffer
	exit := 0
	for _, pkg := range pkgs {
		names := byPkg[pkg]
		undecided := func(reason string) {
			for _, n := range names {
				v := verdicts[testreport.TestID{Lang: testreport.LangGo, Package: pkg, Name: n}]
				v.Verdict, v.Reason = testreport.VerdictInconclusive, reason
			}
		}
		rel, ok := relPackage(pkg, module)
		if !ok {
			c.say("skip %s: not in module %s", pkg, orNone(module))
			undecided("not in this module")
			continue
		}
		dir := filepath.Join(o.Root, filepath.FromSlash(rel))
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			c.say("skip %s: no directory ./%s at this commit", pkg, rel)
			undecided("no package directory at this commit")
			continue
		}
		tags := buildTags(dir, names)
		argv := append(append([]string(nil), o.Command...), "-json", "-race", "-count=1", "-timeout", "20m")
		if len(tags) > 0 {
			argv = append(argv, "-tags", strings.Join(tags, ","))
		}
		pkgArg := "./" + rel
		if rel == "." {
			pkgArg = "."
		}
		argv = append(argv, "-run", "^("+strings.Join(names, "|")+")$", pkgArg)
		tagNote := ""
		if len(tags) > 0 {
			tagNote = " (tags " + strings.Join(tags, ",") + ")"
		}
		c.say("rerunning %s in %s%s", strings.Join(names, " "), pkg, tagNote)
		code, err := o.Runner(ctx, o.Root, argv, o.Environ, &stream, o.Stderr)
		if err != nil {
			c.say("could not run %s: %v", o.Command[0], err)
			undecided("the rerun could not start")
			continue
		}
		if code != 0 {
			exit = code
		}
	}
	return stream.Bytes(), exit
}

func orNone(s string) string {
	if s == "" {
		return "(no go.mod)"
	}
	return s
}

// relPackage is pkg's directory relative to the module root.
func relPackage(pkg, module string) (string, bool) {
	switch {
	case module == "":
		return "", false
	case pkg == module:
		return ".", true
	case strings.HasPrefix(pkg, module+"/"):
		return strings.TrimPrefix(pkg, module+"/"), true
	}
	return "", false
}

// buildTags are the build tags of the files declaring names: the single tag
// a `//go:build <tag>` line in a file's first five lines names, as
// flake-report.sh reads them.
func buildTags(dir string, names []string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "*_test.go"))
	var tags []string
	seen := map[string]bool{}
	for _, n := range names {
		decl := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(n) + `\(`)
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil || !decl.Match(data) {
				continue
			}
			lines := strings.SplitN(string(data), "\n", 6)
			for i := 0; i < len(lines) && i < 5; i++ {
				if m := buildTagRe.FindStringSubmatch(strings.TrimRight(lines[i], "\r")); len(m) == 2 && !seen[m[1]] {
					seen[m[1]] = true
					tags = append(tags, m[1])
				}
			}
			break
		}
	}
	return tags
}

// judge classifies a rerun test from the reruns' results.
func (c *confirmer) judge(f testreport.Case, results *testreport.Results) ConfirmedTest {
	v := ConfirmedTest{Test: f.ID, Signature: f.Signature, Reran: true}
	rerun, pkg := rerunOf(results, f.ID)
	switch {
	case rerun == nil && pkg != nil && (pkg.BuildFailed || pkg.Status == testreport.StatusFail):
		v.Verdict, v.Reason = testreport.VerdictInconclusive, "the rerun's package failed before the test ran"
		c.say("  %s could not run on retry (its package failed): inconclusive", f.ID.Name)
	case rerun == nil:
		v.Verdict, v.Reason = testreport.VerdictReal, "did not run on retry"
		c.say("  %s did not run on retry: a real failure, not reported as a flake", f.ID.Name)
	case rerun.Status == testreport.StatusSkip:
		v.Verdict, v.Reason = testreport.VerdictInconclusive, "skipped on retry"
		c.say("  %s skipped on retry (its environment is missing here): inconclusive", f.ID.Name)
	default:
		v.Verdict = testreport.Classify(f, rerun, nil)
		switch v.Verdict {
		case testreport.VerdictConfirmed:
			v.Reason = "failed, then passed on retry"
			c.say("  %s PASSED on retry: confirmed flake", f.ID.Name)
		case testreport.VerdictRaced:
			v.Reason = "the retry reported a data race"
			c.say("  %s reported a data race on retry: a real failure", f.ID.Name)
		case testreport.VerdictInconclusive:
			v.Reason = "the retry failed on the host"
			c.say("  %s failed on the host on retry: inconclusive", f.ID.Name)
		default:
			v.Reason = "failed again on retry"
			c.say("  %s failed again: a real failure, not reported as a flake", f.ID.Name)
		}
	}
	return v
}

// rerunOf is id's top-level result in the reruns (its last attempt, failed
// when any of its leaves failed), and its package's.
func rerunOf(r *testreport.Results, id testreport.TestID) (*testreport.Case, *testreport.PackageResult) {
	if r == nil {
		return nil, nil
	}
	var pkg *testreport.PackageResult
	for i := range r.Packages {
		if r.Packages[i].Lang == id.Lang && r.Packages[i].Path == id.Package {
			pkg = &r.Packages[i]
		}
	}
	var top *testreport.Case
	failed := false
	var output strings.Builder
	for _, c := range r.Cases {
		if c.ID == id {
			cc := c
			top = &cc
		}
		if c.ID.TopLevel() == id && (c.Status == testreport.StatusFail || c.FailedAttempts > 0) {
			failed = true
			output.WriteString(c.Output)
		}
	}
	if top == nil {
		return nil, pkg
	}
	if failed {
		top.Status = testreport.StatusFail
		top.Output = output.String()
	}
	return top, pkg
}

// record writes the reruns as lane confirm of a new run and returns the
// lane's directory ("" when nothing was recorded); writeDoc puts the
// document beside them once the verdicts are final.
func (c *confirmer) record(ctx context.Context, stream []byte, exit int) string {
	o := c.o
	runDir, recording, warnings := openRun(ctx, o.Begin)
	for _, w := range warnings {
		fmt.Fprintf(o.Stderr, "forge test flakes confirm: warning: %s\n", w)
	}
	if !recording {
		return ""
	}
	now := o.Now()
	cmd := []string{"forge", "test", "flakes", "confirm", "--" + c.doc.Source.Kind, c.doc.Source.Ref}
	laneDir, err := recordLane(runDir, laneRun{Lane: LaneConfirm, Command: cmd, StartedAt: now, FinishedAt: now, ExitCode: exit, Stream: stream})
	if err != nil {
		fmt.Fprintf(o.Stderr, "forge test flakes confirm: warning: %v\n", err)
		return ""
	}
	c.doc.Run = runDir
	if m, err := testreport.ReadRunManifest(runDir); err == nil {
		c.doc.Commit, c.doc.Tree = m.Commit, m.TreeHash
	}
	return laneDir
}

// writeDoc writes the document into the confirm lane's directory.
func (c *confirmer) writeDoc(laneDir string) {
	o := c.o
	data, err := json.MarshalIndent(c.doc, "", "  ")
	if err == nil {
		err = atomicfile.WriteFile(filepath.Join(laneDir, ConfirmFile), append(data, '\n'), 0o644)
	}
	if err != nil {
		fmt.Fprintf(o.Stderr, "forge test flakes confirm: warning: cannot write %s: %v\n", ConfirmFile, err)
	}
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// ReadConfirm reads a confirm lane's document.
func ReadConfirm(laneDir string) (*Confirm, error) {
	data, err := os.ReadFile(filepath.Join(laneDir, ConfirmFile))
	if err != nil {
		return nil, err
	}
	var doc Confirm
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Schema != SchemaConfirm {
		return nil, fmt.Errorf("%s: schema %q, want %q", filepath.Join(laneDir, ConfirmFile), doc.Schema, SchemaConfirm)
	}
	return &doc, nil
}
