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
// account (its `labeled` event names the actor; APIGATE_OWNERS, default
// reliant-01). An accepted break still carries the cost of its consumers'
// migration: declared, with its codemod or playbook. See dev-docs/api-breaks.md.
//
// The converse fails too: an `API-BREAK:` line in the PR body or a commit body
// when the diff has no incompatible change. The squash commit keeps the commit
// bodies, and the release (scripts/semver-next.sh) refuses on that line, so a
// stale marker (forge #1880: a removal its own later commit undid) blocks every
// release. The owner's breaking:approved label waives it.
//
// WHERE THE DECLARATION COMES FROM. With -pr, the label list and body are read
// live through `gh api` (GH_TOKEN, GITHUB_REPOSITORY), not from the workflow
// event: a label added after the run started, then "Re-run failed jobs", is
// seen. Locally, pass -label and -body-file instead.
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
	"strings"

	"github.com/reliant-labs/forge/internal/forgereach"
)

var errStaleMarker = errors.New("an API-BREAK: marker with no incompatible change to forge/pkg behind it")

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "apigate:", err)
		os.Exit(1)
	}
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
	out, err := ghAPI(fmt.Sprintf("repos/%s/pulls/%s", repo, pr))
	if err != nil {
		return "", nil, false, fmt.Errorf("read PR %s from GitHub: %w", pr, err)
	}
	var p struct {
		Body   string
		Labels []struct{ Name string }
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return "", nil, false, fmt.Errorf("decode PR %s: %w", pr, err)
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

// ghAPI runs `gh api <args>` and returns its stdout; tests replace it with a
// fake GitHub.
var ghAPI = func(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", append([]string{"api"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// ownerApplied reports whether the last account to apply label to the PR is
// one of APIGATE_OWNERS (default reliant-01, the owner). It reads every page of
// the PR's events: `gh api --paginate` prints one JSON array per page.
func ownerApplied(repo, pr, label string) (bool, error) {
	out, err := ghAPI("--paginate", fmt.Sprintf("repos/%s/issues/%s/events", repo, pr))
	if err != nil {
		return false, fmt.Errorf("read PR %s's label events from GitHub: %w", pr, err)
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
			return false, fmt.Errorf("decode PR %s's label events: %w", pr, err)
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
		owners = "reliant-01"
	}
	for _, o := range strings.FieldsFunc(owners, func(r rune) bool { return r == ',' || r == ' ' }) {
		if o == last {
			return true, nil
		}
	}
	return false, nil
}
