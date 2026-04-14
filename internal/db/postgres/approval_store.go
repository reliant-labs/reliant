package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

type approvalStore struct{ q pgdb.Querier }

// NewApprovalStore creates the Postgres approval store implementation.
func NewApprovalStore(q pgdb.Querier) core.ApprovalStore { return &approvalStore{q: q} }

func (s *approvalStore) CreateApproval(ctx context.Context, approval *core.Approval) error {
	return s.q.CreateApproval(ctx, pgdb.CreateApprovalParams{
		ID:                 approval.ID,
		ChatID:             approval.ChatID,
		ApprovalType:       approval.ApprovalType,
		EntityID:           approval.EntityID,
		Status:             approval.Status,
		DenialReason:       approvalPtrToNullString(approval.DenialReason),
		Title:              approval.Title,
		Metadata:           approvalPtrToNullString(approval.Metadata),
		TemporalWorkflowID: approval.TemporalWorkflowID,
		CreatedAt:          approval.CreatedAt,
		ResolvedAt:         approvalPtrToNullTime(approval.ResolvedAt),
	})
}

func (s *approvalStore) GetApproval(ctx context.Context, id string) (*core.Approval, error) {
	row, err := s.q.GetApproval(ctx, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return approvalFromPG(row), nil
}

func (s *approvalStore) GetApprovalByEntityID(ctx context.Context, entityID string) (*core.Approval, error) {
	row, err := s.q.GetApprovalByEntityID(ctx, entityID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return approvalFromPG(row), nil
}

func (s *approvalStore) ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*core.Approval, error) {
	rows, err := s.q.ListPendingApprovalsByChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	return approvalsFromPG(rows), nil
}

func (s *approvalStore) ListApprovalsByChat(ctx context.Context, chatID string) ([]*core.Approval, error) {
	rows, err := s.q.ListApprovalsByChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	return approvalsFromPG(rows), nil
}

func (s *approvalStore) UpdateApprovalStatus(ctx context.Context, id string, status int32, denialReason *string, actionTaken *string, metadata *string, resolvedAt *time.Time) error {
	return s.q.UpdateApprovalStatus(ctx, pgdb.UpdateApprovalStatusParams{
		ID:           id,
		Status:       status,
		DenialReason: approvalPtrToNullString(denialReason),
		ActionTaken:  approvalPtrToNullString(actionTaken),
		ResolvedAt:   approvalPtrToNullTime(resolvedAt),
		Metadata:     approvalPtrToNullString(metadata),
	})
}

func approvalFromPG(row pgdb.Approval) *core.Approval {
	return &core.Approval{
		ID:                 row.ID,
		ChatID:             row.ChatID,
		ApprovalType:       row.ApprovalType,
		EntityID:           row.EntityID,
		Status:             row.Status,
		DenialReason:       approvalNullStringToPtr(row.DenialReason),
		ActionTaken:        approvalNullStringToPtr(row.ActionTaken),
		Title:              row.Title,
		Metadata:           approvalNullStringToPtr(row.Metadata),
		TemporalWorkflowID: row.TemporalWorkflowID,
		CreatedAt:          row.CreatedAt,
		ResolvedAt:         approvalNullTimeToPtr(row.ResolvedAt),
	}
}

func approvalsFromPG(rows []pgdb.Approval) []*core.Approval {
	result := make([]*core.Approval, len(rows))
	for i, row := range rows {
		result[i] = approvalFromPG(row)
	}
	return result
}

func approvalPtrToNullString(s *string) sql.NullString {
	if s != nil {
		return sql.NullString{String: *s, Valid: true}
	}
	return sql.NullString{Valid: false}
}

func approvalNullStringToPtr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}

func approvalPtrToNullTime(t *time.Time) sql.NullTime {
	if t != nil {
		return sql.NullTime{Time: *t, Valid: true}
	}
	return sql.NullTime{Valid: false}
}

func approvalNullTimeToPtr(nt sql.NullTime) *time.Time {
	if nt.Valid {
		return &nt.Time
	}
	return nil
}
