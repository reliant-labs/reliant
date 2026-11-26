package cli

// Shared plumbing of the record-reading `forge test` verbs (report, coverage,
// runs): finding the project and its runs, choosing which runs a command is
// about, and turning a verdict into an exit code.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/testlocal"
	skipcheck "github.com/reliant-labs/forge/internal/testreport"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// testGit is the git the test verbs read through; tests swap it.
var testGit testlocal.Git = testlocal.ExecGit

// testRecordRoot is the directory whose .forge/tests/runs/ the verbs read:
// the forge project around the working directory, else the git checkout
// around it (capture records there too when there is no forge.yaml).
func testRecordRoot(ctx context.Context, verb string) (string, error) {
	if root, err := cmdutil.FindProjectRoot(); err == nil && root != "" {
		return root, nil
	}
	dir, err := cmdutil.ResolutionRoot()
	if err != nil {
		return "", err
	}
	if top, err := testlocal.Toplevel(ctx, testGit, dir); err == nil && top != "" {
		return top, nil
	}
	return "", cliutil.UserErr(verb, "no forge.yaml and no git checkout here, so there is no test record to read", "",
		"cd into the project, or record a run first: `task test` (or `forge test capture --lane unit -- go test -json ./...`)")
}

// runSelection says which runs a verb reads.
type runSelection struct {
	refs   []string // explicit run ids or directories
	commit string   // --commit
	run    string   // --run (one run, and the runs on its tree)
}

// selectRuns resolves a selection. With nothing named it is the newest run
// and every run on its source: the clean runs of its commit, or the runs on
// its dirty tree.
func selectRuns(ctx context.Context, root string, sel runSelection, all []*testlocal.Run) ([]*testlocal.Run, string, error) {
	switch {
	case len(sel.refs) > 0:
		var out []*testlocal.Run
		for _, ref := range sel.refs {
			r, err := testlocal.Resolve(root, ref, all)
			if err != nil {
				return nil, "", err
			}
			out = append(out, r)
		}
		return out, fmt.Sprintf("%d named run(s)", len(out)), nil
	case sel.commit != "":
		sha := sel.commit
		if full, err := testlocal.ResolveRev(ctx, testGit, root, sel.commit); err == nil && full != "" {
			sha = full
		}
		out := testlocal.CommitRuns(all, sha)
		if len(out) == 0 {
			return nil, "", fmt.Errorf("no clean-tree run of commit %s under %s (a run on a dirty tree never counts toward a commit)", sel.commit, testreport.RunsRoot(root))
		}
		return out, "the clean runs of commit " + testShortID(sha), nil
	case sel.run != "":
		r, err := testlocal.Resolve(root, sel.run, all)
		if err != nil {
			return nil, "", err
		}
		return testlocal.TreeRuns(all, r), "run " + r.ID + " and the runs on its tree", nil
	}
	if len(all) == 0 {
		return nil, "", errNoRuns
	}
	newest := all[0]
	what := "the newest run's commit"
	if newest.Dirty {
		what = "the newest run's (dirty) tree"
	}
	return testlocal.TreeRuns(all, newest), what, nil
}

var errNoRuns = errors.New("no test runs recorded here yet")

func testShortID(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// testSkipPolicy is verify-test-run's skip policy for the record verbs: the
// shipped defaults, then forge.yaml's ci.test_skips. Unlike verify-test-run
// it does not require the ci feature: reading a record needs no CI.
func testSkipPolicy() (skipcheck.Policy, error) {
	policy := skipcheck.DefaultPolicy()
	store, err := loadProjectStore()
	switch {
	case errors.Is(err, ErrProjectConfigNotFound):
		return policy, nil
	case err != nil:
		return policy, err
	}
	cfg := store.CI().TestSkips
	if cfg.MaxSkipRatio > 0 {
		policy.MaxSkipRatio = cfg.MaxSkipRatio
	}
	if cfg.MinTests > 0 {
		policy.MinTests = cfg.MinTests
	}
	for _, a := range cfg.Allow {
		policy.Allow = append(policy.Allow, skipcheck.Exemption{Package: a.Package, Reason: a.Reason, MaxSkipRatio: a.MaxSkipRatio})
	}
	if errs := policy.Validate(); len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return policy, fmt.Errorf("invalid ci.test_skips in forge.yaml: %s", strings.Join(msgs, "; "))
	}
	return policy, nil
}

// verdictErr carries a record verb's exit code. Code 0 is no error.
func verdictErr(code int, msg string) error {
	if code == exitOK {
		return nil
	}
	return &exitCodeError{code: code, verdict: true, msg: msg}
}
