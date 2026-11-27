package pipeline

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func ts(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func tsp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func repoProto(r *core.PipelineRepo) *pb.PipelineRepository {
	return &pb.PipelineRepository{
		Id: r.ID, OrgId: r.OrgID, ProjectId: r.ProjectID, Provider: r.Provider, AccountKey: r.AccountKey,
		RepositoryId: r.RepositoryID, Repository: r.Repository, BaseBranch: r.BaseBranch, Mode: string(r.Mode),
		ConfigSha: r.ConfigSHA, QueueState: string(r.QueueState), CreatedByUserId: r.CreatedByUserID,
		CreatedAt: ts(r.CreatedAt), UpdatedAt: ts(r.UpdatedAt), Version: r.Version,
	}
}

func batchProto(b *core.QueueBatch) *pb.PipelineQueueBatch {
	if b == nil {
		return nil
	}
	return &pb.PipelineQueueBatch{
		Id: b.ID, BaseSha: b.BaseSHA, CandidateSha: b.CandidateSHA, Kind: string(b.Kind), State: string(b.State),
		Attempt: int32(b.Attempt), IsolationBackend: string(b.IsolationBackend), GateRunner: string(b.GateRunner),
		LandedSha: b.LandedSHA, StartedAt: ts(b.StartedAt), FinishedAt: tsp(b.FinishedAt),
	}
}

func entryProto(e *core.QueueEntryListing) *pb.PipelineQueueEntry {
	return &pb.PipelineQueueEntry{
		Id: e.ID, RecordId: e.RecordID, Number: int32(e.Number), HeadSha: e.HeadSHA, Priority: int32(e.Priority),
		EnqueuedAt: ts(e.EnqueuedAt), BatchId: e.BatchID, State: string(e.State),
	}
}

func mainHealthProto(h *core.MainHealthRecord) *pb.PipelineMainHealth {
	if h == nil {
		return nil
	}
	return &pb.PipelineMainHealth{
		Sha: h.SHA, Source: string(h.Source), Verdict: string(h.Verdict), Signature: h.Signature,
		BatchId: h.BatchID, ObservedAt: ts(h.ObservedAt),
	}
}
