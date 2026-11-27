package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// Fixtures shared by the pipeline store tests (pipeline_*_repo_ext_test.go).

const pipelineOrg = "org-1"

func pipelineEnrolment(repositoryID string) core.PipelineRepoEnrolment {
	return core.PipelineRepoEnrolment{
		OrgID: pipelineOrg, ProjectID: storetest.SeedProjectID, Provider: "github", AccountKey: "inst-1",
		RepositoryID: repositoryID, Repository: "acme/" + repositoryID, CreatedByUserID: storetest.SeedUserID,
	}
}

// pipelineRepo enrols a repository in mode.
func pipelineRepo(t *testing.T, env *storetest.Env, repositoryID string, mode core.PipelineRepoMode) *core.PipelineRepo {
	t.Helper()
	ctx := context.Background()
	r, err := store.UpsertPipelineRepo(ctx, env.ORM, pipelineEnrolment(repositoryID))
	if err != nil {
		t.Fatalf("enrol %s: %v", repositoryID, err)
	}
	if mode != r.Mode {
		if r, err = store.SetPipelineRepoMode(ctx, env.ORM, r.ID, r.Version, mode); err != nil {
			t.Fatalf("mode %s: %v", repositoryID, err)
		}
	}
	return r
}

// pipelineRecord inserts a pr_records row of repo's repository at head.
func pipelineRecord(t *testing.T, env *storetest.Env, repo *core.PipelineRepo, id string, number int, head string) {
	t.Helper()
	if _, err := env.SQL.Exec(`INSERT INTO pr_records (id, provider, account_key, repository_id, repository, number, state,
		head_sha, snapshot_at, observed_at) VALUES ($1, 'github', $2, $3, $4, $5, 'open', $6, now(), now())`,
		id, repo.AccountKey, repo.RepositoryID, repo.Repository, number, head); err != nil {
		t.Fatalf("insert pr record %s: %v", id, err)
	}
}

// pipelineDecision inserts a decision a person made on the web: approved
// unless state says otherwise.
func pipelineDecision(t *testing.T, env *storetest.Env, id string, subject core.PipelineDecisionSubjectKind, subjectID, head string,
	kind core.PipelineDecisionKind, state core.PipelineDecisionState, expires *time.Time) {
	t.Helper()
	var headArg, revokedAt any
	if head != "" {
		headArg = head
	}
	if state == core.PipelineDecisionRevoked {
		revokedAt = time.Now()
	}
	if _, err := env.SQL.Exec(`INSERT INTO pipeline_decisions (id, org_id, project_id, subject_kind, subject_id, head_sha,
		kind, state, reason, decided_by_user_id, decided_via, decided_at, expires_at, revoked_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'because', $9, 'web', now(), $10, $11)`,
		id, pipelineOrg, storetest.SeedProjectID, subject, subjectID, headArg, kind, state, storetest.SeedUserID, expires, revokedAt); err != nil {
		t.Fatalf("insert decision %s: %v", id, err)
	}
}

func TestPipelineRepoEnrolmentAndCAS(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()

	missing, err := store.GetPipelineRepo(ctx, env.ORM, "none")
	if err != nil || missing != nil {
		t.Fatalf("missing repo: %v %v", missing, err)
	}

	r, err := store.UpsertPipelineRepo(ctx, env.ORM, pipelineEnrolment("rid-1"))
	if err != nil {
		t.Fatal(err)
	}
	if r.ID == "" || r.Mode != core.PipelineRepoModeOff || r.QueueState != core.PipelineQueueRunning ||
		r.BaseBranch != "main" || r.Version != 0 || r.ConfigSHA != "" || r.ConfigJSON != nil {
		t.Fatalf("new enrolment: %+v", r)
	}
	byRepo, err := store.GetPipelineRepoByRepository(ctx, env.ORM, "github", "rid-1")
	if err != nil || byRepo == nil || byRepo.ID != r.ID {
		t.Fatalf("by repository: %+v %v", byRepo, err)
	}
	enrolled, err := store.ListEnrolledPipelineRepos(ctx, env.ORM)
	if err != nil || len(enrolled) != 0 {
		t.Fatalf("a repository in mode off is not listed: %+v %v", enrolled, err)
	}

	shadow, err := store.SetPipelineRepoMode(ctx, env.ORM, r.ID, r.Version, core.PipelineRepoModeShadow)
	if err != nil || shadow.Mode != core.PipelineRepoModeShadow || shadow.Version != 1 {
		t.Fatalf("set mode: %+v %v", shadow, err)
	}
	if _, err := store.SetPipelineRepoMode(ctx, env.ORM, r.ID, r.Version, core.PipelineRepoModeOn); !errors.Is(err, core.ErrPipelineVersionConflict) {
		t.Fatalf("a stale version must conflict, got %v", err)
	}
	if _, err := store.SetPipelineRepoMode(ctx, env.ORM, r.ID, shadow.Version, "loud"); err == nil {
		t.Fatal("an unknown mode is refused")
	}
	enrolled, err = store.ListEnrolledPipelineRepos(ctx, env.ORM)
	if err != nil || len(enrolled) != 1 || enrolled[0].ID != r.ID {
		t.Fatalf("a shadow repository is listed: %+v %v", enrolled, err)
	}

	// Re-enrolment by the same project refreshes the names and keeps the
	// mode; another org's (or project's) is refused and changes nothing.
	again := pipelineEnrolment("rid-1")
	again.Repository, again.BaseBranch = "acme/renamed", "trunk"
	re, err := store.UpsertPipelineRepo(ctx, env.ORM, again)
	if err != nil || re.ID != r.ID || re.Repository != "acme/renamed" || re.BaseBranch != "trunk" ||
		re.Mode != core.PipelineRepoModeShadow || re.Version != 2 {
		t.Fatalf("re-enrolment: %+v %v", re, err)
	}
	other := pipelineEnrolment("rid-1")
	other.OrgID = "org-2"
	if _, err := store.UpsertPipelineRepo(ctx, env.ORM, other); !errors.Is(err, core.ErrPipelineRepoEnrolledElsewhere) {
		t.Fatalf("another org's enrolment must be refused, got %v", err)
	}

	cfg := json.RawMessage(`{"version": 1}`)
	withCfg, err := store.SetPipelineRepoConfig(ctx, env.ORM, r.ID, re.Version, "cfgsha", cfg)
	if err != nil || withCfg.ConfigSHA != "cfgsha" || withCfg.Version != 3 {
		t.Fatalf("set config: %+v %v", withCfg, err)
	}
	var got map[string]int
	if err := json.Unmarshal(withCfg.ConfigJSON, &got); err != nil || got["version"] != 1 {
		t.Fatalf("config json %s: %v", withCfg.ConfigJSON, err)
	}
	if _, err := store.SetPipelineRepoConfig(ctx, env.ORM, r.ID, re.Version, "other", cfg); !errors.Is(err, core.ErrPipelineVersionConflict) {
		t.Fatalf("a stale config write must conflict, got %v", err)
	}

	pipelineDecision(t, env, "dec-pause", core.PipelineDecisionSubjectRepo, r.ID, "", core.PipelineDecisionPauseQueue, core.PipelineDecisionApproved, nil)
	paused, err := store.SetPipelineRepoQueueState(ctx, env.ORM, r.ID, withCfg.Version, core.PipelineQueuePaused, "dec-pause")
	if err != nil || paused.QueueState != core.PipelineQueuePaused || paused.QueuePausedByDecisionID != "dec-pause" {
		t.Fatalf("pause: %+v %v", paused, err)
	}
	running, err := store.SetPipelineRepoQueueState(ctx, env.ORM, r.ID, paused.Version, core.PipelineQueueRunning, "dec-pause")
	if err != nil || running.QueueState != core.PipelineQueueRunning || running.QueuePausedByDecisionID != "" {
		t.Fatalf("resume clears the pausing decision: %+v %v", running, err)
	}
	if _, err := store.SetPipelineRepoQueueState(ctx, env.ORM, r.ID, paused.Version, core.PipelineQueueDegraded, ""); !errors.Is(err, core.ErrPipelineVersionConflict) {
		t.Fatalf("a stale queue state write must conflict, got %v", err)
	}
}

// newPipelineRecords enrols a shadow repository and n PR records on it, at
// head "head-<i>".
func newPipelineRecords(t *testing.T, env *storetest.Env, repositoryID string, n int) (*core.PipelineRepo, []string) {
	t.Helper()
	repo := pipelineRepo(t, env, repositoryID, core.PipelineRepoModeShadow)
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-rec-%d", repositoryID, i)
		pipelineRecord(t, env, repo, ids[i], i+1, fmt.Sprintf("head-%d", i))
	}
	return repo, ids
}
