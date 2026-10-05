// Copyright (c) 2025 Reliant Labs
//
// Package videojobs persists one record per generate_video tool call, written
// BEFORE the first poll. A render takes minutes and is billed on completion, so
// a worker that dies mid-render must be able to find the provider job again
// rather than submit (and pay for) a second one. The same record maps a
// finished clip's attachment id back to the provider interaction that produced
// it, which is what conversational edit needs.
//
//forge:exclude-contract: SQL-backed record store; its one consumer (the generate_video tool) declares the narrow interface it needs
package videojobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// State is where a job is in its life.
type State string

const (
	// StateSubmitted: the provider accepted the job; the outcome is unknown.
	StateSubmitted State = "submitted"
	// StateCompleted: the clip was fetched and stored as AttachmentID.
	StateCompleted State = "completed"
	// StateFailed: the provider reported failure or filtered the clip.
	StateFailed State = "failed"
	// StateCancelled: the user interrupted the call.
	StateCancelled State = "cancelled"
)

// Job is one generate_video call.
type Job struct {
	ToolCallID string
	UserID     string
	ChatID     string
	Driver     string
	ModelID    string
	APIModel   string
	// ProviderJob is the Veo operation name or Omni interaction id.
	ProviderJob  string
	State        State
	AttachmentID string
	ErrorMessage string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// DBTX is the subset of database/sql the store needs.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SQLStore is the Postgres-backed store.
type SQLStore struct{ db DBTX }

// NewSQLStore returns a store over db.
func NewSQLStore(db DBTX) *SQLStore { return &SQLStore{db: db} }

const jobColumns = `tool_call_id, user_id, chat_id, driver, model_id, api_model, provider_job,
	state, COALESCE(attachment_id, ''), error_message, created_at, updated_at`

func scanJob(row *sql.Row) (*Job, error) {
	var job Job
	var state string
	err := row.Scan(&job.ToolCallID, &job.UserID, &job.ChatID, &job.Driver, &job.ModelID, &job.APIModel,
		&job.ProviderJob, &state, &job.AttachmentID, &job.ErrorMessage, &job.CreatedAt, &job.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job.State = State(state)
	return &job, nil
}

// GetByToolCall returns the job for a tool call, or (nil, nil) if none.
func (s *SQLStore) GetByToolCall(ctx context.Context, toolCallID string) (*Job, error) {
	return scanJob(s.db.QueryRowContext(ctx,
		`SELECT `+jobColumns+` FROM video_generation_jobs WHERE tool_call_id = $1`, toolCallID))
}

// GetByAttachment returns the job that produced an attachment, or (nil, nil).
func (s *SQLStore) GetByAttachment(ctx context.Context, attachmentID string) (*Job, error) {
	return scanJob(s.db.QueryRowContext(ctx,
		`SELECT `+jobColumns+` FROM video_generation_jobs WHERE attachment_id = $1 LIMIT 1`, attachmentID))
}

// Create records a freshly submitted job. A second Create for the same tool
// call is an error: the caller must have looked it up first.
func (s *SQLStore) Create(ctx context.Context, job *Job) error {
	now := time.Now().UTC()
	if job.State == "" {
		job.State = StateSubmitted
	}
	job.CreatedAt, job.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO video_generation_jobs
			(tool_call_id, user_id, chat_id, driver, model_id, api_model, provider_job, state, error_message, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`,
		job.ToolCallID, job.UserID, job.ChatID, job.Driver, job.ModelID, job.APIModel, job.ProviderJob,
		string(job.State), job.ErrorMessage, now)
	if err != nil {
		return fmt.Errorf("record video job: %w", err)
	}
	return nil
}

// Complete marks the job finished and links the stored clip.
func (s *SQLStore) Complete(ctx context.Context, toolCallID, attachmentID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE video_generation_jobs SET state = $2, attachment_id = $3, updated_at = $4
		WHERE tool_call_id = $1`, toolCallID, string(StateCompleted), attachmentID, time.Now().UTC())
	return err
}

// Finish moves the job to a terminal non-success state with a reason.
func (s *SQLStore) Finish(ctx context.Context, toolCallID string, state State, message string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE video_generation_jobs SET state = $2, error_message = $3, updated_at = $4
		WHERE tool_call_id = $1`, toolCallID, string(state), message, time.Now().UTC())
	return err
}
