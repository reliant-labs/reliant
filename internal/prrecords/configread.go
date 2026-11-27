// Copyright (c) 2026 Reliant Labs
package prrecords

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// ConfigStore is what the [ConfigRefresher] reads and writes: the pending
// reads the projector left, and the config they record. *db.Repo satisfies
// it.
type ConfigStore interface {
	GetPipelineRepo(ctx context.Context, id string) (*core.PipelineRepo, error)
	ListDuePipelineConfigReads(ctx context.Context, now time.Time, limit int) ([]*core.PipelineRepo, error)
	ClaimPipelineConfigRead(ctx context.Context, id string, version int64, sha string, lease time.Time) (*core.PipelineRepo, error)
	DeferPipelineConfigRead(ctx context.Context, id string, version int64, sha, reason string, retry time.Time) (*core.PipelineRepo, error)
	RecordPipelineRepoConfig(ctx context.Context, id string, version int64, sha string, config []byte, changes []core.PipelineConfigChange) (*core.PipelineRepo, error)
	SetPipelineRepoConfigError(ctx context.Context, id string, version int64, sha, reason string) (*core.PipelineRepo, error)
	GetPRRecordByMergeCommit(ctx context.Context, provider, repositoryID, sha string) (*core.PRRecord, error)
}

// ConfigRefresherDeps is what a [ConfigRefresher] is built from.
type ConfigRefresherDeps struct {
	Store ConfigStore
	// Contents reads the file on a gate pool daemon (jobrun's
	// ConfigReader). nil records "no reader" as each repository's config
	// error, and the read stays pending.
	// forge:optional-dep
	Contents Contents
	// forge:optional-dep
	Logger *slog.Logger
	// Now is nil for time.Now.
	// forge:optional-dep
	Now func() time.Time
}

// ConfigRefresher reads the .reliant/pipeline.yaml a base-branch push left
// to read (pipeline_repos.config_pending_sha) and records it: the half of
// the projection that waits on a daemon, so it runs on the worker and never
// in webhook delivery.
//
// A file that is missing or invalid is recorded as the repository's config
// error, keeping the config in force, and the read is done (reading again
// reads the same file). A read that reaches no answer (no gate pool member,
// a member that failed) is recorded as the config error too, and retried
// later, each attempt after a longer wait (configReadBackoff).
type ConfigRefresher struct {
	store    ConfigStore
	contents Contents
	logger   *slog.Logger
	now      func() time.Time
}

// NewConfigRefresher builds a ConfigRefresher. Store is required.
func NewConfigRefresher(d ConfigRefresherDeps) (*ConfigRefresher, error) {
	if d.Store == nil {
		return nil, errors.New("prrecords: ConfigRefresherDeps needs Store")
	}
	r := &ConfigRefresher{store: d.Store, contents: d.Contents, logger: d.Logger, now: d.Now}
	if r.logger == nil {
		r.logger = slog.Default()
	}
	if r.now == nil {
		r.now = time.Now
	}
	return r, nil
}

const (
	// configReadLease holds a claimed read off every other worker: past
	// jobrun's 10-minute budget for a config job.
	configReadLease = 15 * time.Minute
	// configReadConcurrency bounds the reads one Drain runs at once.
	configReadConcurrency = 4
)

// configReadBackoff is how long a read that reached no answer waits after
// its attempt-th try: 30s, doubling, at most an hour.
func configReadBackoff(attempt int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < attempt && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// Run drains the due reads every interval until ctx ends.
func (r *ConfigRefresher) Run(ctx context.Context, every time.Duration) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := r.Drain(ctx); err != nil && ctx.Err() == nil {
			r.logger.WarnContext(ctx, "pipeline config refresh failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Drain reads every pending config read due now, once, and returns how many
// it settled (recorded, or recorded as an error).
func (r *ConfigRefresher) Drain(ctx context.Context) (int, error) {
	due, err := r.store.ListDuePipelineConfigReads(ctx, r.now().UTC(), 0)
	if err != nil {
		return 0, err
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		settled int
		errs    []error
	)
	sem := make(chan struct{}, configReadConcurrency)
	for _, repo := range due {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			done, err := r.refresh(ctx, repo)
			mu.Lock()
			defer mu.Unlock()
			if done {
				settled++
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", repo.Repository, err))
			}
		}()
	}
	wg.Wait()
	return settled, errors.Join(errs...)
}

// refresh claims repo's pending read, reads the file and records what it
// read. It reports whether the read was settled.
func (r *ConfigRefresher) refresh(ctx context.Context, repo *core.PipelineRepo) (bool, error) {
	sha := repo.ConfigPending.SHA
	repo, err := r.store.ClaimPipelineConfigRead(ctx, repo.ID, repo.Version, sha, r.now().UTC().Add(configReadLease))
	if errors.Is(err, core.ErrPipelineVersionConflict) {
		// Another worker took it, or a newer push replaced it.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	read := r.read(ctx, repo, sha)
	for range casTries {
		switch {
		case read.retry:
			r.logger.InfoContext(ctx, "pipeline config not read; retried later, and the previous config stays in force",
				"repository", repo.Repository, "sha", sha, "attempt", repo.ConfigPending.Attempts, "reason", read.reason)
			_, err = r.store.DeferPipelineConfigRead(ctx, repo.ID, repo.Version, sha, read.reason,
				r.now().UTC().Add(configReadBackoff(repo.ConfigPending.Attempts)))
		case read.reason != "":
			r.logger.InfoContext(ctx, "pipeline config not recorded; the previous config stays in force",
				"repository", repo.Repository, "sha", sha, "reason", read.reason)
			_, err = r.store.SetPipelineRepoConfigError(ctx, repo.ID, repo.Version, sha, read.reason)
		default:
			var changes []core.PipelineConfigChange
			if changes, err = r.stageChanges(ctx, repo, sha, read.resolved); err != nil {
				return false, err
			}
			_, err = r.store.RecordPipelineRepoConfig(ctx, repo.ID, repo.Version, sha, read.raw, changes)
		}
		if !errors.Is(err, core.ErrPipelineVersionConflict) {
			return err == nil && !read.retry, err
		}
		if repo, err = r.store.GetPipelineRepo(ctx, repo.ID); err != nil || repo == nil || repo.ConfigPending.SHA != sha {
			// Gone, or a newer push's read replaced this one.
			return false, err
		}
	}
	return false, fmt.Errorf("pipeline repo %s kept changing: %w", repo.ID, core.ErrPipelineVersionConflict)
}

// configRead is what reading the file at a commit came to.
type configRead struct {
	resolved config.Resolved
	// raw is the file as JSON, the form config_json stores (JSON is YAML,
	// so config.Parse reads it back).
	raw []byte
	// reason is why it cannot be recorded; retry says the read reached no
	// answer and is tried again.
	reason string
	retry  bool
}

// read reads and validates the file at sha.
func (r *ConfigRefresher) read(ctx context.Context, repo *core.PipelineRepo, sha string) configRead {
	if r.contents == nil {
		return configRead{reason: "this server has no gate pool reader to read " + config.FilePath + " with", retry: true}
	}
	owner, name, _ := strings.Cut(repo.Repository, "/")
	data, err := r.contents.Read(ctx, ContentsRequest{
		UserID: repo.CreatedByUserID, OrgID: repo.OrgID, Owner: owner, Repo: name, Path: config.FilePath, Ref: sha,
	})
	switch {
	case errors.Is(err, ErrContentsNotFound):
		return configRead{reason: config.FilePath + " is missing at " + sha}
	case err != nil:
		return configRead{reason: "could not read " + config.FilePath + ": " + err.Error(), retry: true}
	}
	resolved, err := config.Load(data)
	if err != nil {
		return configRead{reason: config.FilePath + " is invalid: " + err.Error()}
	}
	raw, err := configJSON(data)
	if err != nil {
		return configRead{reason: config.FilePath + " cannot be stored: " + err.Error()}
	}
	return configRead{resolved: resolved, raw: raw}
}

// stageChanges is the setting changes recording resolved over the config in
// force carries (config.StageChanges), each attributed to the author of the
// PR the commit merged, else to whoever pushed it.
func (r *ConfigRefresher) stageChanges(ctx context.Context, repo *core.PipelineRepo, sha string, resolved config.Resolved) ([]core.PipelineConfigChange, error) {
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
		ChangedByProvider: repo.Provider, ChangedByProviderUserID: repo.ConfigPending.SenderID, ChangedByLogin: repo.ConfigPending.SenderLogin,
	}
	merged, err := r.store.GetPRRecordByMergeCommit(ctx, repo.Provider, repo.RepositoryID, sha)
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
