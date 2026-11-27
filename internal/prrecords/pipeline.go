// Copyright (c) 2026 Reliant Labs
package prrecords

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/ghcontents"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// The PR pipeline's projection (pipeline redesign §2, §4.1, §4.2; build
// plan task M3). For a repository enrolled in the pipeline (mode shadow or
// on), the projector, which is the one writer of the PR mirror, also:
//
//   - keeps each open PR's pr_pipeline row: created on its first event,
//     started over on a new head (ResetPRPipelineForHead), closed with the
//     PR, and woken (next_action decide, due now) on the events a decision
//     may follow: opened, reopened, ready_for_review, synchronize, closed,
//     and a comment on the PR. The record_due trigger (M4) turns the wake
//     into one run of the `pr` workflow, whose pr.next decides. A label
//     event wakes nothing: labels are display, never input;
//   - keeps the repository's config: on a push to its base branch, the
//     .reliant/pipeline.yaml at the pushed commit, validated, with the gate
//     stages it turns off or back on recorded as setting changes. Config
//     is never read from a PR head.
//
// Every write is level-triggered (it reads the record as applied, not the
// event) and a compare-and-set, so a redelivered or out-of-order event, or
// a concurrent writer, can only repeat or lose a wake, never corrupt a row.
// A repository that is not enrolled sees none of it.

// NextActionDecide is the next action a wake sets: pr.next decides what is
// due.
const NextActionDecide = "decide"

// casTries bounds the projector's retries of a compare-and-set another
// writer won. Losing every one fails the delivery, which is retried whole.
const casTries = 4

// pipelineFamilies are the event families of an enrolled repository the
// projector reads: the records' own, the comments that wake a PR, and
// pushes to the base branch.
var pipelineFamilies = map[string]bool{
	"pull_request": true, "pull_request_review": true, "check_run": true,
	"issue_comment": true, "pull_request_review_comment": true, "push": true,
}

// enrolment returns the enrolment of the event's repository when the
// pipeline projection is on and the repository is enrolled with a mode
// other than off, else nil. Lookups are cached per delivery.
func (p *projector) enrolment(ctx context.Context, provider string, ev Event, cache map[string]*core.PipelineRepo) (*core.PipelineRepo, error) {
	family, _, _ := strings.Cut(ev.Type, ".")
	if p.pipeline == nil || provider != ProviderGitHub || ev.ResourceKey == "" || !pipelineFamilies[family] {
		return nil, nil
	}
	if repo, ok := cache[ev.ResourceKey]; ok {
		return repo, nil
	}
	repo, err := p.pipeline.GetPipelineRepoByRepository(ctx, provider, ev.ResourceKey)
	if err != nil {
		return nil, fmt.Errorf("read the pipeline enrolment of repository %s: %w", ev.ResourceKey, err)
	}
	if repo != nil && repo.Mode == core.PipelineRepoModeOff {
		repo = nil
	}
	cache[ev.ResourceKey] = repo
	return repo, nil
}

// projectPipeline projects one event of an enrolled repository.
func (p *projector) projectPipeline(ctx context.Context, provider string, ev Event, repo *core.PipelineRepo, now time.Time) error {
	family, action, _ := strings.Cut(ev.Type, ".")
	switch family {
	case "push":
		return p.refreshConfig(ctx, provider, ev, repo)
	case "pull_request":
		switch action {
		case "opened", "reopened", "ready_for_review", "synchronize", "closed":
		default:
			// labeled, unlabeled, edited, …: nothing a decision follows.
			return nil
		}
		number, ok := intOf(mapOf(ev.Data["pull_request"])["number"])
		if !ok {
			return nil
		}
		return p.wake(ctx, provider, ev.ResourceKey, number, repo, now)
	case "issue_comment":
		if action != "created" || ev.Attributes["is_pull_request"] != "true" {
			return nil
		}
		number, ok := intOf(mapOf(ev.Data["issue"])["number"])
		if !ok {
			return nil
		}
		return p.wake(ctx, provider, ev.ResourceKey, number, repo, now)
	case "pull_request_review_comment":
		if action != "created" {
			return nil
		}
		number, ok := intOf(mapOf(ev.Data["pull_request"])["number"])
		if !ok {
			return nil
		}
		return p.wake(ctx, provider, ev.ResourceKey, number, repo, now)
	}
	return nil
}

// wake brings a PR's pr_pipeline row in line with its record and sets it
// due for a decision now:
//
//   - an open PR with no row gets one (draft or awaiting_review, due now);
//   - an open PR on a head other than its row's, or reopened after it was
//     closed, starts over on that head (ResetPRPipelineForHead, due now);
//   - a closed or merged PR's row becomes closed (unless the queue already
//     landed it), due now so the run that owns it can wind down;
//   - otherwise the row is only woken, and its draft state follows the
//     record's.
//
// A PR with no record yet (a comment on a PR no event has recorded since
// enrolment), or a closed one with no row, is left alone: the next PR event
// creates the row.
func (p *projector) wake(ctx context.Context, provider, repositoryID string, number int, repo *core.PipelineRepo, now time.Time) error {
	due := core.PRPipelineNext{Action: NextActionDecide, At: &now}
	for range casTries {
		rec, err := p.pipeline.GetPRRecordByNumber(ctx, provider, repositoryID, number)
		if err != nil {
			return err
		}
		if rec == nil || rec.HeadSHA == "" {
			return nil
		}
		open := rec.State == core.PRRecordOpen
		var row *core.PRPipeline
		if open {
			state := core.PRPipelineAwaitingReview
			if rec.Draft {
				state = core.PRPipelineDraft
			}
			var created bool
			row, created, err = p.pipeline.EnsurePRPipeline(ctx, core.PRPipelineNew{
				RecordID: rec.ID, OrgID: repo.OrgID, ProjectID: repo.ProjectID, RepoID: repo.ID,
				State: state, StateHeadSHA: rec.HeadSHA, NextAction: due.Action, NextActionAt: due.At,
			})
			if err != nil || created {
				return err
			}
		} else if row, err = p.pipeline.GetPRPipeline(ctx, rec.ID); err != nil || row == nil {
			return err
		}
		err = p.converge(ctx, rec, row, due)
		if !errors.Is(err, core.ErrPipelineVersionConflict) {
			return err
		}
	}
	return fmt.Errorf("pr %s#%d's pipeline row kept changing: %w", repo.Repository, number, core.ErrPipelineVersionConflict)
}

// converge applies wake's rules to an existing row. A lost compare-and-set
// is core.ErrPipelineVersionConflict, for wake to read again.
func (p *projector) converge(ctx context.Context, rec *core.PRRecord, row *core.PRPipeline, due core.PRPipelineNext) error {
	terminal := row.State == core.PRPipelineClosed || row.State == core.PRPipelineLanded
	if rec.State != core.PRRecordOpen {
		if terminal {
			return nil
		}
		to := *row
		to.State = core.PRPipelineClosed
		to.NeedsPersonReason, to.NeedsPersonSince = "", nil
		to.NextAction, to.NextActionAt = due.Action, due.At
		_, err := p.pipeline.TransitionPRPipeline(ctx, row.RecordID, row.Version, to)
		return err
	}
	if row.StateHeadSHA != rec.HeadSHA || terminal {
		reset, err := p.pipeline.ResetPRPipelineForHead(ctx, row.RecordID, row.Version, rec.HeadSHA, due)
		if err != nil || !rec.Draft {
			return err
		}
		// A reset starts at awaiting_review; a draft PR waits as a draft.
		row = reset
	}
	to := *row
	switch {
	case rec.Draft && to.State == core.PRPipelineAwaitingReview:
		to.State = core.PRPipelineDraft
	case !rec.Draft && to.State == core.PRPipelineDraft:
		to.State = core.PRPipelineAwaitingReview
	}
	to.NextAction, to.NextActionAt = due.Action, due.At
	_, err := p.pipeline.TransitionPRPipeline(ctx, row.RecordID, row.Version, to)
	return err
}

// refreshConfig reads .reliant/pipeline.yaml at a commit pushed to the
// repository's base branch and records it, with the gate stages it turns off
// or back on. A file that is missing, unreadable or invalid keeps the config
// in force and is recorded as the repository's config error; the delivery
// still succeeds, since retrying it would read the same file.
//
// A push that cannot have changed the file (GitHub lists every commit's
// changed paths, and none is the file) is skipped once a config is
// recorded, so config_sha is the last commit that could have changed it.
func (p *projector) refreshConfig(ctx context.Context, provider string, ev Event, repo *core.PipelineRepo) error {
	if ev.Attributes["branch"] != repo.BaseBranch || ev.Data["deleted"] == true {
		return nil
	}
	sha := lowerSHA(strOf(ev.Data["after"]))
	if !isSHA(sha) || sha == repo.ConfigSHA || (repo.ConfigSHA != "" && !mayTouch(ev.Data, config.FilePath)) {
		return nil
	}
	resolved, raw, reason := p.readConfig(ctx, repo, sha)
	for range casTries {
		var err error
		if reason != "" {
			slog.InfoContext(ctx, "pipeline config not recorded; the previous config stays in force",
				"repository", repo.Repository, "sha", sha, "reason", reason)
			_, err = p.pipeline.SetPipelineRepoConfigError(ctx, repo.ID, repo.Version, sha, reason)
		} else {
			var changes []core.PipelineConfigChange
			if changes, err = p.stageChanges(ctx, provider, ev, repo, sha, resolved); err != nil {
				return err
			}
			_, err = p.pipeline.RecordPipelineRepoConfig(ctx, repo.ID, repo.Version, sha, raw, changes)
		}
		if !errors.Is(err, core.ErrPipelineVersionConflict) {
			return err
		}
		if repo, err = p.pipeline.GetPipelineRepo(ctx, repo.ID); err != nil || repo == nil || repo.ConfigSHA == sha {
			return err
		}
	}
	return fmt.Errorf("pipeline repo %s kept changing: %w", repo.ID, core.ErrPipelineVersionConflict)
}

// readConfig reads and validates the file at sha. It returns the resolved
// config and the file as JSON (the form config_json stores: JSON is YAML,
// so config.Parse reads it back), or the reason it cannot be recorded.
func (p *projector) readConfig(ctx context.Context, repo *core.PipelineRepo, sha string) (config.Resolved, []byte, string) {
	if p.contents == nil {
		return config.Resolved{}, nil, "this server has no GitHub App to read " + config.FilePath + " with"
	}
	owner, name, _ := strings.Cut(repo.Repository, "/")
	data, err := p.contents.Read(ctx, ghcontents.Request{
		UserID: repo.CreatedByUserID, OrgID: repo.OrgID, Owner: owner, Repo: name, Path: config.FilePath, Ref: sha,
	})
	switch {
	case errors.Is(err, ghcontents.ErrNotFound):
		return config.Resolved{}, nil, config.FilePath + " is missing at " + sha
	case err != nil:
		return config.Resolved{}, nil, "could not read " + config.FilePath + ": " + err.Error()
	}
	resolved, err := config.Load(data)
	if err != nil {
		return config.Resolved{}, nil, config.FilePath + " is invalid: " + err.Error()
	}
	raw, err := configJSON(data)
	if err != nil {
		return config.Resolved{}, nil, config.FilePath + " cannot be stored: " + err.Error()
	}
	return resolved, raw, ""
}

// stageChanges is the setting changes recording resolved over the config in
// force carries (config.StageChanges), each attributed to the author of the
// PR the commit merged, else to whoever pushed it.
func (p *projector) stageChanges(ctx context.Context, provider string, ev Event, repo *core.PipelineRepo, sha string, resolved config.Resolved) ([]core.PipelineConfigChange, error) {
	var old config.Resolved
	if len(repo.ConfigJSON) > 0 {
		// The stored config was valid when it was recorded; one this
		// build no longer reads counts as no config (every stage on).
		old, _ = config.Load(repo.ConfigJSON)
	}
	diff := config.StageChanges(old, resolved)
	if len(diff) == 0 {
		return nil, nil
	}
	actor := core.PipelineConfigChange{
		ChangedByProvider: provider, ChangedByProviderUserID: ev.Attributes["sender_id"], ChangedByLogin: ev.Attributes["sender"],
	}
	merged, err := p.pipeline.GetPRRecordByMergeCommit(ctx, provider, repo.RepositoryID, sha)
	if err != nil {
		return nil, err
	}
	if merged != nil && merged.AuthorID != "" && merged.AuthorLogin != "" {
		actor.ConfigRecordID, actor.ChangedByProviderUserID, actor.ChangedByLogin = merged.ID, merged.AuthorID, merged.AuthorLogin
	}
	if actor.ChangedByProviderUserID == "" || actor.ChangedByLogin == "" {
		// GitHub always names a push's sender; without one there is no
		// actor to record, and a setting change is never recorded
		// anonymously.
		return nil, fmt.Errorf("push %s to %s names no sender to record its gate stage changes against", sha, repo.Repository)
	}
	out := make([]core.PipelineConfigChange, 0, len(diff))
	for _, d := range diff {
		c := actor
		c.Setting, c.OldValue, c.NewValue = core.PipelineSetting(d.Setting), string(d.Old), string(d.New)
		out = append(out, c)
	}
	return out, nil
}

// pushCommitsListed is how many commits a push event lists at most (GitHub
// and the receiver both stop at 20): a push listing that many may have
// more.
const pushCommitsListed = 20

// pushPathsListed is how many paths the receiver keeps of each of a
// commit's added, removed and modified lists.
const pushPathsListed = 50

// mayTouch reports whether a push may have changed path: true unless the
// push lists its commits in full with their changed paths and none names
// it. The receiver drops the paths (and sets truncated) when a push is too
// large to record whole.
func mayTouch(data map[string]any, path string) bool {
	commits := listOf(data["commits"])
	if data["truncated"] == true || len(commits) == 0 || len(commits) >= pushCommitsListed {
		return true
	}
	for _, c := range commits {
		commit := mapOf(c)
		if _, listed := commit["modified"]; !listed {
			return true
		}
		for _, key := range []string{"added", "removed", "modified"} {
			files := stringsOf(commit[key])
			if len(files) >= pushPathsListed {
				return true
			}
			for _, f := range files {
				if f == path {
					return true
				}
			}
		}
	}
	return false
}

// configJSON converts a validated pipeline.yaml to JSON.
func configJSON(data []byte) ([]byte, error) {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}
