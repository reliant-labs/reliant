package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	sqlitedb "github.com/reliant-labs/reliant/internal/db/sqlite/generated"
)

type approvalStore struct{ q sqlitedb.Querier }

// NewApprovalStore creates the SQLite approval store implementation.
func NewApprovalStore(q sqlitedb.Querier) core.ApprovalStore { return &approvalStore{q: q} }

func (s *approvalStore) CreateApproval(ctx context.Context, approval *core.Approval) error {
	return s.q.CreateApproval(ctx, sqlitedb.CreateApprovalParams{
		ID:                 approval.ID,
		ChatID:             approval.ChatID,
		ApprovalType:       int64(approval.ApprovalType),
		EntityID:           approval.EntityID,
		Status:             int64(approval.Status),
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
	return approvalFromSQLc(row), nil
}

func (s *approvalStore) GetApprovalByEntityID(ctx context.Context, entityID string) (*core.Approval, error) {
	row, err := s.q.GetApprovalByEntityID(ctx, entityID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return approvalFromSQLc(row), nil
}

func (s *approvalStore) ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*core.Approval, error) {
	rows, err := s.q.ListPendingApprovalsByChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	return approvalsFromSQLc(rows), nil
}

func (s *approvalStore) ListApprovalsByChat(ctx context.Context, chatID string) ([]*core.Approval, error) {
	rows, err := s.q.ListApprovalsByChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	return approvalsFromSQLc(rows), nil
}

func (s *approvalStore) UpdateApprovalStatus(ctx context.Context, id string, status int32, denialReason *string, actionTaken *string, metadata *string, resolvedAt *time.Time) error {
	return s.q.UpdateApprovalStatus(ctx, sqlitedb.UpdateApprovalStatusParams{
		ID:           id,
		Status:       int64(status),
		DenialReason: approvalPtrToNullString(denialReason),
		ActionTaken:  approvalPtrToNullString(actionTaken),
		ResolvedAt:   approvalPtrToNullTime(resolvedAt),
		Metadata:     approvalPtrToNullString(metadata),
	})
}

func approvalFromSQLc(sa sqlitedb.Approval) *core.Approval {
	return &core.Approval{
		ID:                 sa.ID,
		ChatID:             sa.ChatID,
		ApprovalType:       int32(sa.ApprovalType),
		EntityID:           sa.EntityID,
		Status:             int32(sa.Status),
		DenialReason:       approvalNullStringToPtr(sa.DenialReason),
		ActionTaken:        approvalNullStringToPtr(sa.ActionTaken),
		Title:              sa.Title,
		Metadata:           approvalNullStringToPtr(sa.Metadata),
		TemporalWorkflowID: sa.TemporalWorkflowID,
		CreatedAt:          sa.CreatedAt,
		ResolvedAt:         approvalNullTimeToPtr(sa.ResolvedAt),
	}
}

func approvalsFromSQLc(rows []sqlitedb.Approval) []*core.Approval {
	approvals := make([]*core.Approval, len(rows))
	for i, row := range rows {
		approvals[i] = approvalFromSQLc(row)
	}
	return approvals
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
