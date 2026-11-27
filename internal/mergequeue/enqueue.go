// Copyright (c) 2025 Reliant Labs
package mergequeue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/serveraction"
)

// EnqueueAction is the queue.enqueue server action's name. Its Spec is in
// serveraction's builtin catalog.
const EnqueueAction = serveraction.QueueEnqueue

// EnqueueHandler implements queue.enqueue@1: `with: {record_id}`. It
// recomputes admission on the server (§3.1) from DB state and the
// repository's config, whatever the calling run believes, and puts the PR's
// current head in the queue. It is idempotent: a head already queued is
// queued. It answers {queued, reason, code, entry_id, head}.
type EnqueueHandler struct {
	Store    Store
	Configs  Configs
	Workdirs Workdirs
	Now      func() time.Time
}

// Handle implements serveraction.Handler.
func (h EnqueueHandler) Handle(ctx context.Context, req *serveraction.Request) (*serveraction.Result, error) {
	recordID, _ := req.Params["record_id"].(string)
	if recordID == "" {
		return nil, serveraction.Errorf(serveraction.CodeInvalidArgument, "record_id is required")
	}
	now := h.Now
	if now == nil {
		now = time.Now
	}
	configs := h.Configs
	if configs == nil {
		configs = StoredConfig{}
	}
	p, err := h.Store.GetPRPipeline(ctx, recordID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return refused(Refusal{Code: RefuseNotEnrolled, Reason: "the PR's repository is not enrolled in the pipeline"}, ""), nil
	}
	if req.Principal.ProjectID == "" || req.Principal.ProjectID != p.ProjectID {
		return nil, serveraction.Errorf(serveraction.CodePermissionDenied, "the run's project does not own this PR's repository")
	}
	repo, err := h.Store.GetPipelineRepo(ctx, p.RepoID)
	if err != nil {
		return nil, err
	}
	if repo == nil {
		return refused(Refusal{Code: RefuseNotEnrolled, Reason: "the PR's repository is not enrolled in the pipeline"}, ""), nil
	}
	if repo.Mode == core.PipelineRepoModeOff {
		return refused(Refusal{Code: RefuseModeOff, Reason: "the repository's pipeline is off"}, ""), nil
	}
	rec, err := h.Store.GetQueuePRRecord(ctx, recordID)
	if err != nil {
		return nil, err
	}
	head := ""
	if rec != nil {
		head = rec.HeadSHA
	}
	if want, _ := req.Params["head"].(string); want != "" && want != head {
		return refused(Refusal{Code: RefuseHeadMoved, Reason: fmt.Sprintf("the PR's head is %s, not %s", head, want)}, head), nil
	}
	cfg, err := configs.Resolved(ctx, repo)
	if err != nil {
		return nil, serveraction.Errorf(serveraction.CodeFailedPrecondition, "the repository's pipeline.yaml does not resolve: %v", err)
	}
	adm := admission{store: h.Store, now: now}
	refusal, err := adm.admit(ctx, repo, cfg, rec, head, func(ctx context.Context) ([]string, error) {
		if h.Workdirs == nil {
			return nil, errors.New("no workdir to diff the head in")
		}
		wd, err := h.Workdirs.Open(ctx, repo)
		if err != nil {
			return nil, err
		}
		gh, err := ghport.ParseRepo(repo.Repository)
		if err != nil {
			return nil, err
		}
		return changedPaths(ctx, wd, gh, repo.BaseBranch, head)
	})
	if err != nil {
		return nil, err
	}
	if !refusal.Admissible() {
		return refused(refusal, head), nil
	}
	entry, created, err := h.Store.EnqueueQueueEntry(ctx, core.QueueEntryNew{
		OrgID: repo.OrgID, ProjectID: repo.ProjectID, RepoID: repo.ID, RecordID: recordID, HeadSHA: head, Priority: p.Priority,
	})
	if errors.Is(err, core.ErrQueueEntryOtherHead) {
		// The queue ejects the old entry (head_moved) on its next pass and
		// wakes the PR, which enqueues this head then.
		return refused(Refusal{Code: RefuseOtherHeadInQ, Reason: "the PR is queued at an earlier head; the queue ejects it on its next pass"}, head), nil
	}
	if err != nil {
		return nil, err
	}
	content := "queued " + head
	if !created {
		content = "already queued at " + head
	}
	return &serveraction.Result{
		Data:    map[string]any{"queued": true, "reason": "", "code": "", "entry_id": entry.ID, "head": head},
		Content: content,
	}, nil
}

func refused(r Refusal, head string) *serveraction.Result {
	return &serveraction.Result{
		Data:    map[string]any{"queued": false, "reason": r.Reason, "code": r.Code, "entry_id": "", "head": head},
		Content: "not queued: " + r.Reason,
	}
}
