// Copyright (c) 2025 Reliant Labs
package videogen

import (
	"time"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Image is an input image, already loaded into memory.
type Image struct {
	Bytes    []byte
	MIMEType string
}

// Request is one video to generate. Zero values mean "provider default".
type Request struct {
	Prompt          string
	DurationSeconds int
	Resolution      string
	AspectRatio     string
	StartFrame      *Image
	EndFrame        *Image
	References      []Image
	NegativePrompt  string
	// Audio is nil for the provider default. Only Vertex Veo can turn it off.
	Audio *bool
	// EditOf is the provider job (Job.ID) of the clip to refine, for models
	// that support conversational edit.
	EditOf string
}

// Job names one accepted provider operation: a Veo operation name or an Omni
// interaction id. It is the value a caller persists between Submit and Poll.
type Job struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

// Response is a finished clip.
type Response struct {
	Bytes    []byte
	MIMEType string
	ModelID  string
	APIModel string
	Driver   string
	Job      Job
}

// ModelInfo identifies the model a client is bound to and carries its declared
// capabilities, which the caller validates requests against.
type ModelInfo struct {
	ModelID      string
	APIModel     string
	Driver       string
	Capabilities *models.VideoCapabilities
}

// ErrorKind classifies a failure so the caller can decide what to tell the
// user and whether the job is worth keeping.
type ErrorKind string

const (
	// KindFiltered: a safety filter withheld the clip. Not billed, not retryable.
	KindFiltered ErrorKind = "filtered"
	// KindFailed: the provider reported the job itself failed.
	KindFailed ErrorKind = "failed"
	// KindQuota: rate or quota exhausted at submission.
	KindQuota ErrorKind = "quota"
	// KindInvalid: the provider rejected the request as malformed.
	KindInvalid ErrorKind = "invalid"
	// KindExpired: the job or its result is gone (retention elapsed).
	KindExpired ErrorKind = "expired"
	// KindTimeout: the poll deadline passed; the job may still be running.
	KindTimeout ErrorKind = "timeout"
)

// Error is a classified provider failure.
type Error struct {
	Kind    ErrorKind
	Message string
	// Job is set when the failure happened after the provider accepted the
	// job, so the caller can keep it for a later resume.
	Job Job
}

func (e *Error) Error() string { return e.Message }

// WaitOptions bounds the poll loop.
type WaitOptions struct {
	// Interval between polls. Zero means DefaultPollInterval.
	Interval time.Duration
	// Deadline is the total time to wait. Zero means DefaultPollDeadline.
	Deadline time.Duration
	// MaxConsecutiveErrors tolerated from Poll (network blips) before giving
	// up. Zero means 5.
	MaxConsecutiveErrors int
}

const (
	// DefaultPollInterval: the Veo SDK samples every 10s; 5s halves the tail
	// latency of a short clip without hammering the operations endpoint.
	DefaultPollInterval = 5 * time.Second
	// DefaultPollDeadline: Google documents a 6 minute worst case at peak.
	DefaultPollDeadline = 10 * time.Minute
)
