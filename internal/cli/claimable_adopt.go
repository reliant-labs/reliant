package cli

// Adopting an app that has no forge.yaml, for a claimable deploy.
//
// `forge env deploy <env> --claimable` and the deploy_claimable MCP tool both
// start from a bare Vite or Next.js app (or an app with its own Dockerfile):
// the decision of what to do with the directory, the refusals and the
// `forge project new --in-place` invocation live here, so the two entry points
// cannot drift. Only HOW the command runs differs: the tool runs it through
// its serialized in-process runner, the CLI runs it directly.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/config"
)

// claimableAdoptionPlan is the verdict on a directory: nothing to do (it holds
// a forge.yaml), adopt it with `forge project new --in-place <args>`, or refuse.
type claimableAdoptionPlan struct {
	adopt bool
	name  string
	// flags are what follows `forge project new` to adopt the directory.
	flags   []string
	refusal *claimableAdoptionRefusal
}

// claimableAdoptionRefusal is why dir cannot be adopted, and the command to
// run instead.
type claimableAdoptionRefusal struct {
	msg  string
	next string
}

// planClaimableAdoption decides what a claimable deploy does with dir. A
// directory with a forge.yaml is already a project. A Vite or Next.js app at
// the root is adopted as a static site, an app with its own Dockerfile as
// `--kind app --claimable`; a Go module or anything else is refused.
func planClaimableAdoption(dir, name string) claimableAdoptionPlan {
	if _, err := os.Stat(filepath.Join(dir, config.ProjectConfigFile)); err == nil {
		return claimableAdoptionPlan{}
	}
	name = adoptionName(dir, name)
	kindArgs := []string{"--kind", "site"}
	_, site := siteFrontendAt(dir)
	switch {
	case site:
	case hasAppToAdopt(dir):
		kindArgs = []string{"--kind", "app", "--claimable"}
	default:
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			next := fmt.Sprintf("%s project new %s --service api --claimable", Name(), name)
			return claimableAdoptionPlan{refusal: &claimableAdoptionRefusal{
				msg: fmt.Sprintf("%s is a Go module with no forge.yaml and no Dockerfile. deploy_claimable adopts a static "+
					"Vite or Next.js app, or an app with its own Dockerfile, in place. Add a Dockerfile that builds it, or scaffold a "+
					"forge project sized for the claimable plan in a new directory, move the code into its service, then deploy it: %s", dir, next),
				next: next,
			}}
		}
		next := fmt.Sprintf("%s project new %s --kind site", Name(), name)
		return claimableAdoptionPlan{refusal: &claimableAdoptionRefusal{
			msg: fmt.Sprintf("%s has no forge.yaml, no Vite or Next.js app at its root "+
				"(a package.json that depends on vite or next) and no Dockerfile, so there is nothing to adopt. Start a site, or run "+
				"deploy_claimable in the app's own directory: %s", dir, next),
			next: next,
		}}
	}
	return claimableAdoptionPlan{
		adopt: true,
		name:  name,
		flags: append(append([]string{"--in-place"}, kindArgs...), "--name", name),
	}
}

// command is the full `forge project new ...` argv that adopts the directory.
func (p claimableAdoptionPlan) command() []string {
	return append([]string{"project", "new"}, p.flags...)
}

// failure is the message and the command to retry when running the plan's
// `project new` failed with err after printing out.
func (p claimableAdoptionPlan) failure(out string, err error) (msg, next string) {
	return "adopting the app failed: " + strings.TrimSpace(out+"\n"+err.Error()),
		fmt.Sprintf("%s %s", Name(), strings.Join(p.command(), " "))
}

// adoptBeforeClaimableDeploy is the CLI's half: `env deploy <env> --claimable`
// in a directory that is not yet a forge project adopts the app in place, or
// refuses with the MCP tool's message. A directory inside a project (forge.yaml
// in a parent) is left to the ordinary path.
//
// It returns the paths (relative to the app's root) it created or changed, for
// the release decision to name: git has not seen them yet.
func adoptBeforeClaimableDeploy(ctx context.Context, out io.Writer) ([]string, error) {
	if _, err := findProjectConfigFile(); !errors.Is(err, ErrProjectConfigNotFound) {
		return nil, nil
	}
	dir, err := cmdutil.ResolutionRoot()
	if err != nil {
		return nil, err
	}
	plan := planClaimableAdoption(dir, "")
	if plan.refusal != nil {
		return nil, &claimableRefusal{exit: exitWrong, code: cloud.CodeInvalidArgument, msg: plan.refusal.msg, next: []string{plan.refusal.next}}
	}
	if !plan.adopt {
		return nil, nil
	}
	before := snapshotAppFiles(dir)
	captured, err := runProjectNewQuietly(ctx, dir, plan.flags)
	if err != nil {
		msg, next := plan.failure(captured, err)
		return nil, &claimableRefusal{exit: exitCodeForError(err), code: cloud.CodeInvalidArgument, msg: msg + "\n  run instead: " + next, next: []string{next}}
	}
	_, _ = fmt.Fprintf(out, "  Adopted this app as forge project %q: added forge.yaml and deploy/kcl/ (and forge's .gitignore entries); the app's own files are untouched.\n", plan.name)
	return changedAppFiles(dir, before), nil
}

// skipSnapshotDir holds what no adoption touches and a walk should not pay for.
var skipSnapshotDir = map[string]bool{".git": true, "node_modules": true, "dist": true, "out": true, ".next": true, ".forge": true}

// snapshotAppFiles maps each file under dir to its size and mtime.
func snapshotAppFiles(dir string) map[string]string {
	snap := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && skipSnapshotDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		snap[filepath.ToSlash(rel)] = fmt.Sprintf("%d/%d", info.Size(), info.ModTime().UnixNano())
		return nil
	})
	return snap
}

// changedAppFiles lists what is new or different under dir since before.
func changedAppFiles(dir string, before map[string]string) []string {
	var out []string
	for rel, sig := range snapshotAppFiles(dir) {
		if prev, ok := before[rel]; !ok || prev != sig {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

type adoptedPathsKey struct{}

// withAdoptedPaths carries the files this run's adoption wrote down to the
// release decision.
func withAdoptedPaths(ctx context.Context, paths []string) context.Context {
	if len(paths) == 0 {
		return ctx
	}
	return context.WithValue(ctx, adoptedPathsKey{}, paths)
}

func adoptedPathsFrom(ctx context.Context) []string {
	paths, _ := ctx.Value(adoptedPathsKey{}).([]string)
	return paths
}

// onlyAdoptedPaths reports whether every uncommitted path is one adoption just
// wrote. changes are relative to the git top, adopted to the app's root, so a
// project below the top matches by suffix.
func onlyAdoptedPaths(changes, adopted []string) bool {
	if len(changes) == 0 || len(adopted) == 0 {
		return false
	}
	for _, c := range changes {
		found := false
		for _, a := range adopted {
			if c == a || strings.HasSuffix(c, "/"+a) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// processStdioMu serializes the one place the CLI path swaps the process's cwd
// and stdio, so no two adoptions (or an adoption and the MCP runner, which has
// its own gate) interleave their swaps.
var processStdioMu sync.Mutex

// runProjectNewQuietly runs `forge project new <args>` in dir and returns what
// it printed instead of printing it: a deploy owes its caller one line about
// the adoption, not the scaffolder's whole report.
//
// The scaffolder prints with fmt.Println and resolves the project from the
// working directory, so cwd and stdio are swapped for the call. Every swap is
// undone by a defer, so a panic in the command cannot leave the process in the
// user's app directory with its output going to a capture pipe.
func runProjectNewQuietly(ctx context.Context, dir string, args []string) (captured string, err error) {
	processStdioMu.Lock()
	defer processStdioMu.Unlock()

	capture, err := newOutputCapture(nil)
	if err != nil {
		return "", err
	}
	closed := false
	defer func() {
		if !closed {
			captured = capture.close()
		}
	}()
	prevWd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if err := os.Chdir(dir); err != nil {
		return "", err
	}
	defer func() { _ = os.Chdir(prevWd) }()
	prevOut, prevErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = capture.w, capture.w
	defer func() { os.Stdout, os.Stderr = prevOut, prevErr }()

	cmd := newNewCmd()
	cmd.SetArgs(args)
	cmd.SetOut(capture.w)
	cmd.SetErr(capture.w)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	runErr := cmd.ExecuteContext(ctx)

	os.Stdout, os.Stderr = prevOut, prevErr
	closed = true
	return capture.close(), runErr
}
