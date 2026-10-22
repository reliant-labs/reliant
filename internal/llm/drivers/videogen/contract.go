// Copyright (c) 2025 Reliant Labs
//
// Package videogen is the driver-layer call path for video GENERATION. It is
// parallel to imagegen, with one structural difference that drives the whole
// package: video is a long-running job, so the client interface is Submit then
// Poll, not a single Generate. The split is what makes a render resumable — the
// caller can persist the Job between the two and, after a worker restart, Poll
// the job that is already running instead of paying for a second one.
//
// forge:outbound-io
//
// Each Client calls one provider's video endpoint (Vertex Veo, Gemini Omni,
// or the managed gateway) and serves nothing inbound. internal/llm/drivers
// picks the implementation for the resolved model; its consumers hold the
// narrower VideoGenerator they declare themselves. `forge generate` writes
// MockClient into mock_gen.go.
//
//forge:not-a-component: built by internal/llm/drivers, one client per resolved video model — never a node of the composition graph
package videogen

import "context"

// Client is a video provider bound to one model.
//
//forge:contract
type Client interface {
	// Info names the model the client is bound to and its declared
	// capabilities, which the caller validates requests against.
	Info() ModelInfo
	// Submit starts a render and returns as soon as the provider has accepted
	// it. It retries only failures that happened BEFORE acceptance.
	Submit(ctx context.Context, request Request) (Job, error)
	// Poll checks a previously accepted job. done is false while it is still
	// rendering; a finished job returns its clip, or an *Error.
	Poll(ctx context.Context, job Job) (response *Response, done bool, err error)
	// Cancel asks the provider to stop a job. Best effort; Veo has no cancel.
	Cancel(ctx context.Context, job Job) error
}
