// Command apigate is the Go API-compatibility gate for forge/pkg: the packages
// every generated project imports. Run it with `go run ./internal/apigate`; it
// is CI plumbing, no part of the forge binary.
//
//	apigate [-base REV] [-dir .] [-pr N] [-body-file FILE] [-label L]...
//
// It compares the exported API under pkg/ at the merge base with the working
// tree, using golang.org/x/exp/apidiff, and fails on an incompatible change
// unless the change is DECLARED and ships its remedy:
//
//   - declared: the PR carries the `api-break` label, or a line
//     `API-BREAK: [codemod=<name>] [playbook=migrations/<vX.Y.Z>]` appears in
//     the PR body or a commit body;
//   - shipped: the same diff adds a `forge fix <name>` codemod (a non-test file
//     in internal/codemod registering that name) or touches a migration
//     playbook (internal/templates/project/skills/forge/migrations/<version>/).
//
// forge is semver at major 1 (owner, 2026-11-05), so a break fails unless the
// OWNER accepted it: the breaking:approved label, applied by the owner's
// account or by the Reliant bot on the owner's instruction (its `labeled`
// event names the actor; APIGATE_OWNERS, default reliant-01 and
// reliant-labs[bot]: agents label as the bot, and the owner decided on
// 2026-11-19 to "let the bot's label count"). An accepted break still carries the cost of its consumers'
// migration: declared, with its codemod or playbook. See dev-docs/api-breaks.md.
//
// The converse fails too: an `API-BREAK:` line in the PR body or a commit body
// when the diff has no incompatible change. The squash commit keeps the commit
// bodies, and the release (scripts/semver-next.sh) refuses on that line, so a
// stale marker (forge #1880: a removal its own later commit undid) blocks every
// release. The owner's breaking:approved label waives it.
//
// WHERE THE DECLARATION COMES FROM. With -pr, the label list and body are read
// live through `gh api` (GITHUB_REPOSITORY; APIGATE_GH may name an equivalent
// read-only client), not from the workflow event: a label added after the run
// started, then "Re-run failed jobs", is seen. Locally, pass -label and
// -body-file instead.
//
// A failure to READ that declaration (a GitHub rate limit, a 5xx, an
// unreachable API) is infrastructure, not a verdict: apigate says so, names the
// cause, and exits 3 instead of 1. It still fails, because without the labels
// and body it cannot tell a declared, owner-accepted break from an undeclared
// one, or find a stale API-BREAK: marker.
//
//forge:exclude-contract: a CI command, no collaborator to substitute
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/forgereach"
)

var errStaleMarker = errors.New("an API-BREAK: marker with no incompatible change to forge/pkg behind it")

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// exitInfrastructure is the exit status when GitHub could not be read: the
// gate reached no verdict, which a caller must not mistake for a break.
const exitInfrastructure = 3

func main() {
	err := run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(report(err, os.Stdout, os.Stderr, os.Getenv("GITHUB_ACTIONS") == "true"))
}

// report prints run's error and returns the exit status: 0, 1 for a verdict
// (or a usage or git failure), exitInfrastructure when GitHub could not be read.
func report(err error, stdout, stderr io.Writer, inActions bool) int {
	if err == nil {
		return 0
	}
	fmt.Fprintln(stderr, "apigate:", err)
	var gh *githubReadError
	if !errors.As(err, &gh) {
		return 1
	}
	fmt.Fprintln(stderr, "apigate: nothing in the diff was judged incompatible. The gate needs the pull request's labels and body"+
		" (an api-break declaration, the owner's breaking:approved) to judge pkg/, so it fails closed; it runs again on the next push.")
	if inActions {
		// An error annotation, so the run's summary names the cause without
		// opening the log.
		esc := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")
		fmt.Fprintf(stdout, "::error title=pkg/ API gate could not read the pull request (infrastructure)::%s\n", esc.Replace(gh.Error()))
	}
	return exitInfrastructure
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("apigate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "HEAD^1", "git revision to compare against (the merge base or the base branch tip)")
	dir := fs.String("dir", ".", "the forge checkout to judge")
	pr := fs.String("pr", "", "pull request number: read its labels and body from GitHub (gh, GH_TOKEN)")
	bodyFile := fs.String("body-file", "", "file holding the PR body, instead of -pr")
	var labels stringList
	fs.Var(&labels, "label", "a PR label (repeatable), instead of -pr")
	ownerApproved := fs.Bool("owner-approved", false, "the owner accepted the break (local runs; with -pr it is read from the label's events)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}

	changed, err := forgereach.ChangedFiles(root, *base)
	if err != nil {
		return err
	}
	reach, err := forgereach.Compute(root, *base, changed)
	if err != nil {
		return err
	}
	text, lbl, approved, err := declarationInputs(root, *base, *pr, *bodyFile, labels)
	if err != nil {
		return err
	}
	decl := ParseDeclaration(text, lbl)
	decl.OwnerApproved = approved || *ownerApproved

	if !reach.API() {
		// No API change is possible, so the one failure left is an API-BREAK:
		// marker with nothing behind it.
		if v := Evaluate(nil, decl, changed, root); !v.OK {
			fmt.Fprintln(stderr, "apigate: FAIL —", v.Reason)
			return errStaleMarker
		}
		fmt.Fprintf(stdout, "apigate: ok — the diff touches nothing the API of pkg/ depends on (%s)\n", reach.Summary())
		return nil
	}

	tmp, err := os.MkdirTemp("", "apigate-base-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := forgereach.ExportRev(root, *base, tmp); err != nil {
		return err
	}
	breaks, compatible, err := diffAPI(tmp, root)
	if err != nil {
		return err
	}

	v := Evaluate(breaks, decl, changed, root)
	if !v.OK {
		fmt.Fprintln(stderr, "apigate: FAIL —", v.Reason)
		if len(breaks) == 0 {
			return errStaleMarker
		}
		return errors.New("incompatible change to forge/pkg is not an owner-accepted break declared with its codemod")
	}
	if len(breaks) == 0 {
		fmt.Fprintf(stdout, "apigate: ok — no incompatible change to forge/pkg (%d compatible)\n", compatible)
		return nil
	}
	fmt.Fprintf(stdout, "apigate: ok — %d incompatible change(s) to forge/pkg, declared and shipped with their remedy:\n", len(breaks))
	for _, b := range breaks {
		fmt.Fprintf(stdout, "  - %s\n", b)
	}
	return nil
}

// localDeclarationText is the declaration text read without GitHub: the commit
// bodies of base..HEAD (what the squash commit keeps) and -body-file.
func localDeclarationText(root, base, bodyFile string) (string, error) {
	var text strings.Builder
	if out, err := forgereach.Git(root, "log", "--format=%B", base+"..HEAD"); err == nil {
		text.Write(out)
	}
	if bodyFile != "" {
		b, err := os.ReadFile(bodyFile)
		if err != nil {
			return "", err
		}
		text.WriteString("\n" + string(b))
	}
	return text.String(), nil
}

// declarationInputs returns the PR text, its labels, and whether the owner
// applied ApprovedLabel (only -pr can say so).
func declarationInputs(root, base, pr, bodyFile string, labels []string) (string, []string, bool, error) {
	if pr == "" {
		text, err := localDeclarationText(root, base, bodyFile)
		return text, labels, false, err
	}
	text, err := localDeclarationText(root, base, "")
	if err != nil {
		return "", nil, false, err
	}
	body, prLabels, approved, err := prDeclaration(pr)
	if err != nil {
		return "", nil, false, err
	}
	return text + "\n" + body, append(labels, prLabels...), approved, nil
}

// prDeclaration reads pull request pr's body and labels from GitHub, and
// whether the owner applied ApprovedLabel: the label is on the PR now, and the
// last account to apply it is an owner.
func prDeclaration(pr string) (string, []string, bool, error) {
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		repo = "reliant-labs/forge"
	}
	what := "PR " + pr
	out, err := ghAPI(fmt.Sprintf("repos/%s/pulls/%s", repo, pr))
	if err != nil {
		return "", nil, false, newGitHubReadError(what, err)
	}
	var p struct {
		Body   string
		Labels []struct{ Name string }
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return "", nil, false, &githubReadError{what: what, cause: "GitHub API returned an unreadable response", err: err}
	}
	var labels []string
	approved := false
	for _, l := range p.Labels {
		labels = append(labels, l.Name)
		approved = approved || l.Name == ApprovedLabel
	}
	if approved {
		if approved, err = ownerApplied(repo, pr, ApprovedLabel); err != nil {
			return "", nil, false, err
		}
	}
	return p.Body, labels, approved, nil
}

// ghAPI runs `gh api <args>` and returns its stdout, or a *ghFailure; tests
// replace it with a fake GitHub.
var ghAPI = func(args ...string) ([]byte, error) {
	bin := os.Getenv("APIGATE_GH")
	if bin == "" {
		bin = "gh"
	}
	cmd := exec.Command(bin, append([]string{"api"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		f := &ghFailure{bin: bin, stderr: strings.TrimSpace(stderr.String()), err: err}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			f.started, f.exitCode = true, exit.ExitCode()
		}
		return nil, f
	}
	return out, nil
}

// ghFailure is a `gh api` (or APIGATE_GH) invocation that failed: whether it
// started, its exit status and what it printed on stderr.
type ghFailure struct {
	bin      string
	started  bool
	exitCode int
	stderr   string
	err      error
}

func (f *ghFailure) Error() string {
	if f.stderr == "" {
		return f.err.Error()
	}
	return f.err.Error() + "\n" + f.stderr
}

func (f *ghFailure) Unwrap() error { return f.err }

// githubReadError is a failure to read the pull request's labels, body or
// label events from GitHub. It is infrastructure: no verdict was reached, and
// main reports it apart from an incompatible change (exitInfrastructure).
type githubReadError struct {
	what  string // "PR 2459", "PR 2459's label events"
	cause string // what went wrong, classified: "GitHub API rate limit (HTTP 403)"
	err   error
}

func (e *githubReadError) Error() string {
	return fmt.Sprintf("infrastructure: %s reading %s from GitHub; this is not an API break: %v", e.cause, e.what, e.err)
}

func (e *githubReadError) Unwrap() error { return e.err }

func newGitHubReadError(what string, err error) *githubReadError {
	exitCode, stderr := 0, err.Error()
	var f *ghFailure
	if errors.As(err, &f) {
		if !f.started {
			return &githubReadError{what: what, cause: "cannot run the GitHub client " + f.bin, err: err}
		}
		exitCode, stderr = f.exitCode, f.stderr
	}
	return &githubReadError{what: what, cause: classifyGitHubFailure(exitCode, stderr), err: err}
}

// httpStatus finds the HTTP status in what gh ("HTTP 403: API rate limit
// exceeded"), curl ("The requested URL returned error: 403") or
// scripts/ci-public-gh.sh ("(HTTP 403)") print.
var httpStatus = regexp.MustCompile(`\b(?:HTTP|error:)\s*([1-5][0-9]{2})\b`)

// transportFailure matches a request that never got an HTTP answer, as curl
// and gh word it.
var transportFailure = regexp.MustCompile(`(?i)unreachable|could not resolve|no such host|failed to connect|connection (refused|reset)|timed out|i/o timeout|tls handshake`)

// classifyGitHubFailure names why a GitHub read failed, from the client's exit
// status and stderr. scripts/ci-public-gh.sh exits 75 (EX_TEMPFAIL) once its
// retries of a rate limit, 5xx or transport failure are spent.
func classifyGitHubFailure(exitCode int, stderr string) string {
	status := ""
	if m := httpStatus.FindStringSubmatch(stderr); m != nil {
		status = m[1]
	}
	switch {
	case status == "429" || strings.Contains(strings.ToLower(stderr), "rate limit"):
		if status == "" {
			return "GitHub API rate limit"
		}
		return "GitHub API rate limit (HTTP " + status + ")"
	case status == "403":
		// An anonymous read's 403 is almost always its rate limit, and curl
		// --fail drops the body that would say so.
		return "GitHub API refused the read (HTTP 403, most often a rate limit)"
	case strings.HasPrefix(status, "5"):
		return "GitHub API error (HTTP " + status + ")"
	case status != "":
		return "GitHub API returned HTTP " + status
	case transportFailure.MatchString(stderr):
		return "GitHub API unreachable"
	case exitCode == 75:
		return "GitHub API temporarily unavailable"
	}
	return "GitHub API read failed"
}

// defaultOwners are the accounts whose breaking:approved counts when
// APIGATE_OWNERS is unset: the owner, and the Reliant bot applying the label
// on the owner's instruction (owner decision, 2026-11-19). The same default as
// scripts/check-compat.sh's COMPAT_OWNERS.
const defaultOwners = "reliant-01,reliant-labs[bot]"

// ownerApplied reports whether the last account to apply label to the PR is
// one of APIGATE_OWNERS (default defaultOwners). It reads every page of the
// PR's events: `gh api --paginate` prints one JSON array per page.
func ownerApplied(repo, pr, label string) (bool, error) {
	what := "PR " + pr + "'s label events"
	out, err := ghAPI("--paginate", fmt.Sprintf("repos/%s/issues/%s/events", repo, pr))
	if err != nil {
		return false, newGitHubReadError(what, err)
	}
	type event struct {
		Event string
		Label struct{ Name string }
		Actor struct{ Login string }
	}
	last := ""
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var page []event
		if err := dec.Decode(&page); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return false, &githubReadError{what: what, cause: "GitHub API returned an unreadable response", err: err}
		}
		for _, e := range page {
			if e.Event == "labeled" && e.Label.Name == label {
				last = e.Actor.Login
			}
		}
	}
	if last == "" {
		return false, nil
	}
	owners := os.Getenv("APIGATE_OWNERS")
	if owners == "" {
		owners = defaultOwners
	}
	for _, o := range strings.FieldsFunc(owners, func(r rune) bool { return r == ',' || r == ' ' }) {
		if o == last {
			return true, nil
		}
	}
	return false, nil
}
