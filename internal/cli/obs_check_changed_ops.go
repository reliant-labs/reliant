package cli

// THE OPS A RELEASE CHANGED, judged by `forge obs check` (runtime-context
// proposal, section C).
//
// The service-level findings say THAT a service got worse; the diff the judged
// window runs says WHAT changed. This file finds the ops that diff changed
// (internal/opindex maps it onto component methods and rpcs), reads each one's
// figures in both windows (obsbackend.OpsReader, panels both backends run),
// and hands them to releasehealth.WithChangedOps, which judges them by the
// same rule a control plane would. It runs the same way over both backends,
// after the report is in: a local report has no ops of its own, and a control
// plane that predates the rule sends none.
//
// Which diff:
//
//   - a promotion (hosted): the move the ledger records, from the release the
//     env ran before to the judged one (`forge env releases show`'s range),
//     mapped commit by commit so each op names the commit that changed it. A
//     move behind (a rollback) maps the commits it takes away.
//   - a local env (no promotions, P is --since or the last env up): what
//     changed since P, so from the newest commit of HEAD committed before P
//     to the working tree, uncommitted changes included: hot reload runs them.
//
// None of it can fail the check. The commits not being in this checkout (a
// shallow clone, another repository), no checkout at all (the hosted MCP
// tools), a backend that cannot read per-op figures: each is a not_evaluated
// entry starting releasehealth.NotEvaluatedChangedOps, and the rest of the
// report is judged as before.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/internal/opindex"
	"github.com/reliant-labs/forge/internal/releasetimeline"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/releasehealth"
)

// changedOp is one op the judged diff changed, before its figures are read.
type changedOp struct {
	Name string
	// Kind is releasehealth.OpKindComponent or OpKindRPC.
	Kind string
	// Commits changed it, newest first; SHA "" is uncommitted changes.
	Commits []releasehealth.OpCommit
}

// opChanges is what changed in the code a judged after window runs.
type opChanges struct {
	// Read says the diff was mapped: Ops is then the whole answer, none
	// meaning nothing changed. A source that could not look says why in
	// NotEvaluated and leaves Read false.
	Read bool
	Ops  []changedOp
	// Base is the commit the diff is measured from (blast-radius --base).
	Base string
	// NotEvaluated are the report's not_evaluated entries this adds, each
	// prefixed releasehealth.NotEvaluatedChangedOps.
	NotEvaluated []string
}

// opChangeFunc reads the ops the code live in env's judged after window
// changed, for the release s names (a promotion's subject, or only a local
// env's PromotedAt).
type opChangeFunc func(ctx context.Context, env string, s releasehealth.Subject) opChanges

// opChangesTimeout bounds the git and index work: the changed ops are an
// addition to a check, never a reason for it to hang.
const opChangesTimeout = time.Minute

func notEvaluatedOps(format string, args ...any) opChanges {
	return opChanges{NotEvaluated: []string{releasehealth.NotEvaluatedChangedOps + " " + fmt.Sprintf(format, args...)}}
}

// noCheckoutOpChanges is the source of a caller with no repository: the
// hosted MCP tools.
func noCheckoutOpChanges(context.Context, string, releasehealth.Subject) opChanges {
	return notEvaluatedOps("there is no checkout here to map the release's commits to ops; run `forge obs check` in the repository")
}

// checkoutOpChanges reads the changed ops from this checkout: the default.
func checkoutOpChanges(ctx context.Context, env string, s releasehealth.Subject) opChanges {
	ctx, cancel := context.WithTimeout(ctx, opChangesTimeout)
	defer cancel()
	cfg, err := findProjectConfigFile()
	if err != nil {
		return notEvaluatedOps("there is no forge project checkout here to map the release's commits to ops")
	}
	dir := filepath.Dir(cfg)
	if _, err := opGit(ctx, dir, "rev-parse", "--git-dir"); err != nil {
		return notEvaluatedOps("%s is not a git checkout, so the release's commits cannot be mapped to ops", dir)
	}
	var base, head string
	if promotionSubject(s) {
		var skip opChanges
		if base, head, skip = promotionRange(ctx, env, s); base == "" {
			return skip
		}
	} else {
		if base, err = localOpBase(ctx, dir, s.PromotedAt); err != nil {
			return notEvaluatedOps("git could not find the commit live at %s: %v", s.PromotedAt.UTC().Format(time.RFC3339), err)
		}
		if base == "" {
			return notEvaluatedOps("no commit of HEAD predates %s, so there is no base to diff the working tree against", s.PromotedAt.UTC().Format(time.RFC3339))
		}
		// Nothing committed or edited since P: no op changed, and the
		// index need not be built.
		if tip, err := opGit(ctx, dir, "rev-parse", "HEAD"); err == nil && tip == base {
			if changes, err := opindex.Changes(ctx, dir, base, ""); err == nil && len(changes) == 0 {
				return opChanges{Read: true, Base: base}
			}
		}
	}
	return mapOpChanges(ctx, dir, base, head)
}

// promotionSubject reports whether s names a promotion rather than a local
// env's instant.
func promotionSubject(s releasehealth.Subject) bool {
	return s.PromotionID != "" || s.Release != "" || s.GitCommit != ""
}

// promotionRange is the commit range the promotion s moved env over, from
// env's release ledger: base..head, or base "" and why not.
func promotionRange(ctx context.Context, env string, s releasehealth.Subject) (base, head string, skip opChanges) {
	b, err := openReleasesBackend(ctx, env)
	if err != nil {
		return "", "", notEvaluatedOps("the release ledger of %s could not be opened: %v", env, err)
	}
	data, err := b.Source.Load(ctx, env, time.Time{})
	if err != nil {
		return "", "", notEvaluatedOps("the release ledger of %s could not be read: %v", env, err)
	}
	var from, to string
	tl := releasetimeline.Assemble(data, time.Time{}, 0)
	for _, ref := range []string{s.PromotionID, s.Release} {
		if ref == "" {
			continue
		}
		if e, ferr := tl.Find(ref); ferr == nil && e.Range != nil {
			from, to = e.Range.FromCommit, e.Range.ToCommit
			if e.Range.Reverts {
				// A move behind runs the older commit: what it changed is
				// the commits it takes away.
				from, to = to, from
			}
			break
		}
	}
	// A ledger whose entry carries no range still names the two releases.
	commitOf := func(version string) string {
		for _, r := range data.Releases {
			if r.Version == version && version != "" {
				return r.Git.Commit
			}
		}
		return ""
	}
	if to == "" {
		to = firstNonEmptyString(s.GitCommit, commitOf(s.Release))
	}
	if from == "" {
		from = commitOf(s.PreviousRelease)
	}
	if from == "" || to == "" {
		return "", "", notEvaluatedOps("no commit range is recorded for %s (the release before it, or its commit, is not in the ledger)", subjectLabel(s))
	}
	return from, to, opChanges{}
}

func subjectLabel(s releasehealth.Subject) string {
	if s.Release != "" {
		return s.Release
	}
	return "promotion " + s.PromotionID
}

// localOpBase is the commit a local env's after window measures from: the
// newest commit of HEAD committed before p, the code live at P as far as
// history says. It reads the committer date, so a commit rebased after P
// counts as after it — the safe side: its ops are named, not missed. "" when
// no commit of HEAD predates p.
func localOpBase(ctx context.Context, dir string, p time.Time) (string, error) {
	return opGit(ctx, dir, "rev-list", "-1", "--before="+p.UTC().Format(time.RFC3339), "HEAD")
}

// mapOpChanges maps base..head (head "" the working tree) to ops, commit by
// commit, after checking both ends are in the checkout.
func mapOpChanges(ctx context.Context, dir, base, head string) opChanges {
	for _, rev := range []string{base, head} {
		if rev == "" {
			continue
		}
		if _, err := opGit(ctx, dir, "cat-file", "-e", rev+"^{commit}"); err != nil {
			return notEvaluatedOps("the release's commits are not in this checkout (%s..%s; a shallow clone, or another repository)", short7(base), short7(head))
		}
	}
	ix, err := opindex.Build(dir)
	if err != nil {
		return notEvaluatedOps("this checkout's ops could not be indexed: %v", err)
	}
	m, err := ix.MapRange(ctx, base, head)
	if err != nil {
		return notEvaluatedOps("git could not map %s..%s to ops: %v", short7(base), short7(head), err)
	}
	out := opChanges{Read: true, Base: m.Base, Ops: []changedOp{}}
	for _, mo := range m.Ops {
		if mo.Removed {
			continue // the release deleted it: nothing of it runs to judge
		}
		op := changedOp{Name: mo.Name, Kind: mo.Kind}
		for _, c := range mo.Commits {
			op.Commits = append(op.Commits, releasehealth.OpCommit{SHA: c.SHA, Subject: c.Subject, PR: c.PR})
		}
		out.Ops = append(out.Ops, op)
	}
	if m.Truncated {
		out.NotEvaluated = append(out.NotEvaluated, fmt.Sprintf("%s only the newest %d of the range's %d commits were mapped to ops",
			releasehealth.NotEvaluatedChangedOps, release.MaxRangeCommits, m.Total))
	}
	return out
}

func short7(sha string) string {
	if sha == "" {
		return "working tree"
	}
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// opGit runs git in dir, its output trimmed: no optional locks (a read must
// not leave another git in this checkout facing index.lock), and never a
// credential prompt.
func opGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

// changedOpHealth reads each changed op's figures in r's before and after
// windows, one OpHealth per service the op ran in (an op no window saw is
// one, with no service and no calls). note, when not "", is why the figures
// could not be read. explain compiles the statements without running them.
//
// When r's subject names a release (a hosted promotion), both windows are
// read per service.version (joinOpHealth): each op's After is the release's
// own calls, and its Before one baseline version's, so an old version still
// in rotation on either side cannot make a changed op look broken, or
// healthy. A local subject names no release: every span is the judged code,
// and both windows are read whole, as `forge obs ops` reads them.
func changedOpHealth(ctx context.Context, b obsbackend.Backend, r releasehealth.Report, ops []changedOp, explain bool) (out []releasehealth.OpHealth, stmts []obsbackend.Statement, note string) {
	if len(ops) == 0 {
		return []releasehealth.OpHealth{}, nil, ""
	}
	reader, ok := b.(obsbackend.OpsReader)
	if !ok {
		return nil, nil, fmt.Sprintf("%s the %s backend cannot read per-op figures", releasehealth.NotEvaluatedChangedOps, b.Info().Backend)
	}
	names := make([]string, 0, len(ops))
	for _, o := range ops {
		names = append(names, o.Name)
	}
	rel := r.Subject.Release
	read := func(w releasehealth.Range, byVersion bool) (map[opKey][]obsbackend.OpStats, error) {
		got := map[opKey][]obsbackend.OpStats{}
		if w.To.Sub(w.From) < time.Second {
			return got, nil // a window too short to compile has no calls to count
		}
		res, err := reader.Ops(ctx, obsbackend.OpsRequest{
			Narrow:    obsbackend.Narrow{Range: obsbackend.Range{From: w.From, To: w.To}, Explain: explain},
			Names:     names,
			ByVersion: byVersion,
		})
		stmts = append(stmts, res.Statements...)
		if err != nil {
			return nil, err
		}
		for _, s := range res.Ops {
			k := opKey{s.Service, s.Name}
			got[k] = append(got[k], s)
		}
		return got, nil
	}
	before, err := read(r.Before, rel != "")
	if err == nil {
		var after map[opKey][]obsbackend.OpStats
		if after, err = read(r.After, rel != ""); err == nil {
			out = joinOpHealth(ops, before, after, r.Subject)
		}
	}
	if err != nil {
		return nil, stmts, fmt.Sprintf("%s the changed ops' figures could not be read: %v", releasehealth.NotEvaluatedChangedOps, err)
	}
	return out, stmts, ""
}

// opKey is one op of one service.
type opKey struct{ service, op string }

// joinOpHealth pairs each changed op's per-service figures across the two
// windows, ordered by op and service, for the judged subject s. A subject
// with no release (a local env) has each op's whole figures, one row per key
// in each window. With a release the rows are per service.version:
//
//   - After is the release's own row (AfterVersion the release; zero calls
//     when the release made none, which the rule reads as too thin to
//     compare) — unless every after row of the op carries no version at all:
//     the spans say nothing of which release served them, so After keeps
//     their unscoped figures with AfterVersion "", which the rule names in
//     not_evaluated rather than judges.
//   - Before is the baseline's row (BeforeVersion; beforeOf): the previous
//     release when the subject records one, else the version that served
//     most of that op's calls in the before window. The judged release's own
//     calls there are never the baseline: they are a canary of it, not what
//     it replaced.
func joinOpHealth(ops []changedOp, before, after map[opKey][]obsbackend.OpStats, s releasehealth.Subject) []releasehealth.OpHealth {
	rel := s.Release
	services := map[string][]string{}
	for _, side := range []map[opKey][]obsbackend.OpStats{before, after} {
		for k := range side {
			if !slices.Contains(services[k.op], k.service) {
				services[k.op] = append(services[k.op], k.service)
			}
		}
	}
	window := func(s obsbackend.OpStats) releasehealth.WindowStats {
		return releasehealth.WindowStats{Requests: int64(math.Round(s.Calls)), ErrorRate: s.ErrorRatio, P50Ms: s.P50Ms, P95Ms: s.P95Ms, P99Ms: s.P99Ms}
	}
	first := func(rows []obsbackend.OpStats) obsbackend.OpStats {
		if len(rows) == 0 {
			return obsbackend.OpStats{}
		}
		return rows[0]
	}
	// afterOf is a key's After and the version it is restricted to.
	afterOf := func(rows []obsbackend.OpStats) (obsbackend.OpStats, string) {
		if rel == "" {
			return first(rows), ""
		}
		if len(rows) > 0 && !slices.ContainsFunc(rows, func(s obsbackend.OpStats) bool { return s.Version != "" }) {
			return first(rows), "" // no span says which release served it
		}
		for _, s := range rows {
			if s.Version == rel {
				return s, rel
			}
		}
		return obsbackend.OpStats{}, rel // the release made no calls of it
	}
	prev := s.PreviousRelease
	if prev == rel {
		prev = "" // a re-promotion: the release cannot be its own baseline
	}
	// beforeOf is a key's Before and the baseline version it is restricted
	// to. Per op, not per service: the baseline is the version that served
	// this op, which a partial rollout can make differ between ops. With no
	// version other than the release's among the rows, there is no baseline:
	// Before keeps the unscoped figures, if any, with BeforeVersion "", which
	// the rule names in not_evaluated.
	beforeOf := func(rows []obsbackend.OpStats) (obsbackend.OpStats, string) {
		if rel == "" {
			return first(rows), ""
		}
		var base []obsbackend.OpStats
		for _, r := range rows {
			if r.Version != "" && r.Version != rel {
				base = append(base, r)
			}
		}
		if len(base) == 0 && (len(rows) > 0 || prev == "") {
			for _, r := range rows {
				if r.Version == "" {
					return r, ""
				}
			}
			return obsbackend.OpStats{}, ""
		}
		if prev != "" {
			for _, r := range base {
				if r.Version == prev {
					return r, prev
				}
			}
			// The previous release made no calls of it (an op it lacked,
			// or a window it no longer served): zero calls, too thin.
			return obsbackend.OpStats{}, prev
		}
		best := base[0]
		for _, r := range base[1:] {
			if r.Calls > best.Calls || (r.Calls == best.Calls && r.Version < best.Version) {
				best = r
			}
		}
		return best, best.Version
	}
	out := []releasehealth.OpHealth{}
	for _, o := range ops {
		svcs := services[o.Name]
		if len(svcs) == 0 {
			_, av := afterOf(nil)
			_, bv := beforeOf(nil)
			out = append(out, releasehealth.OpHealth{Op: o.Name, Kind: o.Kind, AfterVersion: av, BeforeVersion: bv, Commits: o.Commits})
			continue
		}
		for _, svc := range svcs {
			k := opKey{svc, o.Name}
			a, av := afterOf(after[k])
			b, bv := beforeOf(before[k])
			out = append(out, releasehealth.OpHealth{Op: o.Name, Service: svc, Kind: o.Kind,
				Before: window(b), After: window(a), AfterVersion: av, BeforeVersion: bv, Commits: o.Commits})
		}
	}
	slices.SortStableFunc(out, func(a, b releasehealth.OpHealth) int {
		if c := strings.Compare(a.Op, b.Op); c != 0 {
			return c
		}
		return strings.Compare(a.Service, b.Service)
	})
	return out
}

// checkChangedOps applies the changed-op rule to a check's report. The diff
// is read once per check, on first use, and kept: a --wait loop re-judges
// the same promotion every poll, and its git work must not repeat.
type checkChangedOps struct {
	d   obsDeps
	b   obsbackend.Backend
	env string

	read    bool
	changes opChanges
}

func (c *checkChangedOps) source(ctx context.Context, s releasehealth.Subject) opChanges {
	if !c.read {
		c.changes, c.read = c.d.opChanges()(ctx, c.env, s), true
	}
	return c.changes
}

// apply judges the ops the report's release changed. overrides are the
// request's --threshold pairs. When the diff or the figures cannot be read,
// a report that carries ops of its own (a control plane that judges them)
// is held to the request's thresholds, and one that carries none says why in
// not_evaluated. WithChangedOps drops the changed-op not_evaluated entries
// it re-derives, so this source's own entries are added after it.
func (c *checkChangedOps) apply(ctx context.Context, r releasehealth.Report, overrides map[string]string) releasehealth.Report {
	ch := c.source(ctx, r.Subject)
	var ops []releasehealth.OpHealth
	notes := ch.NotEvaluated
	if ch.Read {
		var note string
		if ops, _, note = changedOpHealth(ctx, c.b, r, ch.Ops, false); note != "" {
			notes = append(slices.Clone(notes), note)
		}
	}
	if ops == nil {
		if len(r.ChangedOps) > 0 {
			return releasehealth.WithChangedOps(r, nil, overrides)
		}
		r.NotEvaluated = append(slices.Clone(r.NotEvaluated), notes...)
		return r
	}
	r = releasehealth.WithChangedOps(r, ops, overrides)
	r.NotEvaluated = append(r.NotEvaluated, notes...)
	return r
}

// writeChangedOpsText is the changed-ops table of `forge obs check`'s text
// output: each op's figures and the newest commit that changed it.
func writeChangedOpsText(w io.Writer, ops []releasehealth.OpHealth) {
	rows := make([][]string, len(ops))
	for i, o := range ops {
		changed := ""
		if c, ok := o.Newest(); ok {
			changed = "uncommitted"
			if c.SHA != "" {
				changed = short7(c.SHA)
			}
			if c.PR > 0 {
				changed += fmt.Sprintf(" (#%d)", c.PR)
			}
		}
		rows[i] = []string{o.Op, o.Service,
			fmt.Sprintf("%d -> %d", o.Before.Requests, o.After.Requests),
			fmt.Sprintf("%.1f%% -> %.1f%%", o.Before.ErrorRate*100, o.After.ErrorRate*100),
			fmt.Sprintf("%.0fms -> %.0fms", o.Before.P95Ms, o.After.P95Ms), changed}
	}
	fmt.Fprintln(w)
	obsout.Table(w, []string{"CHANGED OP", "SERVICE", "CALLS", "ERRORS", "P95", "CHANGED IN"}, rows, 48)
}

// changedOpSuggestions are the next commands for each changed-op finding:
// the op's traces (`--route` matches an rpc's root server span; a component
// span is found by its forge.op attribute, as `forge obs ops` suggests), and
// the blast radius of the commit that changed it.
func changedOpSuggestions(env string, r releasehealth.Report, base string) []string {
	var cmds []string
	for _, f := range r.Findings {
		if !slices.Contains(release.ChangedOpFindings, f.Code) || f.Op == "" {
			continue
		}
		args := []string{"forge", "obs", "traces", env}
		if f.Service != "" {
			args = append(args, "--service", f.Service)
		}
		if obsbackend.OpKindOf(f.Op) == obsbackend.OpKindComponent {
			args = append(args, "--filter", "span.attr.forge.op="+f.Op)
		} else {
			args = append(args, "--route", f.Op)
		}
		if f.Code == release.HealthChangedOpErrors {
			args = append(args, "--errors")
		} else {
			args = append(args, "--slow")
		}
		cmds = append(cmds, obsout.Command(args...))
		if base != "" {
			br := []string{"forge", "obs", "blast-radius", env, "--base", base}
			if f.Commit != "" {
				br = append(br, "--head", f.Commit)
			}
			cmds = append(cmds, obsout.Command(br...))
		}
	}
	return cmds
}
