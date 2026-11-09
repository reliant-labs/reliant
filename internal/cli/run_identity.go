package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"

	"github.com/reliant-labs/forge/pkg/release"
)

// Run identity: the `--run-id` / `--run-url` every WRITING verb (cut,
// promote, record) accepts, and what they default to when CI does not set
// them.
//
// WHY A DEFAULT AT ALL. A run id is the join key that turns a scatter of
// ledger rows — a release, its promotions, its gates — into one readable
// story, and it only works if every step of one pipeline sends the SAME
// value. Requiring each workflow to thread `--run-id` through every step is
// a requirement most will satisfy incompletely, and a half-populated join
// key is worse than none: the timeline looks complete and is missing rows.
// So the default comes from the environment the steps already share.
//
// THERE ARE NO GITHUB-SPECIFIC CODE PATHS beyond this default. Nothing
// downstream branches on the provider — release.Run documents Provider as
// display-only — so adding GitLab is the few lines below and not a second
// implementation. That is the whole point of defaulting rather than
// detecting: the id is opaque to everything that stores it.

// The CI environment variables these defaults read. Named so the help text,
// the implementation and the tests cannot drift apart.
const (
	envGitHubRepository = "GITHUB_REPOSITORY"
	envGitHubRunID      = "GITHUB_RUN_ID"
	envGitHubRunAttempt = "GITHUB_RUN_ATTEMPT"
	envGitHubServerURL  = "GITHUB_SERVER_URL"

	envGitLabPipelineID  = "CI_PIPELINE_ID"
	envGitLabProjectPath = "CI_PROJECT_PATH"
	envGitLabPipelineURL = "CI_PIPELINE_URL"
)

// The provider labels a defaulted run carries. DISPLAY ONLY (release.Run).
const (
	runProviderGitHub = "github"
	runProviderGitLab = "gitlab"
	// runProviderManual is what an explicitly-supplied id with no
	// recognised CI environment gets: a human at a terminal, or an
	// unrecognised runner.
	runProviderManual = "manual"
)

// runOptions are the run flags a writing verb declares. Zero means "default
// from the environment"; --no-run means "deliberately no run".
type runOptions struct {
	// ID and URL override the environment defaults.
	ID  string
	URL string
	// None suppresses the default entirely (`--no-run`). A promotion that
	// belongs to no run is still a promotion, and a human who does not
	// want their shell's stray CI variables attributed to them needs a
	// way to say so.
	None bool
}

// resolveRun turns the flags plus the process environment into the run to
// send. It reads os.Getenv, so it is the one function a verb calls; the
// logic is in runFromEnvironment, which is pure.
func (o runOptions) resolveRun() (release.Run, error) {
	return o.resolve(os.Getenv)
}

// resolve is resolveRun with the environment injected, so the rules are
// testable without t.Setenv (which cannot be used in a parallel test).
func (o runOptions) resolve(getenv func(string) string) (release.Run, error) {
	if o.None {
		if o.ID != "" || o.URL != "" {
			// Both were stated, and they contradict. Refused rather
			// than silently preferring one: a pipeline that passes
			// both has a bug, and guessing would hide it.
			return release.Run{}, fmt.Errorf(
				"--no-run cannot be combined with --run-id/--run-url: pick whether this write belongs to a run")
		}
		return release.Run{}, nil
	}

	run := runFromEnvironment(getenv)
	// An explicit flag overrides the detected value. The URL is kept
	// when only the id was overridden, because a pipeline that renames
	// its run still links to the same page.
	if o.ID != "" {
		if o.ID != run.ID {
			run = release.Run{ID: o.ID, URL: run.URL, Provider: runProviderManual}
		}
	}
	if o.URL != "" {
		run.URL = o.URL
	}
	if run.ID == "" && run.URL != "" {
		// release.Run.Validate refuses this, and the message there is
		// about the model rather than about the flags. Say which flag.
		return release.Run{}, fmt.Errorf(
			"--run-url was given without a run id: a url with no id is detail about a run nobody can find; add --run-id")
	}
	if err := run.Validate(); err != nil {
		return release.Run{}, err
	}
	return run, nil
}

// runFromEnvironment is the default: the run identity the surrounding CI
// system already defines, or the zero Run when there is none.
//
// A ZERO RUN IS NOT AN ERROR. A human at a terminal writes one, and
// release.Run.Zero() exists precisely to say so.
func runFromEnvironment(getenv func(string) string) release.Run {
	if run, ok := gitHubRun(getenv); ok {
		return run
	}
	if run, ok := gitLabRun(getenv); ok {
		return run
	}
	return release.Run{}
}

// gitHubRun builds "github:<owner/repo>/<run id>/<attempt>".
//
// THE ATTEMPT IS PART OF THE ID. GitHub keeps GITHUB_RUN_ID stable across
// re-runs and increments GITHUB_RUN_ATTEMPT, so an id without the attempt
// would make a re-run indistinguishable from its original — and the gate
// idempotency key is (promotion, name, run id), so the re-run's gates would
// collide with the first attempt's and return the OLD rows. Including the
// attempt is what keeps "stable across retries of ONE attempt" true while
// keeping two attempts distinct.
func gitHubRun(getenv func(string) string) (release.Run, bool) {
	repo := strings.TrimSpace(getenv(envGitHubRepository))
	id := strings.TrimSpace(getenv(envGitHubRunID))
	if repo == "" || id == "" {
		return release.Run{}, false
	}
	attempt := strings.TrimSpace(getenv(envGitHubRunAttempt))
	if attempt == "" {
		// A first run sometimes leaves it unset. Defaulting to 1 is
		// what GitHub itself means by an absent attempt, and it keeps
		// the id shape fixed so a parser never meets two forms.
		attempt = "1"
	}
	server := strings.TrimSpace(getenv(envGitHubServerURL))
	if server == "" {
		server = "https://github.com"
	}
	return release.Run{
		ID:       fmt.Sprintf("%s:%s/%s/%s", runProviderGitHub, repo, id, attempt),
		URL:      fmt.Sprintf("%s/%s/actions/runs/%s", strings.TrimRight(server, "/"), repo, id),
		Provider: runProviderGitHub,
	}, true
}

// gitLabRun is the same few lines for GitLab, which is the point: a second
// provider costs a function, not a code path.
func gitLabRun(getenv func(string) string) (release.Run, bool) {
	id := strings.TrimSpace(getenv(envGitLabPipelineID))
	if id == "" {
		return release.Run{}, false
	}
	project := strings.TrimSpace(getenv(envGitLabProjectPath))
	runID := runProviderGitLab + ":" + id
	if project != "" {
		runID = fmt.Sprintf("%s:%s/%s", runProviderGitLab, project, id)
	}
	return release.Run{
		ID:       runID,
		URL:      strings.TrimSpace(getenv(envGitLabPipelineURL)),
		Provider: runProviderGitLab,
	}, true
}

// registerRunFlags declares --run-id / --run-url / --no-run on a writing
// verb. One registrar, so every verb spells the flags and their help the
// same way — §3.A says "every verb that writes accepts --run-id and
// --run-url", and three hand-written copies would drift.
func registerRunFlags(flags *pflag.FlagSet, opts *runOptions) {
	flags.StringVar(&opts.ID, "run-id", "",
		"CI run this write belongs to (default: detected from the CI environment, e.g. github:<repo>/<run>/<attempt>)")
	flags.StringVar(&opts.URL, "run-url", "",
		"Link to the CI run, for a human reading the ledger (default: detected from the CI environment)")
	flags.BoolVar(&opts.None, "no-run", false,
		"Record no run, even inside CI")
}

// runWireFields renders a run as the proto3 JSON `run` member every writing
// RPC accepts (controlplane.v1.DeployRun), or nil for a zero run so the
// field is omitted rather than sent empty.
func runWireFields(run release.Run) map[string]any {
	if run.Zero() {
		return nil
	}
	out := map[string]any{"id": run.ID}
	if run.URL != "" {
		out["url"] = run.URL
	}
	if run.Provider != "" {
		out["provider"] = run.Provider
	}
	return out
}
