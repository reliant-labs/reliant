package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/internal/releasetimeline"
	"github.com/reliant-labs/forge/pkg/release"
)

// releasesShowDoc is the `show --json` document: one promotion in full.
type releasesShowDoc struct {
	obsout.Head
	Env     string `json:"env"`
	Backend string `json:"backend"`
	Ledger  string `json:"ledger"`
	// Entry is the promotion row, as `list` renders it.
	Entry releasetimeline.Entry `json:"entry"`
	// FailedDeploys are the deploys of THIS promotion that failed or timed out,
	// newest first. They are rows of their own in `list`; here they belong to
	// the promotion being shown, because "why did v2 not land" is asked of v2.
	FailedDeploys []releasetimeline.Entry `json:"failed_deploys,omitempty"`
	// Commits are the source history the move carried, newest first. For a
	// BEHIND move they are the commits the move takes AWAY (the range runs the
	// other way). Absent when git could not list them; CommitsNote says why.
	Commits     []showCommit `json:"commits,omitempty"`
	CommitsNote string       `json:"commits_note,omitempty"`
	// PullRequests are the PR numbers merged in Commits (squash subjects).
	PullRequests []int `json:"pull_requests,omitempty"`
	obsout.Page
	// Changes is what differs between the previous release and this one in
	// this environment: artifacts and objects. Absent when it cannot be said.
	Changes     *showChanges `json:"changes,omitempty"`
	ChangesNote string       `json:"changes_note,omitempty"`
	// Migrations are the migration files the commit range added, changed or
	// removed (db/migrations). Present only when git could read the range.
	Migrations []releasetimeline.PathChange `json:"migrations,omitempty"`
	Warnings   []string                     `json:"warnings,omitempty"`
	Suggested  []string                     `json:"suggested,omitempty"`
}

type showCommit struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	Author  string    `json:"author,omitempty"`
	Time    time.Time `json:"time"`
	PR      int       `json:"pr,omitempty"`
}

type showChanges struct {
	// Artifacts are the artifacts whose identity moved.
	Artifacts []showArtifactChange `json:"artifacts,omitempty"`
	// UnchangedArtifacts counts the ones that did not.
	UnchangedArtifacts int `json:"unchanged_artifacts"`
	// Objects are the rendered Kubernetes objects the bundles add, modify and remove.
	Objects          []showObjectChange `json:"objects,omitempty"`
	UnchangedObjects int                `json:"unchanged_objects,omitempty"`
	// SummariesMissing: a bundle predates object summaries, so objects are
	// named without saying what in them changed.
	SummariesMissing bool `json:"summaries_missing,omitempty"`
}

type showArtifactChange struct {
	Name   string `json:"name"`
	Change string `json:"change"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

type showObjectChange struct {
	Change   string `json:"change"`
	Object   string `json:"object"`
	Workload string `json:"workload,omitempty"`
	Summary  string `json:"summary,omitempty"`
}

var releasesShowSchema = obsout.Schema{
	Name: schemaReleasesShow,
	Fields: append([]string{
		"env", "backend", "ledger", "entry",
		"failed_deploys", "failed_deploys.at", "failed_deploys.rollout", "failed_deploys.rollout.status",
		"failed_deploys.rollout.reason", "failed_deploys.rollout.duration_s", "failed_deploys.rollout.cluster",
		"commits", "commits.sha", "commits.subject", "commits.author", "commits.time", "commits.pr",
		"commits_note", "pull_requests",
		"changes", "changes.artifacts", "changes.artifacts.name", "changes.artifacts.change",
		"changes.artifacts.from", "changes.artifacts.to", "changes.unchanged_artifacts",
		"changes.objects", "changes.objects.change", "changes.objects.object",
		"changes.objects.workload", "changes.objects.summary", "changes.unchanged_objects",
		"changes.summaries_missing", "changes_note",
		"migrations", "migrations.status", "migrations.path",
		"warnings", "suggested",
	}, prefixed("entry.", entryFields)...),
}

func newEnvReleasesShowCmd(deps releasesDeps) *cobra.Command {
	var flags obsout.Flags
	cmd := &cobra.Command{
		Use:   "show <env> <version|promotion-id>",
		Short: "One release in full: commits and PRs, what changed, migrations, gates, health",
		Args:  cobra.ExactArgs(2),
		Long: `Show one promotion of an environment. The reference is a promotion id, or a
release version (the newest promotion of it).

  commits      git log of the recorded commit range (PR numbers from squash
               subjects "(#123)"); a BEHIND move lists what it takes away.
               Needs the commits in this checkout: otherwise commits_note says so.
  changes      artifacts whose digest moved, and rendered objects added, modified
               or removed in THIS environment, against the release it replaced
               (control plane: DeployService.CompareReleases; local: the two
               recorded bundles).
  migrations   migration files the range touched (db/migrations).
  gates        the checks recorded before and after the environment moved.

Exit 0 shown, 1 no such release in this environment, 2 the ledger could not be read.

` + releasesShowSchema.Help(),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			fields, err := flags.Parsed(releasesShowSchema)
			if err != nil {
				return usagef("%v", err)
			}
			doc, err := runReleasesShow(cmd.Context(), deps, args[0], args[1], flags.Limit)
			if err != nil {
				return failJSON(cmd, flags, releasesShowSchema, err,
					releasesCmd("env", "releases", "list", args[0], "--since", "90d"))
			}
			writeWarnings(cmd.ErrOrStderr(), doc.Warnings)
			if ok, err := flags.Emit(cmd.OutOrStdout(), releasesShowSchema, doc, fields); ok || err != nil {
				return err
			}
			renderReleasesShow(cmd.OutOrStdout(), doc)
			return nil
		},
	}
	flags.Register(cmd.Flags(), maxShownCommits)
	return cmd
}

func runReleasesShow(ctx context.Context, deps releasesDeps, env, ref string, limit int) (releasesShowDoc, error) {
	if limit < 0 || limit > release.MaxRangeCommits {
		return releasesShowDoc{}, usagef("--limit must be between 1 and %d, got %d", release.MaxRangeCommits, limit)
	}
	b, err := deps.open(ctx, env)
	if err != nil {
		return releasesShowDoc{}, err
	}
	// A reference can name any promotion, however old: read the whole ledger
	// this source will give, not a recent window.
	data, err := b.Source.Load(ctx, env, time.Time{})
	if err != nil {
		return releasesShowDoc{}, releasesError("read the release timeline of "+env, err)
	}
	tl := releasetimeline.Assemble(data, time.Time{}, 0)
	entry, err := tl.Find(ref)
	if err != nil {
		if errors.Is(err, releasetimeline.ErrNotFound) {
			return releasesShowDoc{}, usagef("%s has no promotion or release %q on record (%s)", env, ref, b.Ledger)
		}
		return releasesShowDoc{}, err
	}

	doc := releasesShowDoc{Env: env, Backend: b.Name, Ledger: b.Ledger, Entry: entry, Warnings: data.Warnings}
	if b.Cloud != nil {
		h, herr := releasetimeline.ReleaseHealth(ctx, *b.Cloud, env, entry.PromotionID)
		if herr != nil {
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("no health verdict: %v", herr))
		} else {
			doc.Entry.Health = &h
		}
	}

	doc.FailedDeploys = tl.FailuresOf(entry.PromotionID)
	showCommits(ctx, deps, &doc, entry, limit)
	showChangesOf(ctx, b, &doc, entry, data.Releases)
	doc.Suggested = suggestFromShow(env, doc)
	doc.Stamp(schemaReleasesShow, nil)
	return doc, nil
}

// showCommits fills the commit list, PR numbers and migrations from git. When
// git cannot answer it falls back to the sample the ledger recorded at cut, and
// says which it is.
func showCommits(ctx context.Context, deps releasesDeps, doc *releasesShowDoc, e releasetimeline.Entry, limit int) {
	r := e.Range
	if r == nil {
		doc.CommitsNote = "no commit range is recorded for this promotion (the release recorded none, or the earlier release is not in the ledger)"
		return
	}
	lister := deps.commits()
	commits, prs, err := releasetimeline.CommitsOf(ctx, lister, *r, limit+1)
	switch {
	case err == nil:
		commits, doc.Page = obsout.Bound(commits, limit, "")
		doc.PullRequests = prs
	default:
		doc.CommitsNote = fmt.Sprintf("git could not list the range in this checkout: %v", err)
		return
	}
	for _, c := range commits {
		pr, _ := releasetimeline.PRNumber(c.Subject)
		doc.Commits = append(doc.Commits, showCommit{SHA: c.SHA, Subject: c.Subject, Author: c.Author, Time: c.Time, PR: pr})
	}
	if g, ok := lister.(releasetimeline.Git); ok {
		from, to := r.FromCommit, r.ToCommit
		if r.Reverts {
			from, to = to, from
		}
		if paths, perr := g.ChangedPaths(ctx, from, to, "db/migrations"); perr == nil {
			doc.Migrations = paths
		}
	}
}

// showChangesOf says what the move changed in this environment.
func showChangesOf(ctx context.Context, b releasesBackend, doc *releasesShowDoc, e releasetimeline.Entry, releases []release.Release) {
	if e.PreviousVersion == "" {
		doc.ChangesNote = "this is the environment's first promotion in the history read: there is nothing to compare against"
		return
	}
	if e.PreviousVersion == e.Version && e.PreviousBundleID == e.BundleID {
		doc.ChangesNote = "the environment was already on this release and bundle: nothing changed"
		return
	}
	if b.Cloud != nil {
		changes, err := cloudChanges(ctx, *b.Cloud, doc.Env, e.PreviousVersion, e.Version)
		if err != nil {
			doc.ChangesNote = fmt.Sprintf("the control plane could not compare %s and %s: %v", e.PreviousVersion, e.Version, err)
			return
		}
		doc.Changes = changes
		return
	}
	doc.Changes, doc.ChangesNote = localChanges(b, e, releases)
}

// cloudChanges asks DeployService.CompareReleases. The wire shape is declared
// here and narrowly: a field forge does not read is a field it cannot break on.
func cloudChanges(ctx context.Context, c releasetimeline.Cloud, env, from, to string) (*showChanges, error) {
	var resp struct {
		Artifacts []struct {
			Name   string `json:"name"`
			Change string `json:"change"`
			From   string `json:"from"`
			To     string `json:"to"`
		} `json:"artifacts"`
		Unchanged int `json:"unchangedArtifacts"`
		Objects   struct {
			Deltas []struct {
				Change    string `json:"change"`
				Cluster   string `json:"cluster"`
				Kind      string `json:"kind"`
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
				Workload  string `json:"workload"`
				Summary   string `json:"summary"`
			} `json:"deltas"`
			Unchanged        int  `json:"unchanged"`
			SummariesMissing bool `json:"summariesMissing"`
		} `json:"objects"`
	}
	if err := releasetimeline.CompareReleases(ctx, c, from, to, env, &resp); err != nil {
		return nil, err
	}
	out := &showChanges{UnchangedArtifacts: resp.Unchanged, UnchangedObjects: resp.Objects.Unchanged, SummariesMissing: resp.Objects.SummariesMissing}
	for _, a := range resp.Artifacts {
		out.Artifacts = append(out.Artifacts, showArtifactChange{Name: a.Name, Change: wireChange(a.Change), From: a.From, To: a.To})
	}
	for _, d := range resp.Objects.Deltas {
		key := release.ObjectKey{Cluster: d.Cluster, Kind: d.Kind, Namespace: d.Namespace, Name: d.Name}
		out.Objects = append(out.Objects, showObjectChange{Change: wireChange(d.Change), Object: key.String(), Workload: d.Workload, Summary: d.Summary})
	}
	return out, nil
}

// wireChange maps DEPLOY_CHANGE_KIND_MODIFIED to "modified".
func wireChange(w string) string {
	return strings.ToLower(strings.TrimPrefix(w, "DEPLOY_CHANGE_KIND_"))
}

// localChanges compares the two recorded releases (artifacts) and their two
// recorded bundles (objects), with the code `env diff` and the control plane
// share (pkg/release).
func localChanges(b releasesBackend, e releasetimeline.Entry, releases []release.Release) (*showChanges, string) {
	byVersion := map[string]release.Release{}
	for _, r := range releases {
		byVersion[r.Version] = r
	}
	from, okF := byVersion[e.PreviousVersion]
	to, okT := byVersion[e.Version]
	if !okF || !okT {
		return nil, fmt.Sprintf("release %s or %s is not in this machine's release ledger, so their artifacts cannot be compared",
			e.PreviousVersion, e.Version)
	}
	changed, unchanged := release.CompareArtifacts(from, to)
	out := &showChanges{UnchangedArtifacts: unchanged}
	for _, c := range changed {
		out.Artifacts = append(out.Artifacts, showArtifactChange{Name: c.Name, Change: string(c.Change), From: c.From, To: c.To})
	}
	if e.PreviousBundleID == "" || e.BundleID == "" || b.Bundle == nil {
		return out, "objects not compared: one of the two promotions recorded no bundle"
	}
	fb, ferr := b.Bundle(e.PreviousBundleID)
	tb, terr := b.Bundle(e.BundleID)
	if ferr != nil || terr != nil || fb == nil || tb == nil {
		return out, "objects not compared: a recorded bundle could not be read"
	}
	objs := release.CompareObjects(*fb, *tb)
	out.UnchangedObjects, out.SummariesMissing = objs.Unchanged, objs.SummariesMissing
	for _, d := range objs.Deltas {
		out.Objects = append(out.Objects, showObjectChange{Change: string(d.Change), Object: d.Key.String(), Workload: d.Workload, Summary: d.Summary})
	}
	sort.SliceStable(out.Objects, func(i, j int) bool { return out.Objects[i].Object < out.Objects[j].Object })
	return out, ""
}

func suggestFromShow(env string, doc releasesShowDoc) []string {
	e := doc.Entry
	var prev, at string
	if e.PreviousVersion != "" && e.PreviousVersion != e.Version {
		prev = releasesCmd("env", "releases", "show", env, e.PreviousVersion)
	}
	at = releasesCmd("env", "releases", "at", env, e.At.UTC().Format(time.RFC3339))
	return obsout.Suggest(prev, releasesCmd("env", "releases", "list", env), at)
}

func renderReleasesShow(w io.Writer, doc releasesShowDoc) {
	e := doc.Entry
	fmt.Fprintf(w, "%s  %s in %s\n", e.Version, entryWhat(e), doc.Env)
	kv := func(k, v string) {
		if v != "" {
			fmt.Fprintf(w, "  %-10s %s\n", k, v)
		}
	}
	kv("at", e.At.UTC().Format(time.RFC3339))
	kv("promotion", e.PromotionID)
	kv("by", strings.TrimPrefix(whoVia(e), "-"))
	kv("from env", e.FromEnv)
	kv("note", e.Note)
	kv("run", e.RunURL)
	if e.Live {
		kv("state", "live now")
	}
	if e.Superseded {
		kv("warning", "superseded a rollout that had not finished")
	}
	if r := e.Rollout; r != nil {
		line := rolloutLabel(e)
		if r.Reason != "" {
			line += ": " + r.Reason
		}
		kv("rollout", line)
	}
	if e.Health != nil {
		kv("health", strings.TrimSpace(e.Health.Verdict+" "+e.Health.Detail))
	}

	for _, f := range doc.FailedDeploys {
		fmt.Fprintf(w, "\n  DEPLOY %s at %s", strings.ToUpper(strings.ReplaceAll(f.Rollout.Status, "_", " ")), f.At.UTC().Format(time.RFC3339))
		if f.Rollout.DurationSeconds > 0 {
			fmt.Fprintf(w, " after %s", (time.Duration(f.Rollout.DurationSeconds) * time.Second).String())
		}
		if f.Rollout.Reason != "" {
			fmt.Fprintf(w, ": %s", f.Rollout.Reason)
		}
		fmt.Fprintln(w)
	}

	if len(e.Gates) > 0 {
		fmt.Fprintf(w, "\ngates (%s)\n", e.GateVerdict)
		rows := make([][]string, 0, len(e.Gates))
		for _, g := range e.Gates {
			rows = append(rows, []string{g.Phase, g.Name, g.Status, g.Summary})
		}
		obsout.Table(w, []string{"WHEN", "GATE", "STATUS", "SUMMARY"}, rows, 60)
	}

	fmt.Fprintf(w, "\ncommits")
	if e.Range != nil && e.Range.Reverts {
		fmt.Fprintf(w, " (taken away by this move)")
	}
	fmt.Fprintln(w)
	if len(doc.Commits) == 0 {
		fmt.Fprintf(w, "  %s\n", emptyAs(doc.CommitsNote, "none in the range"))
	}
	for _, c := range doc.Commits {
		fmt.Fprintf(w, "  %s  %s\n", short(c.SHA), c.Subject)
	}
	if doc.Truncated {
		fmt.Fprintf(w, "  … more than %d: raise --limit\n", len(doc.Commits))
	}
	if len(doc.PullRequests) > 0 {
		nums := make([]string, len(doc.PullRequests))
		for i, n := range doc.PullRequests {
			nums[i] = fmt.Sprintf("#%d", n)
		}
		fmt.Fprintf(w, "  PRs: %s\n", strings.Join(nums, " "))
	}
	if len(doc.Migrations) > 0 {
		fmt.Fprintf(w, "\nmigrations\n")
		for _, m := range doc.Migrations {
			fmt.Fprintf(w, "  %s  %s\n", m.Status, m.Path)
		}
	}

	if doc.Changes != nil {
		fmt.Fprintf(w, "\nchanged vs %s\n", e.PreviousVersion)
		for _, a := range doc.Changes.Artifacts {
			fmt.Fprintf(w, "  artifact  %-9s %s  %s -> %s\n", a.Change, a.Name, emptyDash(shortDigest(a.From)), emptyDash(shortDigest(a.To)))
		}
		for _, o := range doc.Changes.Objects {
			fmt.Fprintf(w, "  object    %-9s %s  %s\n", o.Change, o.Object, o.Summary)
		}
		if len(doc.Changes.Artifacts)+len(doc.Changes.Objects) == 0 {
			fmt.Fprintf(w, "  nothing differs (%d artifacts unchanged)\n", doc.Changes.UnchangedArtifacts)
		}
	}
	if doc.ChangesNote != "" {
		fmt.Fprintf(w, "  %s\n", doc.ChangesNote)
	}
	if len(doc.Suggested) > 0 {
		fmt.Fprintf(w, "\nnext:\n")
		for _, s := range doc.Suggested {
			fmt.Fprintf(w, "  %s\n", s)
		}
	}
}
