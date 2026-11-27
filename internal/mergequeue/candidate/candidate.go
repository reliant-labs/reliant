// Copyright (c) 2025 Reliant Labs
package candidate

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// Entry is one queued PR at the head that was reviewed.
type Entry struct {
	Repo   ghport.Repo
	Number int
	// Head is the reviewed head commit. In [Candidate.Included] it is the
	// head that lands: the regen commit's, for a regenerated entry.
	Head  string
	Title string
}

// Reason is why an entry left the batch.
type Reason string

const (
	// ReasonConflict: the head conflicts with M, in paths regen may not
	// settle (or the repository declares no regen). The PR needs a fix.
	ReasonConflict Reason = "conflict"
	// ReasonBatchConflict: the head applies to M but conflicts with an
	// earlier entry of this batch. Nothing is wrong with the PR; it waits
	// for the next batch, built on the main those entries land.
	ReasonBatchConflict Reason = "batch_conflict"
	// ReasonRegenFailed: regen ran and failed (or was refused by the
	// sandbox, or timed out).
	ReasonRegenFailed Reason = "regen_failed"
	// ReasonNotGeneratedOnly: regen's result changed paths outside
	// `generated:` ([GeneratedOnly] does not hold), so it is not the
	// reviewed change and needs a review.
	ReasonNotGeneratedOnly Reason = "not_generated_only"
	// ReasonHeadMoved: the PR is closed, or its head is no longer the one
	// queued, so the regen commit was not pushed.
	ReasonHeadMoved Reason = "head_moved"
)

// Ejection is an entry left out of the candidate.
type Ejection struct {
	Entry  Entry
	Reason Reason
	// Paths are the conflicting paths, or for ReasonNotGeneratedOnly the
	// non-generated paths regen changed.
	Paths []string
	// Detail is a human-readable note: regen's output tail, for one.
	Detail string
}

// Regenerated is an entry kept by regenerating it: its new head is on the
// PR's branch, and the caller records that head's verdict under the rule
// `generated_only`.
type Regenerated struct {
	// Entry is the entry as queued, at the reviewed head.
	Entry Entry
	// NewHead is the "merge M + regen" commit (parents: Entry.Head, then
	// Base), now the PR's head.
	NewHead string
	// Base is M, the main the regen ran against.
	Base string
	// Conflicts are the generated paths the reviewed head conflicted on.
	Conflicts []string
}

// Candidate is a built batch.
type Candidate struct {
	// Base is M, the main tip the candidate starts from.
	Base string
	// Commit is the candidate's tip, pushed to Ref. Empty when every entry
	// was ejected (nothing is pushed then).
	Commit string
	// Ref is refs/heads/reliant-queue/<batch-id>.
	Ref string
	// Trees are T_1..T_n: Trees[i] is the tree after squashing Included[0..i]
	// onto Base, so the last one is the tree the gate tests.
	Trees []string
	// Included are the entries in the candidate, in order.
	Included    []Entry
	Regenerated []Regenerated
}

// Config is what a build reads from the batch and the repository's
// pipeline.yaml.
type Config struct {
	// BatchID names the batch: its candidate ref and the Reliant-Batch
	// trailer.
	BatchID string
	// Generated are pipeline.yaml's `generated:` globs.
	Generated []string
	// Regen is pipeline.yaml's `regen:` command; empty means a conflict
	// always ejects.
	Regen string
}

// ConfigFor is the build configuration for a batch of a repository whose
// resolved pipeline.yaml is r.
func ConfigFor(batchID string, r config.Resolved) Config {
	return Config{BatchID: batchID, Generated: slices.Clone(r.Generated), Regen: r.Regen}
}

// Ports are what a build works through.
type Ports struct {
	// Git is the queue's workdir for the repository. Build fetches M and
	// the heads into it from Remote when they are missing.
	Git gitport.Port
	// GitHub reads PRs, refs and commits: to check a PR before pushing its
	// regen commit, and to recognise what a retried build pushed before.
	GitHub ghport.Client
	// Remote is the URL Git fetches from and pushes to, with credentials
	// in Git's environment, never in the URL.
	Remote string
	// Runner runs regen; nil is SandboxRunner{}.
	Runner Runner
}

// QueueRef is the ref a batch's candidate is pushed to.
func QueueRef(batchID string) string { return "refs/heads/reliant-queue/" + batchID }

// ErrRefTaken means the batch's candidate ref already holds a different
// candidate: batch ids name one build each.
var ErrRefTaken = errors.New("candidate: the batch's candidate ref holds a different candidate")

var shaRE = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// Build builds the candidate of entries on base (M, a commit sha), in order,
// and pushes it. Entries that cannot be squashed are returned as ejections,
// and the candidate is built from the rest. An error means nothing about the
// batch was decided (a git or GitHub failure); a retry with the same inputs
// is safe.
func Build(ctx context.Context, p Ports, base string, entries []Entry, cfg Config) (Candidate, []Ejection, error) {
	if err := validate(p, base, entries, cfg); err != nil {
		return Candidate{}, nil, err
	}
	runner := p.Runner
	if runner == nil {
		runner = SandboxRunner{}
	}
	b := &builder{
		p: p, cfg: cfg, m: base, runner: runner, repo: entries[0].Repo,
		local: localGit{dir: p.Git.Dir(), id: gitport.DefaultIdentity()},
	}
	shas := []string{base}
	for _, e := range entries {
		shas = append(shas, e.Head)
	}
	if err := b.ensure(ctx, shas...); err != nil {
		return Candidate{}, nil, err
	}

	cand := Candidate{Base: base, Ref: QueueRef(cfg.BatchID)}
	var ejected []Ejection
	tip := base
	for _, e := range entries {
		st, err := b.add(ctx, tip, e)
		if err != nil {
			return Candidate{}, nil, fmt.Errorf("candidate: %s#%d: %w", e.Repo, e.Number, err)
		}
		if st.ejection != nil {
			ejected = append(ejected, *st.ejection)
			continue
		}
		tip = st.commit
		cand.Trees = append(cand.Trees, st.tree)
		cand.Included = append(cand.Included, st.entry)
		if st.regen != nil {
			cand.Regenerated = append(cand.Regenerated, *st.regen)
		}
	}
	if len(cand.Included) == 0 {
		return cand, ejected, nil
	}
	commit, err := b.pushCandidate(ctx, cand, tip)
	if err != nil {
		return Candidate{}, nil, err
	}
	cand.Commit = commit
	return cand, ejected, nil
}

func validate(p Ports, base string, entries []Entry, cfg Config) error {
	switch {
	case p.Git == nil || p.GitHub == nil || p.Remote == "":
		return errors.New("candidate: Ports needs Git, GitHub and Remote")
	case !shaRE.MatchString(base):
		return fmt.Errorf("candidate: base %q is not a commit sha", base)
	case len(entries) == 0:
		return errors.New("candidate: a batch needs at least one entry")
	case cfg.BatchID == "" || strings.Contains(cfg.BatchID, "/"):
		return fmt.Errorf("candidate: batch id %q must be one non-empty ref component", cfg.BatchID)
	}
	if err := ghport.ValidateRef(QueueRef(cfg.BatchID)); err != nil {
		return fmt.Errorf("candidate: batch id %q: %w", cfg.BatchID, err)
	}
	seen := map[int]bool{}
	for _, e := range entries {
		switch {
		case e.Repo != entries[0].Repo:
			return fmt.Errorf("candidate: a batch is one repository's, not %s and %s", entries[0].Repo, e.Repo)
		case e.Number <= 0 || seen[e.Number]:
			return fmt.Errorf("candidate: entry #%d is missing or repeated", e.Number)
		case !shaRE.MatchString(e.Head):
			return fmt.Errorf("candidate: #%d head %q is not a commit sha", e.Number, e.Head)
		}
		seen[e.Number] = true
	}
	return nil
}

type builder struct {
	p      Ports
	cfg    Config
	m      string
	runner Runner
	repo   ghport.Repo
	local  localGit
}

// step is one entry's outcome: in the candidate, or ejected.
type step struct {
	entry    Entry
	commit   string
	tree     string
	regen    *Regenerated
	ejection *Ejection
}

func eject(e Entry, reason Reason, paths []string, detail string) step {
	return step{ejection: &Ejection{Entry: e, Reason: reason, Paths: paths, Detail: detail}}
}

// ensure fetches the commits the workdir does not have yet.
func (b *builder) ensure(ctx context.Context, shas ...string) error {
	var missing []string
	for _, s := range shas {
		if _, err := b.p.Git.ResolveCommit(ctx, s); err != nil && !slices.Contains(missing, s) {
			missing = append(missing, s)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if err := b.p.Git.Fetch(ctx, b.p.Remote, missing...); err != nil {
		return fmt.Errorf("candidate: fetch %d commit(s): %w", len(missing), err)
	}
	return nil
}

func (b *builder) trailers(e Entry, head string) []gitport.Trailer {
	return []gitport.Trailer{gitport.PRTrailer(e.Repo.String(), e.Number, head), gitport.BatchTrailer(b.cfg.BatchID)}
}

func (b *builder) squash(ctx context.Context, tip string, e Entry, head string) (gitport.Result, error) {
	return b.p.Git.Squash(ctx, gitport.SquashInput{Base: tip, Head: head, Title: e.Title, Trailer: b.trailers(e, head)})
}

// add squashes e onto tip, or decides why it cannot be.
func (b *builder) add(ctx context.Context, tip string, e Entry) (step, error) {
	res, err := b.squash(ctx, tip, e, e.Head)
	if err == nil {
		return step{entry: e, commit: res.Commit, tree: res.Tree}, nil
	}
	var ce *gitport.ConflictError
	if !errors.As(err, &ce) {
		return step{}, err
	}
	// Against M or against the batch? Only a conflict with M is the PR's
	// to fix, and only one with M can regen settle: the regen commit goes
	// on the PR's branch, which must never carry another PR's changes.
	paths := ce.Paths
	if tip != b.m {
		_, err := b.p.Git.Squash(ctx, gitport.SquashInput{Base: b.m, Head: e.Head, Title: e.Title})
		var mc *gitport.ConflictError
		switch {
		case err == nil:
			return eject(e, ReasonBatchConflict, ce.Paths, "applies to main but conflicts with an earlier entry of this batch"), nil
		case !errors.As(err, &mc):
			return step{}, err
		}
		paths = mc.Paths
	}
	if b.cfg.Regen == "" || len(NotGenerated(paths, b.cfg.Generated)) > 0 {
		return eject(e, ReasonConflict, paths, ""), nil
	}
	return b.regenerate(ctx, tip, e, paths)
}

// pushCandidate pushes the candidate to its ref, creating it. A ref that
// already holds the same candidate (a retried build) is adopted.
func (b *builder) pushCandidate(ctx context.Context, cand Candidate, tip string) (string, error) {
	perr := b.p.Git.Push(ctx, b.p.Remote, gitport.PushInput{SHA: tip, Ref: cand.Ref, ExpectedOld: strings.Repeat("0", 40)})
	if perr == nil {
		return tip, nil
	}
	ref, err := b.p.GitHub.GetRef(ctx, b.repo, cand.Ref)
	if err != nil {
		return "", fmt.Errorf("candidate: push %s: %w (and reading it back: %w)", cand.Ref, perr, err)
	}
	if ref.SHA == tip {
		return tip, nil
	}
	same, err := b.sameCandidate(ctx, ref.SHA, cand)
	if err != nil {
		return "", err
	}
	if !same {
		return "", fmt.Errorf("%w: %s is at %s", ErrRefTaken, cand.Ref, ref.SHA)
	}
	return ref.SHA, nil
}

// sameCandidate reports whether sha is a candidate with cand's shape: one
// single-parent commit per included entry, each with cand's tree and the
// entry's trailers, on cand's base.
func (b *builder) sameCandidate(ctx context.Context, sha string, cand Candidate) (bool, error) {
	for i := len(cand.Included) - 1; i >= 0; i-- {
		c, err := b.p.GitHub.GetCommit(ctx, b.repo, sha)
		if err != nil {
			return false, fmt.Errorf("candidate: read %s: %w", sha, err)
		}
		if c.TreeSHA != cand.Trees[i] || len(c.Parents) != 1 {
			return false, nil
		}
		got := gitport.ParseTrailers(c.Message)
		for _, want := range b.trailers(cand.Included[i], cand.Included[i].Head) {
			if !slices.Contains(got, want) {
				return false, nil
			}
		}
		sha = c.Parents[0]
	}
	return sha == cand.Base, nil
}
