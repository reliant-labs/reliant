// Copyright (c) 2026 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

type (
	// ToolBodyMode is how a transcript read sends the bodies of finished tool
	// calls: the tool_bodies field of a GetChatUpdates request.
	ToolBodyMode = reliantv1.ToolBodyMode
	// ContentBlockBody is one block's body, as [RunsClient.ContentBlockBodies]
	// returns it: a tool call's input, a tool result's content, or thinking.
	ContentBlockBody = reliantv1.ContentBlockBody
)

const (
	// ToolBodiesFull is what every read sends unless asked otherwise: tool
	// inputs whole, tool results previewed at 8 KiB.
	ToolBodiesFull = reliantv1.ToolBodyMode_TOOL_BODY_MODE_FULL
	// ToolBodiesStub leaves the bodies of finished tool calls (and thinking)
	// out of a read, marking each such block body_elided; read one back with
	// [RunsClient.ContentBlockBodies]. A server that predates the field
	// ignores it and sends full bodies, so act on each block's body_elided,
	// never on the mode asked for.
	ToolBodiesStub = reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB
)

// MaxContentBlockBodyIDs is the most block ids one GetContentBlockBodies
// request may name. [RunsClient.ContentBlockBodies] takes any number and
// batches them.
const MaxContentBlockBodyIDs = 100

// ContentBlockBodiesOptions configures [RunsClient.ContentBlockBodies].
type ContentBlockBodiesOptions struct {
	// Full returns tool results whole, rather than as the 8 KiB display
	// preview (marked Truncated) a transcript read sends.
	Full bool
}

// ContentBlockBodies is what [RunsClient.ContentBlockBodies] read. Every id
// asked for is in exactly one of Bodies and NotFound.
type ContentBlockBodies struct {
	// Bodies by block id.
	Bodies map[string]*ContentBlockBody
	// NotFound are ids that are not blocks the session displays: unknown,
	// another session's, or hidden. Asking again will not find them.
	NotFound []string
}

// ContentBlockBodies reads the bodies of a session's content blocks by block
// id: the tool inputs, tool results and thinking that a read with
// [ToolBodiesStub] left out (ContentBlock.body_elided). A block of history a
// branched session inherits resolves too.
//
// Any number of ids: duplicates and empty ids are dropped, the rest are sent
// at most [MaxContentBlockBodyIDs] a request, and the ids the server defers to
// keep a response under its byte budget are asked for again.
func (r *RunsClient) ContentBlockBodies(ctx context.Context, sessionID string, blockIDs []string, opts ContentBlockBodiesOptions) (*ContentBlockBodies, error) {
	const op = "runs.contentBlockBodies"
	if sessionID == "" {
		return nil, r.c.fail(op, ErrInvalidArgument, "session id is required")
	}
	out := &ContentBlockBodies{Bodies: map[string]*ContentBlockBody{}}
	seen := map[string]bool{}
	var queue []string
	for _, id := range blockIDs {
		if id != "" && !seen[id] {
			seen[id] = true
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		n := min(len(queue), MaxContentBlockBodyIDs)
		asked := queue[:n:n]
		queue = queue[n:]
		req := &reliantv1.GetContentBlockBodiesRequest{ChatId: sessionID, BlockIds: asked}
		if opts.Full {
			req.Full = &opts.Full
		}
		resp, err := r.chat.GetContentBlockBodies(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, r.c.wrap(op, err)
		}
		// Only ids this request named count as answered: an id the server
		// names that was not asked (or was answered before) is not progress.
		pending := make(map[string]bool, len(asked))
		for _, id := range asked {
			pending[id] = true
		}
		answered := map[string]bool{}
		for _, b := range resp.Msg.GetBodies() {
			if id := b.GetBlockId(); pending[id] && !answered[id] {
				out.Bodies[id] = b
				answered[id] = true
			}
		}
		for _, id := range resp.Msg.GetNotFound() {
			if pending[id] && !answered[id] {
				out.NotFound = append(out.NotFound, id)
				answered[id] = true
			}
		}
		// The server answers at least one asked id per response, so every
		// round removes at least one id from the queue and the loop ends: a
		// server that keeps deferring an id still settles the others, and once
		// that id is the only one left, a response that answers nothing fails.
		if len(answered) == 0 {
			return nil, r.c.fail(op, ErrUnavailable, "the server answered none of the %d block ids it was asked for", len(asked))
		}
		deferred := map[string]bool{}
		for _, id := range resp.Msg.GetDeferred() {
			deferred[id] = true
		}
		var retry []string
		for _, id := range asked {
			switch {
			case answered[id]:
			case deferred[id]:
				retry = append(retry, id)
			default:
				// Named in none of the three lists: not one the server can find.
				out.NotFound = append(out.NotFound, id)
			}
		}
		queue = append(retry, queue...)
	}
	return out, nil
}
