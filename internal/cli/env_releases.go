package cli

// `forge env releases list|show|at <env>`: what was released to an
// environment, when, by whom, and what changed. The third leg of the feedback
// loop beside `forge db query` and `forge obs`: an agent that sees an error
// asks which release was live then, and what that release carried.
//
// WHY `env releases` AND NOT `release …`. The `release` noun holds only verbs
// that act on a ledger independent of any environment (verify, where, bundle);
// everything env-scoped lives on the env noun (release_cmd.go). A timeline is
// per env.
//
// THE BACKEND IS THE ENV'S LEDGER. An env that declares forge.ControlPlane
// keeps its history there (DeployService + ActivityService); every other env
// keeps it in this machine's ledger. selectLedger already decides that for
// every other verb, and this one asks it rather than re-deriving the rule, so
// `env status`, `ledger show` and this verb can never name different ledgers.
//
// The assembly (directions, bracketing, PR numbers) is internal/releasetimeline;
// this file is flags, backend choice, and the agent output contract
// (internal/obsout): one document, a head that mirrors the exit code, bounded
// lists, at most three suggested next commands.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/internal/releasetimeline"
	"github.com/reliant-labs/forge/pkg/release"
)

// Schema names. Field names under a name are stable; a breaking change is a
// new version.
const (
	schemaReleasesList = "forge.env.releases.list/v1"
	schemaReleasesShow = "forge.env.releases.show/v1"
	schemaReleasesAt   = "forge.env.releases.at/v1"
)

const (
	backendLocal = "local"
	backendCloud = "cloud"

	defaultReleasesLimit = 20
	// maxReleasesLimit keeps one document readable and the cloud paging bounded.
	maxReleasesLimit = 200
	// defaultReleasesSince bounds `list` when --since is not given.
	defaultReleasesSince = 7 * 24 * time.Hour
	// maxShownCommits bounds the commit list `show` prints.
	maxShownCommits = 50
)

// releasesBackend is what a verb reads: a Source, plus the cloud-only calls
// when the env's ledger is a control plane.
type releasesBackend struct {
	Name   string
	Source releasetimeline.Source
	// Cloud is non-nil only for the cloud backend.
	Cloud *releasetimeline.Cloud
	// Ledger is where the history lives, for the document and for errors.
	Ledger string
	// Bundle reads one recorded bundle, for the object diff. Local only: the
	// control plane diffs bundles itself (CompareReleases).
	Bundle func(id string) (*release.BundleRecord, error)
}

// openReleasesBackend resolves env's ledger. A failure to reach or
// authenticate is "could not look" (exit 2): the timeline neither confirms
// nor denies anything.
var openReleasesBackend = func(ctx context.Context, env string) (releasesBackend, error) {
	projectDir := projectDirForKCL()
	l, err := selectLedger(ctx, projectDir, env)
	if err != nil {
		return releasesBackend{}, undeterminedf("choose the ledger of %s: %v", env, err)
	}
	if !l.Hosted {
		store, err := openMachineLedger(projectDir)
		if err != nil {
			return releasesBackend{}, undeterminedf("open this machine's ledger: %v", err)
		}
		return releasesBackend{
			Name: backendLocal, Source: releasetimeline.Local{Ledger: store}, Ledger: store.Dir(),
			Bundle: store.Bundle,
		}, nil
	}
	ep, cred, err := resolveCloudTarget(ctx, env, "")
	if err != nil {
		return releasesBackend{}, undeterminedf("reach the control plane of %s: %v", env, err)
	}
	c := releasetimeline.NewCloud(cloud.NewClient(ep, cred), hostedProjectName())
	return releasesBackend{Name: backendCloud, Source: c, Cloud: &c, Ledger: ep.URL}, nil
}

// releasesDeps are the seams a test replaces: the backend, the clock, and the
// git checkout that lists a recorded range.
type releasesDeps struct {
	open    func(ctx context.Context, env string) (releasesBackend, error)
	now     func() time.Time
	commits func() releasetimeline.CommitLister
}

func defaultReleasesDeps() releasesDeps {
	return releasesDeps{
		open: openReleasesBackend,
		now:  time.Now,
		commits: func() releasetimeline.CommitLister {
			return releasetimeline.NewGit(projectDirForKCL())
		},
	}
}

func newEnvReleasesCmd() *cobra.Command {
	return newEnvReleasesCmdWith(defaultReleasesDeps())
}

func newEnvReleasesCmdWith(deps releasesDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "releases",
		Short: "What was released to an environment, when, and what changed",
		Long: `The release timeline of an environment: promotions (a move to an OLDER release
is a rollback and is marked BEHIND), failed and timed-out deploys, gate results,
and who did it. Local environments read this machine's ledger; an environment that
declares forge.ControlPlane reads its control plane (credential scopes deploy:read
and obs:read).

  list <env>             the timeline, newest first
  show <env> <ref>       one release in full: commits and PRs, what changed, gates
  at   <env> <when>      which release was live at a timestamp or trace id

Every verb takes --json for one machine-readable document (--fields projects it).`,
	}
	cmd.AddCommand(newEnvReleasesListCmd(deps))
	cmd.AddCommand(newEnvReleasesShowCmd(deps))
	cmd.AddCommand(newEnvReleasesAtCmd(deps))
	return cmdutil.StrictGroup(cmd)
}

// failJSON writes the failure document for err when --json is set, and hands
// err back unchanged: the document and the process status come from the one
// error, so they cannot disagree.
func failJSON(cmd *cobra.Command, f obsout.Flags, schema obsout.Schema, err error, suggested ...string) error {
	if f.JSON {
		_ = schema.Write(cmd.OutOrStdout(), obsout.Failed(schema.Name, err, suggested...), nil)
	}
	return err
}

// ─── shared helpers ──────────────────────────────────────────────────────────

// parseSince reads --since: a duration ("7d", "36h", "90m") or an absolute
// time (RFC 3339, or a date). "" is no bound.
func parseSince(s string, now time.Time) (time.Time, error) {
	return parseWhen(strings.TrimSpace(s), now, true)
}

// parseWhen reads a point in time. relative is whether a bare duration means
// "that long ago" (--since) rather than being refused.
func parseWhen(s string, now time.Time, relative bool) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if strings.EqualFold(s, "now") {
		return now.UTC(), nil
	}
	if relative {
		if d, ok := parseAgo(s); ok {
			return now.Add(-d).UTC(), nil
		}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a time: use RFC 3339 (2026-10-21T14:30:00Z), a date (2026-10-21)%s", s,
		map[bool]string{true: ", or a duration ago (7d, 36h, 90m)", false: ""}[relative])
}

// parseAgo reads "7d", "36h", "90m" and any Go duration. Days are accepted
// because deploy history is counted in them and time.ParseDuration has no unit
// longer than an hour.
func parseAgo(s string) (time.Duration, bool) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.ParseFloat(n, 64)
		if err != nil || days < 0 {
			return 0, false
		}
		return time.Duration(days * float64(24*time.Hour)), true
	}
	d, err := time.ParseDuration(s)
	return d, err == nil && d >= 0
}

// releasesError classifies a failed read. Reaching nothing is exit 2 ("could
// not look"); a request the verb cannot honour is exit 1.
func releasesError(what string, err error) error {
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return err
	}
	return undeterminedf("%s: %v", what, err)
}

// releasesCmd builds a complete, runnable command line for a suggestion. Name()
// is "forge" or, embedded in another binary, "reliant forge": two words that
// must not be quoted into one.
func releasesCmd(args ...string) string {
	return obsout.Command(append(strings.Fields(Name()), args...)...)
}

// usagef is a request the verb cannot honour: exit 1, "we looked, and it is wrong".
func usagef(format string, args ...any) error {
	return exitCodeError{code: exitWrong, msg: fmt.Sprintf(format, args...)}
}

// writeWarnings prints a source's soft failures to stderr, never into a
// document that consumers parse.
func writeWarnings(w io.Writer, warnings []string) {
	for _, m := range warnings {
		fmt.Fprintf(w, "warning: %s\n", m)
	}
}
