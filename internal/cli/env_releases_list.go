package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/internal/releasetimeline"
)

// releasesListDoc is the `list --json` document.
type releasesListDoc struct {
	obsout.Head
	Env     string `json:"env"`
	Backend string `json:"backend"`
	// Ledger says WHICH ledger answered: a directory, or a control plane URL.
	Ledger string    `json:"ledger"`
	Since  time.Time `json:"since,omitempty"`
	// Entries are newest first.
	Entries []releasetimeline.Entry `json:"entries"`
	obsout.Page
	// Warnings are reads that failed softly (the activity stream, say): the
	// timeline is still true, but incomplete in the way they name.
	Warnings  []string `json:"warnings,omitempty"`
	Suggested []string `json:"suggested,omitempty"`
}

// entryFields are the selectable paths of one timeline row. Declared once so
// list and at agree on them.
var entryFields = []string{
	"kind", "env", "version", "at", "promotion_id", "bundle_id", "live", "direction",
	"previous_version", "previous_bundle_id", "from_env", "actor", "via", "run_url",
	"note", "targets", "superseded_in_flight",
	"commit_range", "commit_range.from_release", "commit_range.to_release",
	"commit_range.from_commit", "commit_range.to_commit", "commit_range.reverts",
	"commit_range.total", "commit_range.truncated",
	"rollout", "rollout.status", "rollout.duration_s", "rollout.finished_at", "rollout.reason", "rollout.cluster",
	"gates", "gates.name", "gates.status", "gates.phase", "gates.summary", "gates.url",
	"gate_verdict", "health", "health.verdict", "health.detail",
}

func prefixed(prefix string, fields []string) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = prefix + f
	}
	return out
}

var releasesListSchema = obsout.Schema{
	Name: schemaReleasesList,
	Fields: append([]string{
		"env", "backend", "ledger", "since", "entries", "warnings", "suggested",
	}, prefixed("entries.", entryFields)...),
}

func newEnvReleasesListCmd(deps releasesDeps) *cobra.Command {
	var (
		flags obsout.Flags
		since string
	)
	cmd := &cobra.Command{
		Use:   "list <env>",
		Short: "The release timeline of an environment, newest first",
		Args:  cobra.ExactArgs(1),
		Long: `List an environment's timeline: promotions (BEHIND marks a rollback), failed and
timed-out deploys, with version, time, who/via, commit range, rollout duration,
gate verdicts and a health verdict when one exists.

--since bounds the window (default 7d): a duration (7d, 36h) or a time
(RFC 3339 or a date). --limit caps the rows; a truncated list says so.

Exit 0 the timeline was read (an empty one is a read), 2 it could not be.

` + releasesListSchema.Help(),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			fields, err := flags.Parsed(releasesListSchema)
			if err != nil {
				return usagef("%v", err)
			}
			doc, err := runReleasesList(cmd.Context(), deps, args[0], since, flags.Limit)
			if err != nil {
				return failJSON(cmd, flags, releasesListSchema, err,
					releasesCmd("env", "status", args[0]))
			}
			writeWarnings(cmd.ErrOrStderr(), doc.Warnings)
			if ok, err := flags.Emit(cmd.OutOrStdout(), releasesListSchema, doc, fields); ok || err != nil {
				return err
			}
			renderReleasesList(cmd.OutOrStdout(), doc)
			return nil
		},
	}
	flags.Register(cmd.Flags(), defaultReleasesLimit)
	cmd.Flags().StringVar(&since, "since", "", "window start: a duration ago (7d, 36h) or a time (default 7d)")
	return cmd
}

func runReleasesList(ctx context.Context, deps releasesDeps, env, sinceFlag string, limit int) (releasesListDoc, error) {
	if limit < 0 || limit > maxReleasesLimit {
		return releasesListDoc{}, usagef("--limit must be between 1 and %d, got %d", maxReleasesLimit, limit)
	}
	now := deps.now()
	since, err := parseSince(sinceFlag, now)
	if err != nil {
		return releasesListDoc{}, usagef("--since: %v", err)
	}
	if since.IsZero() {
		since = now.Add(-defaultReleasesSince).UTC()
	}
	b, err := deps.open(ctx, env)
	if err != nil {
		return releasesListDoc{}, err
	}
	data, err := b.Source.Load(ctx, env, since)
	if err != nil {
		return releasesListDoc{}, releasesError("read the release timeline of "+env, err)
	}
	tl := releasetimeline.Assemble(data, since, limit)
	attachHealth(ctx, b, env, &tl, &data)

	doc := releasesListDoc{
		Env: env, Backend: b.Name, Ledger: b.Ledger, Since: since,
		Entries: tl.Entries, Warnings: data.Warnings,
	}
	if doc.Entries == nil {
		doc.Entries = []releasetimeline.Entry{}
	}
	doc.Page = obsout.Page{Truncated: tl.Truncated}
	doc.Suggested = suggestFromList(env, doc, since)
	doc.Stamp(schemaReleasesList, nil)
	return doc, nil
}

// attachHealth asks the control plane for a verdict on the newest promotion
// rows. Best effort and bounded: a control plane that predates the RPC, or a
// credential without obs:read, leaves rows without a verdict, which reads as
// "unknown" and never as healthy. The reason is a warning, said once.
func attachHealth(ctx context.Context, b releasesBackend, env string, tl *releasetimeline.Timeline, data *releasetimeline.Data) {
	if b.Cloud == nil {
		return
	}
	const maxHealthLookups = 5
	verdicts := map[string]releasetimeline.Health{}
	var firstErr error
	n := 0
	for _, e := range tl.Entries {
		if e.Kind != releasetimeline.KindPromotion || e.PromotionID == "" {
			continue
		}
		if n++; n > maxHealthLookups {
			break
		}
		h, err := releasetimeline.ReleaseHealth(ctx, *b.Cloud, env, e.PromotionID)
		if err != nil {
			firstErr = err
			break
		}
		verdicts[e.PromotionID] = h
	}
	if firstErr != nil {
		data.Warnings = append(data.Warnings, fmt.Sprintf("no health verdicts: %v", firstErr))
	}
	tl.AttachHealth(verdicts)
}

// suggestFromList offers up to three next commands: look at the newest
// failure, else the newest rollback, else the live release.
func suggestFromList(env string, doc releasesListDoc, since time.Time) []string {
	var failed, behind, live string
	for _, e := range doc.Entries {
		ref := e.PromotionID
		if ref == "" {
			ref = e.Version
		}
		switch {
		case e.Kind == releasetimeline.KindFailedDeploy && failed == "" && e.Version != "":
			failed = releasesCmd("env", "releases", "show", env, ref)
		case e.Direction == releasetimeline.DirBehind && behind == "":
			behind = releasesCmd("env", "releases", "show", env, ref)
		case e.Live && live == "":
			live = releasesCmd("env", "releases", "show", env, ref)
		}
	}
	var more string
	if doc.Truncated {
		more = releasesCmd("env", "releases", "list", env, "--since", since.UTC().Format(time.RFC3339), "--limit", fmt.Sprint(maxReleasesLimit))
	}
	return obsout.Suggest(failed, behind, live, more)
}

func renderReleasesList(w io.Writer, doc releasesListDoc) {
	fmt.Fprintf(w, "Release timeline of %s  (%s: %s)\n\n", doc.Env, doc.Backend, doc.Ledger)
	if len(doc.Entries) == 0 {
		fmt.Fprintf(w, "no releases since %s — this environment has not been promoted in the window (try --since 90d)\n", doc.Since.Format(time.RFC3339))
		return
	}
	rows := make([][]string, 0, len(doc.Entries))
	for _, e := range doc.Entries {
		rows = append(rows, []string{
			e.At.UTC().Format("2006-01-02 15:04:05Z"), releaseLabel(e), entryWhat(e),
			whoVia(e), rangeLabel(e), rolloutLabel(e), gateLabel(e), healthLabel(e),
		})
	}
	obsout.Table(w, []string{"AT", "RELEASE", "WHAT", "WHO", "COMMITS", "ROLLOUT", "GATES", "HEALTH"}, rows, 40)
	if doc.Truncated {
		fmt.Fprintf(w, "\nmore rows exist than shown: raise --limit or widen --since\n")
	}
	if len(doc.Suggested) > 0 {
		fmt.Fprintf(w, "\nnext:\n")
		for _, s := range doc.Suggested {
			fmt.Fprintf(w, "  %s\n", s)
		}
	}
}

func releaseLabel(e releasetimeline.Entry) string {
	if e.Live {
		return e.Version + " (live)"
	}
	return emptyDash(e.Version)
}

// entryWhat names the row: a rollback is said in capitals, because it is the
// row somebody is looking for.
func entryWhat(e releasetimeline.Entry) string {
	if e.Kind == releasetimeline.KindFailedDeploy {
		if e.Rollout != nil && e.Rollout.Status == releasetimeline.StatusTimedOut {
			return "DEPLOY TIMED OUT"
		}
		return "DEPLOY FAILED"
	}
	switch e.Direction {
	case releasetimeline.DirBehind:
		return "ROLLBACK (behind " + e.PreviousVersion + ")"
	case releasetimeline.DirAhead:
		return "promote (from " + e.PreviousVersion + ")"
	case releasetimeline.DirSame:
		if len(e.Targets) > 0 {
			return "redeploy " + strings.Join(e.Targets, ",")
		}
		return "re-promote"
	case releasetimeline.DirInitial:
		return "first promote"
	}
	return "promote"
}

func whoVia(e releasetimeline.Entry) string {
	switch {
	case e.Actor != "" && e.Via != "":
		return e.Actor + " via " + e.Via
	case e.Actor != "":
		return e.Actor
	case e.Via != "":
		return "via " + e.Via
	}
	return "-"
}

func rangeLabel(e releasetimeline.Entry) string {
	r := e.Range
	switch {
	case r == nil:
		return "-"
	case r.Total > 0 && r.Truncated:
		return fmt.Sprintf("%d+", r.Total)
	case r.Total > 0:
		return fmt.Sprint(r.Total)
	case r.Truncated:
		return "?"
	}
	return "0"
}

func rolloutLabel(e releasetimeline.Entry) string {
	r := e.Rollout
	if r == nil {
		return "-"
	}
	if r.DurationSeconds > 0 {
		return r.Status + " " + (time.Duration(r.DurationSeconds) * time.Second).String()
	}
	return r.Status
}

func gateLabel(e releasetimeline.Entry) string {
	if e.GateVerdict == "" {
		return "-"
	}
	return fmt.Sprintf("%s (%d)", e.GateVerdict, len(e.Gates))
}

func healthLabel(e releasetimeline.Entry) string {
	if e.Health == nil {
		return "-"
	}
	return e.Health.Verdict
}
