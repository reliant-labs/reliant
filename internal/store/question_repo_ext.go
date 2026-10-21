// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Question. The generated question_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: questionRepo (a *crud.Repo[Question]) and the
//     package-level GetQuestionByID / ListQuestion / CreateQuestion / UpdateQuestion / DeleteQuestion
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Question struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindQuestionBy(ctx context.Context, db orm.Context, /* args */) ([]*Question, error) {
//		var rows []*Question
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("chat_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveQuestions(ctx context.Context, db orm.Context) ([]*Question, error) {
//		return ListQuestion(ctx, db, orm.WhereEq("chat_id", true))
//	}
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/crud"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The questions queries (P5b L1b). A question is a gate a workflow step
// raised; status 1 is pending, 2 resolved (core.QuestionStatus*).

// InsertQuestion stores a new question. questions has no updated_at, so the
// generated Create writes every caller-set column as given.
func InsertQuestion(ctx context.Context, db orm.Context, q *core.Question) error {
	if err := CreateQuestion(ctx, db, questionToRow(q)); err != nil {
		return fmt.Errorf("failed to create question: %w", err)
	}
	return nil
}

// GetQuestion returns the question, or nil with no error when there is none.
func GetQuestion(ctx context.Context, db orm.Context, id string) (*core.Question, error) {
	row, err := GetQuestionByID(ctx, db, id)
	if chatIsMissing(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get question: %w", err)
	}
	return questionFromRow(row), nil
}

// GetPendingQuestionByChatID returns the chat's newest pending question from
// any of its threads, or nil when none is pending.
func GetPendingQuestionByChatID(ctx context.Context, db orm.Context, chatID string) (*core.Question, error) {
	rows, err := ListQuestion(ctx, db,
		orm.WhereEq(QuestionFieldChatID, chatID),
		orm.WhereEq(QuestionFieldStatus, core.QuestionStatusPending),
		orm.WithOrderBy(QuestionFieldCreatedAt, orm.Desc),
		orm.WithLimit(1))
	if err != nil {
		return nil, fmt.Errorf("failed to get pending question: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return questionFromRow(rows[0]), nil
}

// ListQuestionsByWorkflowStepIteration returns the questions one step raised
// in one loop iteration of a workflow. A step raises a handful at most, well
// under the generated List's default cap.
func ListQuestionsByWorkflowStepIteration(ctx context.Context, db orm.Context, workflowID, stepID string, iteration int) ([]*core.Question, error) {
	rows, err := ListQuestion(ctx, db,
		orm.WhereEq(QuestionFieldWorkflowID, workflowID),
		orm.WhereEq(QuestionFieldStepID, stepID),
		orm.WhereEq(QuestionFieldLoopIteration, iteration))
	if err != nil {
		return nil, fmt.Errorf("failed to get questions by workflow step iteration: %w", err)
	}
	return questionsFromRows(rows), nil
}

// ListQuestionsByChat returns every question raised in a chat, in creation
// order: the gate history run analysis reconstructs. Read through the
// builder rather than the generated List, which caps a limit-less list at
// its default page.
func ListQuestionsByChat(ctx context.Context, db orm.Context, chatID string) ([]*core.Question, error) {
	var rows []*Question
	err := db.Bun().NewSelect().Model(&rows).
		Where("chat_id = ?", chatID).
		OrderExpr("created_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list questions by chat: %w", err)
	}
	return questionsFromRows(rows), nil
}

// ListPendingQuestionsByChat returns a chat's pending questions, oldest
// first.
func ListPendingQuestionsByChat(ctx context.Context, db orm.Context, chatID string) ([]*core.Question, error) {
	rows, err := ListQuestion(ctx, db,
		orm.WhereEq(QuestionFieldChatID, chatID),
		orm.WhereEq(QuestionFieldStatus, core.QuestionStatusPending),
		orm.WithOrderBy(QuestionFieldCreatedAt, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list pending questions by chat: %w", err)
	}
	return questionsFromRows(rows), nil
}

// ResolveQuestion marks the question answered with responseData at
// resolvedAt. Resolving one that does not exist is a no-op.
func ResolveQuestion(ctx context.Context, db orm.Context, id string, responseData *string, resolvedAt time.Time) error {
	status := int32(core.QuestionStatusResolved)
	row := &Question{ID: id, Status: &status, ResponseData: responseData, ResolvedAt: &resolvedAt}
	err := questionRepo.UpdateMasked(ctx, db, row,
		[]string{QuestionFieldStatus, QuestionFieldResponseData, QuestionFieldResolvedAt}, crud.NoReadBack())
	if err != nil && !chatIsMissing(err) {
		return fmt.Errorf("failed to resolve question: %w", err)
	}
	return nil
}

func questionToRow(q *core.Question) *Question {
	status := int32(q.Status)
	row := &Question{
		ID:                 q.ID,
		ChatID:             q.ChatID,
		WorkflowID:         q.WorkflowID,
		TemporalWorkflowID: q.TemporalWorkflowID,
		ThreadID:           q.ThreadID,
		StepID:             q.StepID,
		LoopNodeID:         q.LoopNodeID,
		Status:             &status,
		Metadata:           q.Metadata,
		ResponseData:       q.ResponseData,
		CreatedAt:          q.CreatedAt,
		ResolvedAt:         q.ResolvedAt,
		ToolCallID:         q.ToolCallID,
	}
	if q.LoopIteration != nil {
		it := int64(*q.LoopIteration)
		row.LoopIteration = &it
	}
	return row
}

func questionFromRow(row *Question) *core.Question {
	q := &core.Question{
		ID:                 row.ID,
		ChatID:             row.ChatID,
		WorkflowID:         row.WorkflowID,
		TemporalWorkflowID: row.TemporalWorkflowID,
		ThreadID:           row.ThreadID,
		StepID:             row.StepID,
		LoopNodeID:         row.LoopNodeID,
		Metadata:           row.Metadata,
		ResponseData:       row.ResponseData,
		CreatedAt:          row.CreatedAt,
		ResolvedAt:         row.ResolvedAt,
		ToolCallID:         row.ToolCallID,
	}
	if row.Status != nil {
		q.Status = int(*row.Status)
	}
	if row.LoopIteration != nil {
		it := int(*row.LoopIteration)
		q.LoopIteration = &it
	}
	return q
}

func questionsFromRows(rows []*Question) []*core.Question {
	out := make([]*core.Question, len(rows))
	for i, row := range rows {
		out[i] = questionFromRow(row)
	}
	return out
}

// QuestionExtra carries fields on Question that are NOT columns — computed
// values, transient state, anything derived rather than stored. Question is
// embedded on QuestionExtra (see question_orm.go), so a field added here is a field on
// every Question value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type QuestionExtra struct {
}
