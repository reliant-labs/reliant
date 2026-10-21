// Copyright (c) 2025 Reliant Labs
//
// Package videojobs persists one record per generate_video tool call, written
// BEFORE the first poll. A render takes minutes and is billed on completion, so
// a worker that dies mid-render must be able to find the provider job again
// rather than submit (and pay for) a second one. The same record maps a
// finished clip's attachment id back to the provider interaction that produced
// it, which is what conversational edit needs.
//
//forge:exclude-contract: record store over the generated video_generation_jobs store; its one consumer (the generate_video tool) declares the narrow interface it needs
package videojobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/store"
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

// SQLStore is the Postgres-backed store: video_generation_jobs through the
// generated internal/store delegates, keyed by tool_call_id.
type SQLStore struct{ db orm.Context }

// NewSQLStore returns a store over db (the Repository's ORM client, so a call
// inside a Repository transaction joins it).
func NewSQLStore(db orm.Context) *SQLStore { return &SQLStore{db: db} }

// GetByToolCall returns the job for a tool call, or (nil, nil) if none.
func (s *SQLStore) GetByToolCall(ctx context.Context, toolCallID string) (*Job, error) {
	row, err := store.GetVideoGenerationJobByID(ctx, s.db, toolCallID)
	if errors.Is(err, svcerr.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return jobFromRow(row), nil
}

// GetByAttachment returns the job that produced an attachment, or (nil, nil).
func (s *SQLStore) GetByAttachment(ctx context.Context, attachmentID string) (*Job, error) {
	rows, err := store.ListVideoGenerationJob(ctx, s.db,
		orm.WhereEq(store.VideoGenerationJobFieldAttachmentID, attachmentID), orm.WithLimit(1))
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return jobFromRow(rows[0]), nil
}

// Create records a freshly submitted job; created_at and updated_at are the
// time of the write, set on job too. A second Create for the same tool call
// is an error: the caller must have looked it up first.
func (s *SQLStore) Create(ctx context.Context, job *Job) error {
	if job.State == "" {
		job.State = StateSubmitted
	}
	row := &store.VideoGenerationJob{
		ToolCallID:   job.ToolCallID,
		UserID:       job.UserID,
		ChatID:       job.ChatID,
		Driver:       job.Driver,
		ModelID:      job.ModelID,
		APIModel:     job.APIModel,
		ProviderJob:  job.ProviderJob,
		State:        string(job.State),
		ErrorMessage: job.ErrorMessage,
	}
	if err := store.CreateVideoGenerationJob(ctx, s.db, row); err != nil {
		return fmt.Errorf("record video job: %w", err)
	}
	job.CreatedAt, job.UpdatedAt = row.CreatedAt, row.UpdatedAt
	return nil
}

// Complete marks the job finished and links the stored clip.
func (s *SQLStore) Complete(ctx context.Context, toolCallID, attachmentID string) error {
	return s.update(ctx, &store.VideoGenerationJob{ToolCallID: toolCallID, State: string(StateCompleted), AttachmentID: &attachmentID},
		store.VideoGenerationJobFieldState, store.VideoGenerationJobFieldAttachmentID)
}

// Finish moves the job to a terminal non-success state with a reason.
func (s *SQLStore) Finish(ctx context.Context, toolCallID string, state State, message string) error {
	return s.update(ctx, &store.VideoGenerationJob{ToolCallID: toolCallID, State: string(state), ErrorMessage: message},
		store.VideoGenerationJobFieldState, store.VideoGenerationJobFieldErrorMessage)
}

// update writes fields (and updated_at). A job that is not there is not an
// error: there is nothing left to mark.
func (s *SQLStore) update(ctx context.Context, row *store.VideoGenerationJob, fields ...string) error {
	if err := store.UpdateVideoGenerationJobMasked(ctx, s.db, row, fields); err != nil && !errors.Is(err, orm.ErrNoRows) {
		return err
	}
	return nil
}

func jobFromRow(row *store.VideoGenerationJob) *Job {
	job := &Job{
		ToolCallID:   row.ToolCallID,
		UserID:       row.UserID,
		ChatID:       row.ChatID,
		Driver:       row.Driver,
		ModelID:      row.ModelID,
		APIModel:     row.APIModel,
		ProviderJob:  row.ProviderJob,
		State:        State(row.State),
		ErrorMessage: row.ErrorMessage,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
	if row.AttachmentID != nil {
		job.AttachmentID = *row.AttachmentID
	}
	return job
}
