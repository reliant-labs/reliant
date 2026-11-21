package cli

// `forge vuln patch --pr`: commit a verified patch on its own branch, push
// it, and open the pull request with gh, as the user.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/vulnpatch"
	"github.com/reliant-labs/forge/internal/vulnscan"
)

// dirtyTree is why --pr must refuse the tree at dir, "" when it is a clean
// git checkout. --pr commits everything that changed under dir, so anything
// already uncommitted would ride along in a security PR.
func dirtyTree(ctx context.Context, run vulnscan.Runner, dir string) string {
	out, err := runCapture(ctx, run, dir, "git", "status", "--porcelain")
	if err != nil {
		return fmt.Sprintf("%s is not a git checkout --pr can commit in (git status: %v)", dir, err)
	}
	if lines := nonEmptyLines(out); len(lines) > 0 {
		return fmt.Sprintf("the tree has uncommitted changes (%d path(s), e.g. %s): --pr commits only the patch, so commit or stash them first, or use --no-pr",
			len(lines), strings.TrimSpace(lines[0]))
	}
	return ""
}

// openPatchPR commits the patched tree on a new branch, pushes it and opens
// the pull request. It opens none when nothing changed or the patch did not
// verify: a red patch is a report, not a PR.
func openPatchPR(ctx context.Context, deps vulnPatchDeps, tree, projectDir string, doc vulnpatch.Document, branch string) (*vulnpatch.PRResult, string, error) {
	if !doc.Changed {
		return nil, "nothing changed, so no pull request", nil
	}
	if doc.ExitCode == vulnpatch.ExitVerify {
		return &vulnpatch.PRResult{}, "the patch did not verify, so no pull request: the changes are left in the tree", nil
	}
	run := deps.exec
	if branch == "" {
		short, err := runCapture(ctx, run, tree, "git", "rev-parse", "--short", "HEAD")
		if err != nil {
			return nil, "", fmt.Errorf("--pr: read HEAD: %w", err)
		}
		branch = fmt.Sprintf("forge/vuln-patch/%s-%s", deps.now().UTC().Format("20060102"), strings.TrimSpace(short))
	}
	pr := &vulnpatch.PRResult{Branch: branch}
	title := patchPRTitle(doc)
	body := patchPRBody(doc, patchProjectName(projectDir))
	for _, argv := range [][]string{
		{"git", "switch", "-c", branch},
		{"git", "add", "-A", "--", "."},
		{"git", "commit", "-m", title, "-m", patchCommitBody(doc)},
	} {
		if _, err := runCapture(ctx, run, tree, argv[0], argv[1:]...); err != nil {
			return pr, "", fmt.Errorf("--pr: %w", err)
		}
	}
	pr.Committed = true
	if _, err := runCapture(ctx, run, tree, "git", "push", "-u", "origin", branch); err != nil {
		return pr, "", fmt.Errorf("--pr: the patch is committed on %s, but the push failed: %w", branch, err)
	}
	bodyDir, err := os.MkdirTemp("", "forge-vuln-patch-")
	if err != nil {
		return pr, "", err
	}
	defer func() { _ = os.RemoveAll(bodyDir) }()
	bodyFile := filepath.Join(bodyDir, "body.md")
	if err := atomicfile.WriteFile(bodyFile, []byte(body), 0o600); err != nil {
		return pr, "", err
	}
	out, err := runCapture(ctx, run, tree, "gh", "pr", "create", "--head", branch, "--title", title, "--body-file", bodyFile)
	if err != nil {
		return pr, "", fmt.Errorf("--pr: %s is pushed, but gh pr create failed: %w", branch, err)
	}
	if lines := nonEmptyLines(out); len(lines) > 0 {
		pr.URL = strings.TrimSpace(lines[len(lines)-1])
	}
	return pr, "", nil
}

func patchPRTitle(doc vulnpatch.Document) string {
	var bumped []vulnpatch.Result
	for _, r := range doc.Findings {
		if r.Target != "" && (r.Status == vulnpatch.StatusFixed || r.Status == vulnpatch.StatusBumped) {
			bumped = append(bumped, r)
		}
	}
	if len(bumped) == 1 {
		r := bumped[0]
		return fmt.Sprintf("fix(deps): bump %s to %s (%s)", r.Package.Name, r.Target, r.ID)
	}
	where := ""
	if doc.Env != "" {
		where = " in " + doc.Env
	}
	return fmt.Sprintf("fix(deps): bump %d vulnerable dependencies%s", len(bumped), where)
}

func patchCommitBody(doc vulnpatch.Document) string {
	var b strings.Builder
	for _, r := range doc.Findings {
		if r.Target == "" || r.Status == vulnpatch.StatusSkipped {
			continue
		}
		fmt.Fprintf(&b, "%s: %s %s -> %s\n", r.ID, r.Package.Name, r.Installed, r.Target)
	}
	b.WriteString("\nBumped by forge vuln patch.")
	return b.String()
}

// patchPRBody is the pull request's description: each advisory, installed →
// fixed, the release and env it was found in, the verification, and the
// console link.
func patchPRBody(doc vulnpatch.Document, project string) string {
	var b strings.Builder
	b.WriteString("Bumps vulnerable dependencies to the lowest version that fixes them, with each ecosystem's own tool (`forge vuln patch`).\n\n")
	if doc.Env != "" {
		fmt.Fprintf(&b, "Found in release `%s`, which env `%s` runs.\n\n", doc.Release, doc.Env)
	}
	b.WriteString("| Advisory | Severity | Package | Installed → fixed | Result |\n|---|---|---|---|---|\n")
	for _, r := range doc.Findings {
		change := "`" + r.Installed + "`"
		if r.Target != "" {
			change += " → `" + r.Target + "`"
		}
		result := string(r.Status)
		if r.Reason != "" {
			result += ": " + r.Reason
		}
		fmt.Fprintf(&b, "| %s | %s | `%s` | %s | %s |\n", r.ID, r.Severity, r.Package.Name, change, strings.ReplaceAll(result, "|", `\|`))
	}
	if v := doc.Verify; v != nil {
		b.WriteString("\n### Verification\n\n")
		if v.Command != "" {
			state := "passed"
			if !v.Passed {
				state = "failed"
			}
			fmt.Fprintf(&b, "`%s` (%s): %s.\n", v.Command, v.Source, state)
		}
		if len(v.Rescanned) > 0 {
			fmt.Fprintf(&b, "Rescanned: %s.\n", strings.Join(v.Rescanned, ", "))
		}
		if v.Output != "" {
			fmt.Fprintf(&b, "\n<details><summary>Output</summary>\n\n```\n%s\n```\n</details>\n", v.Output)
		}
	} else {
		b.WriteString("\nNot verified (`forge vuln patch` ran without `--verify`).\n")
	}
	if project != "" {
		fmt.Fprintf(&b, "\nFindings: %s/forge/p/%s/vulnerabilities\n", defaultForgeConsole, project)
	}
	return b.String()
}

// runCapture runs a command in dir and returns its stdout; an error carries
// its stderr.
func runCapture(ctx context.Context, run vulnscan.Runner, dir, name string, args ...string) (string, error) {
	var out, errOut bytes.Buffer
	if err := run(ctx, vulnscan.Command{Path: name, Args: args, Dir: dir, Stdout: &out, Stderr: &errOut}); err != nil {
		if msg := strings.TrimSpace(errOut.String()); msg != "" {
			return out.String(), fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, vulnpatch.Tail(msg, 1000))
		}
		return out.String(), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out.String(), nil
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
