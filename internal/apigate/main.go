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
// forge is pre-1.0 and owes no compatibility; this gate does not forbid a
// break, it makes the breaking PR carry the cost of its consumers' migration
// instead of the next pinner. See dev-docs/api-breaks.md.
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
	if !reach.API() {
		fmt.Fprintf(stdout, "apigate: ok — the diff touches nothing the API of pkg/ depends on (%s)\n", reach.Summary())
		return nil
	}

	text, lbl, err := declarationInputs(root, *base, *pr, *bodyFile, labels)
	if err != nil {
		return err
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

	v := Evaluate(breaks, ParseDeclaration(text, lbl), changed, root)
	if !v.OK {
		fmt.Fprintln(stderr, "apigate: FAIL —", v.Reason)
		return errors.New("incompatible change to forge/pkg is not declared with its codemod")
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

func declarationInputs(root, base, pr, bodyFile string, labels []string) (string, []string, error) {
	var text strings.Builder
	if out, err := forgereach.Git(root, "log", "--format=%B", base+"..HEAD"); err == nil {
		text.Write(out)
	}
	switch {
	case pr != "":
		repo := os.Getenv("GITHUB_REPOSITORY")
		if repo == "" {
			repo = "reliant-labs/forge"
		}
		cmd := exec.Command("gh", "api", fmt.Sprintf("repos/%s/pulls/%s", repo, pr))
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return "", nil, fmt.Errorf("read PR %s from GitHub: %w\n%s", pr, err, strings.TrimSpace(stderr.String()))
		}
		var p struct {
			Body   string
			Labels []struct{ Name string }
		}
		if err := json.Unmarshal(out, &p); err != nil {
			return "", nil, fmt.Errorf("decode PR %s: %w", pr, err)
		}
		text.WriteString("\n" + p.Body)
		for _, l := range p.Labels {
			labels = append(labels, l.Name)
		}
	case bodyFile != "":
		b, err := os.ReadFile(bodyFile)
		if err != nil {
			return "", nil, err
		}
		text.WriteString("\n" + string(b))
	}
	return text.String(), labels, nil
}
