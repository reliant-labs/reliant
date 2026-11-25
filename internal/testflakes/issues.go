package testflakes

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/testreport"
)

// Filing `flake:suspected` issues with gh, for a project with no control
// plane: the port of flake-report.sh's issue handling. One issue per test,
// titled `flake:suspected: <Test> (<package>)` as the script titles it, so
// the issues it already filed are the ones found and updated:
//
//   - a suspected or flaky test with no issue gets one, carrying the test's
//     body, its failure signature and the runs that showed it;
//   - one whose issue exists gets a comment naming the runs the issue does
//     not mention yet (and is reopened if it was closed);
//   - an open issue whose test has left those states — healthy again, fixed,
//     removed or quarantined — is closed, saying why.
//
// A test whose failures in the record were all real (it failed again on a
// rerun), host or infra is healthy without ever having been suspected here:
// it gets no issue, and an open one is left alone.
//
// A test the local record knows nothing about is never touched by those
// rules: the local record is one machine's, and silence there is not
// evidence. A CI runner starts with an empty record, so on CI nothing would
// ever close; StaleAfter is the sweep for that case (flake-report.sh
// --sweep's port), which reads the evidence from the issues themselves:
//
//   - an open issue whose test function no longer exists in the checkout is
//     closed;
//   - an open issue whose newest evidence — its creation, or the newest
//     "Failed then passed on retry again" comment forge wrote on it — is
//     older than StaleAfter is closed.
//
// Either way a later flake of the test finds the closed issue and reopens it.

// LabelSuspected is the label of a suspected flake's issue.
const LabelSuspected = "flake:suspected"

// evidencePrefix starts every comment forge (and flake-report.sh before it)
// writes when a test flakes again: the sweep's evidence marker. Comments
// anyone else writes do not keep an issue open.
const evidencePrefix = "Failed then passed on retry again"

// IssueOptions configures SyncIssues.
type IssueOptions struct {
	Root   string
	Repo   string
	GH     GH
	Getenv func(string) string
	Stdout io.Writer
	// DryRun says what would be filed, commented and closed, and does none
	// of it.
	DryRun bool
	// StaleAfter, when positive, also closes each open forge-filed issue
	// whose newest flake evidence is older than this, or whose test no
	// longer exists under Root. Zero leaves those issues alone.
	StaleAfter time.Duration
	// Now is the clock StaleAfter is measured against (default time.Now).
	Now time.Time
}

// IssueResult is what SyncIssues did.
type IssueResult struct {
	// URLs maps a test id to its issue.
	URLs     map[string]string
	Opened   []string
	Updated  []string
	Closed   []string
	Messages []string
}

type ghIssue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	URL       string    `json:"url"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	Comments  []struct {
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"createdAt"`
	} `json:"comments"`
}

func (i ghIssue) text() string {
	var b strings.Builder
	b.WriteString(i.Body)
	for _, c := range i.Comments {
		b.WriteString("\n")
		b.WriteString(c.Body)
	}
	return b.String()
}

// SyncIssues files, updates and closes `flake:suspected` issues for the tests
// of doc, which must carry every state (healthy included) so a test that left
// suspected is seen leaving it.
func SyncIssues(ctx context.Context, o IssueOptions, doc *testreport.Flakes) IssueResult {
	if o.GH == nil {
		o.GH = ExecGH
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	s := &issueSync{o: o, res: IssueResult{URLs: map[string]string{}}}
	s.repo = o.Repo
	if s.repo == "" {
		s.repo = o.Getenv("GITHUB_REPOSITORY")
	}
	if s.repo == "" {
		out, err := o.GH(ctx, "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner")
		if err != nil {
			s.say("cannot tell which repository to file issues in (set GITHUB_REPOSITORY or pass --repo): %v", err)
			return s.res
		}
		s.repo = strings.TrimSpace(string(out))
	}
	s.module, _ = ModulePath(o.Root)

	open := map[string]ghIssue{} // title -> issue
	fields := "number,title,state,url"
	if o.StaleAfter > 0 {
		fields += ",createdAt,comments"
	}
	out, err := o.GH(ctx, "issue", "list", "-R", s.repo, "--label", LabelSuspected, "--state", "open", "--limit", "500", "--json", fields)
	if err != nil {
		s.say("cannot list the open %s issues: %v", LabelSuspected, err)
		return s.res
	}
	var issues []ghIssue
	if json.Unmarshal(out, &issues) == nil {
		for _, i := range issues {
			open[i.Title] = i
		}
	}
	// handled holds the titles the record decided this run: the sweep
	// leaves them to it, so a test the record still calls a flake is not
	// closed (and then reopened by the next run), and none is closed twice.
	handled := map[string]bool{}
	for _, t := range doc.Tests {
		if t.ID.Name == "" || t.ID.Lang != testreport.LangGo {
			continue
		}
		title := IssueTitle(t.ID, s.module)
		switch t.State {
		case testreport.StateSuspected, testreport.StateFlaky:
			handled[title] = true
			s.file(ctx, t)
		case testreport.StateHealthy:
			// Healthy with no Since was never suspected in this record: its
			// failures were all real, host or infra. That is no evidence it
			// stopped flaking, so its issue is left alone.
			if t.Since == nil {
				continue
			}
			if existing, ok := open[title]; ok {
				handled[title] = true
				s.close(ctx, existing, closeReason(t))
			}
		default:
			if existing, ok := open[title]; ok {
				handled[title] = true
				s.close(ctx, existing, closeReason(t))
			}
		}
	}
	if o.StaleAfter > 0 {
		s.sweep(ctx, issues, handled)
	}
	return s.res
}

// issueTitleRE parses IssueTitle: the test's name and its package, relative
// to the module (the module path itself for its root package).
var issueTitleRE = regexp.MustCompile(`^flake:suspected: (\S+) \((.+)\)$`)

// sweep closes each open issue, of the ones the record did not decide, whose
// test is gone from the checkout or whose newest evidence is older than
// StaleAfter.
func (s *issueSync) sweep(ctx context.Context, issues []ghIssue, handled map[string]bool) {
	now := s.o.Now
	if now.IsZero() {
		now = time.Now()
	}
	for _, i := range issues {
		m := issueTitleRE.FindStringSubmatch(i.Title)
		if m == nil || handled[i.Title] {
			continue // not one forge filed, or the record decided it
		}
		reason := s.goneReason(ctx, m[1], m[2])
		if reason == "" {
			last := lastEvidence(i)
			if last.IsZero() || now.Sub(last) < s.o.StaleAfter {
				continue
			}
			reason = fmt.Sprintf("it has not flaked again for %d days (last seen %s), past --stale-after %s",
				int(now.Sub(last)/(24*time.Hour)), last.UTC().Format("2006-01-02"), FormatStaleAfter(s.o.StaleAfter))
		}
		s.close(ctx, i, reason)
	}
}

// goneReason says why the test of an issue is gone from the checkout at
// Root, or "" when it is there or that cannot be proved. Only a complete scan
// proves absence: the package directory exists and every one of its test
// files was read and parsed without finding the function. A missing
// directory (a partial checkout, a package of another module), or a file
// that cannot be read or parsed, is indeterminate and leaves the issue open.
func (s *issueSync) goneReason(ctx context.Context, name, short string) string {
	if s.module == "" {
		return ""
	}
	rel := "."
	if short != s.module {
		rel = filepath.FromSlash(short)
	}
	if !filepath.IsLocal(rel) {
		return ""
	}
	top, _, _ := strings.Cut(name, "/") // a subtest lives in its top-level test
	if present, known := testFuncPresent(filepath.Join(s.o.Root, rel), top); present || !known {
		return ""
	}
	at := ""
	if head, err := ExecGit(ctx, s.o.Root, "rev-parse", "--short=12", "HEAD"); err == nil && head != "" {
		at = " at `" + head + "`"
	}
	return fmt.Sprintf("%#q no longer exists in %#q%s", top, short, at)
}

// testFuncPresent reports whether a top-level func name is declared in the
// *_test.go files of dir. known is false when that cannot be told: dir is
// not a readable directory, or a test file could not be read or parsed.
func testFuncPresent(dir, name string) (present, known bool) {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return false, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, false
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			return false, false
		}
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return false, false
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == name {
				return true, true
			}
		}
	}
	return false, true
}

// lastEvidence is the newest time an issue was seen flaking: its creation,
// or the newest evidence comment forge wrote on it.
func lastEvidence(i ghIssue) time.Time {
	last := i.CreatedAt
	for _, c := range i.Comments {
		if strings.HasPrefix(strings.TrimSpace(c.Body), evidencePrefix) && c.CreatedAt.After(last) {
			last = c.CreatedAt
		}
	}
	return last
}

// ParseStaleAfter parses --stale-after: a whole number of days ("7d") or a
// Go duration ("168h"). It must be positive.
func ParseStaleAfter(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	var d time.Duration
	if n, ok := strings.CutSuffix(v, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, fmt.Errorf("--stale-after %q: want a whole number of days (7d) or a duration (168h)", v)
		}
		d = time.Duration(days) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(v); err != nil {
			return 0, fmt.Errorf("--stale-after %q: want a whole number of days (7d) or a duration (168h)", v)
		}
	}
	if d <= 0 {
		return 0, fmt.Errorf("--stale-after %q: must be positive", v)
	}
	return d, nil
}

// FormatStaleAfter prints a StaleAfter the way ParseStaleAfter reads it:
// whole days as "7d", anything else as a Go duration.
func FormatStaleAfter(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
	return d.String()
}

type issueSync struct {
	o        IssueOptions
	repo     string
	module   string
	res      IssueResult
	labelled bool
}

func (s *issueSync) say(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	s.res.Messages = append(s.res.Messages, msg)
	fmt.Fprintf(s.o.Stdout, "forge test flakes --issues: %s\n", msg)
}

// file opens t's issue, or brings the existing one up to date.
func (s *issueSync) file(ctx context.Context, t testreport.FlakyTest) {
	o, title := s.o, IssueTitle(t.ID, s.module)
	if !s.labelled && !o.DryRun {
		_, _ = o.GH(ctx, "label", "create", LabelSuspected, "-R", s.repo, "--color", "FBCA04",
			"--description", "A test failed, then passed on a retry of the same tree")
		s.labelled = true
	}
	existing, found := findIssue(ctx, o.GH, s.repo, t.ID.Name, title)
	if !found {
		if o.DryRun {
			s.say("--dry-run: would open an issue for %s", t.ID)
			return
		}
		out, err := o.GH(ctx, "issue", "create", "-R", s.repo, "--title", title, "--label", LabelSuspected, "--body", issueBody(o.Root, s.module, t))
		if err != nil {
			s.say("could not open an issue for %s: %v", t.ID.Name, err)
			return
		}
		url := strings.TrimSpace(string(out))
		s.res.URLs[t.ID.String()] = url
		s.res.Opened = append(s.res.Opened, url)
		s.say("opened %s for %s", url, t.ID.Name)
		return
	}
	s.res.URLs[t.ID.String()] = existing.URL
	fresh := newEvents(t.Events, existing.text())
	if len(fresh) == 0 && existing.State != "CLOSED" {
		return
	}
	if o.DryRun {
		s.say("--dry-run: would update #%d (%s)", existing.Number, t.ID.Name)
		return
	}
	num := fmt.Sprint(existing.Number)
	if existing.State == "CLOSED" {
		if _, err := o.GH(ctx, "issue", "reopen", num, "-R", s.repo); err != nil {
			s.say("could not reopen #%d: %v", existing.Number, err)
		} else {
			s.say("reopened #%d (%s)", existing.Number, t.ID.Name)
		}
	}
	if len(fresh) > 0 {
		if _, err := o.GH(ctx, "issue", "comment", num, "-R", s.repo, "--body", evidencePrefix+":\n\n"+eventLines(fresh)); err != nil {
			s.say("could not comment on #%d: %v", existing.Number, err)
			return
		}
	}
	s.res.Updated = append(s.res.Updated, existing.URL)
	s.say("commented on #%d (%s)", existing.Number, t.ID.Name)
}

// close closes an open issue, saying why: its test left suspected and
// flaky, or the sweep found it stale or gone.
func (s *issueSync) close(ctx context.Context, existing ghIssue, reason string) {
	if s.o.DryRun {
		s.say("--dry-run: would close #%d: %s", existing.Number, reason)
		return
	}
	if _, err := s.o.GH(ctx, "issue", "close", fmt.Sprint(existing.Number), "-R", s.repo, "--reason", "not planned",
		"--comment", "Closed by `forge test flakes --issues`: "+reason+". A new flake of this test reopens it."); err != nil {
		s.say("could not close #%d: %v", existing.Number, err)
		return
	}
	s.res.Closed = append(s.res.Closed, existing.URL)
	s.say("closed #%d: %s", existing.Number, reason)
}

// IssueTitle is the issue title of a test's flake: flake-report.sh's.
func IssueTitle(id testreport.TestID, module string) string {
	short := strings.TrimPrefix(id.Package, module+"/")
	if module == "" {
		short = id.Package
	}
	return fmt.Sprintf("flake:suspected: %s (%s)", id.Name, short)
}

// findIssue is the newest issue for a test in any state: a closed one is
// reopened, never duplicated.
func findIssue(ctx context.Context, gh GH, repo, test, title string) (ghIssue, bool) {
	out, err := gh(ctx, "issue", "list", "-R", repo, "--label", LabelSuspected, "--state", "all",
		"--search", fmt.Sprintf("%q in:title", test), "--json", "number,title,state,url,body,comments")
	if err != nil {
		return ghIssue{}, false
	}
	var issues []ghIssue
	if json.Unmarshal(out, &issues) != nil {
		return ghIssue{}, false
	}
	for _, i := range issues {
		if i.Title == title {
			return i, true
		}
	}
	return ghIssue{}, false
}

func closeReason(t testreport.FlakyTest) string {
	switch t.State {
	case testreport.StateQuarantined:
		if t.Quarantine != nil {
			return "it is quarantined (`" + t.Quarantine.Path + "`)"
		}
		return "it is quarantined"
	case testreport.StateRemoved:
		return "the test no longer runs (deleted)"
	case testreport.StateFixed:
		return "its file changed and it has not flaked since"
	}
	return "it has not flaked again in 14 days"
}

// runMarker names a run in an issue, so a later sync can tell which runs the
// issue already mentions.
func runMarker(e testreport.FlakeEvent) string { return "run `" + e.Run + "`" }

func newEvents(evs []testreport.FlakeEvent, text string) []testreport.FlakeEvent {
	var out []testreport.FlakeEvent
	for _, e := range evs {
		if !strings.Contains(text, runMarker(e)) {
			out = append(out, e)
		}
	}
	return out
}

func eventLines(evs []testreport.FlakeEvent) string {
	var b strings.Builder
	for _, e := range evs {
		fmt.Fprintf(&b, "- %s, lane `%s`, commit `%s`, %s, %s", runMarker(e), e.Lane, short(e.Commit), e.Signal, e.At.UTC().Format("2006-01-02 15:04Z"))
		if e.Signature != "" {
			fmt.Fprintf(&b, ", signature `%s`", e.Signature)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func issueBody(root, module string, t testreport.FlakyTest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "`%s` in `%s` failed and then **passed** on the same tree (state: %s, flake rate %.3f with n=%d).\n\n", t.ID.Name, t.ID.Package, t.State, t.Score, t.N)
	sig := ""
	for _, e := range t.Events {
		if e.Signature != "" {
			sig = e.Signature
		}
	}
	if sig != "" {
		fmt.Fprintf(&b, "### Failure signature\n\n`%s`\n\n", sig)
	}
	b.WriteString("### Runs\n\n")
	b.WriteString(eventLines(t.Events))
	if body := TestBody(root, module, t.ID); body != "" {
		fmt.Fprintf(&b, "\n### The test\n\n```go\n%s\n```\n", body)
	}
	b.WriteString("\n### What to do\n\nOwner policy: flaky = delete immediately; rewrite as a new deterministic test if it is worth having. ")
	b.WriteString("Delete the test (or its flaky assertion), or quarantine it now with `forge test quarantine add " + t.ID.String() + "`. ")
	b.WriteString("If the failure is the host (inotify, fd or disk budget, a download), make the test skip with a message naming the missing resource.\n\n")
	b.WriteString("Filed by `forge test flakes --issues`; later occurrences are added as comments.\n")
	return b.String()
}

// maxTestBodyLines bounds the test source an issue carries.
const maxTestBodyLines = 150

// TestBody is the source of a Go test function in the module at root, from
// its `func` line to its closing brace (bounded), or "" when it is not found.
func TestBody(root, module string, id testreport.TestID) string {
	rel, ok := relPackage(id.Package, module)
	if !ok {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(rel), "*_test.go"))
	decl := regexp.MustCompile(`^func ` + regexp.QuoteMeta(id.Name) + `\(`)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		for i, l := range lines {
			if !decl.MatchString(l) {
				continue
			}
			var out []string
			for j := i; j < len(lines); j++ {
				out = append(out, strings.TrimRight(lines[j], "\r"))
				if strings.TrimRight(lines[j], "\r") == "}" {
					break
				}
				if len(out) == maxTestBodyLines {
					out = append(out, "\t// … truncated")
					break
				}
			}
			return strings.Join(out, "\n")
		}
	}
	return ""
}
