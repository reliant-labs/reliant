// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
	"github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1/reliantv1connect"
)

// TriggersClient reads automations, fires them, and lists what they launched.
// Get one from [Client.Triggers].
//
// Turning an automation on or off, creating one and editing one are not here:
// they decide who runs execute as and where, and they are not part of the
// public API yet. They will be permission-based (token scopes). The shape for
// that is already in place and is purely additive: the client holds one
// generated service client and every method is a thin wrapper over one rpc, so
// Enable, Disable and Activate arrive as new methods on this type once their
// rpcs are classified in proto/public-api.txt and generated into this module.
// Nothing that exists changes.
type TriggersClient struct {
	c        *Client
	triggers reliantv1connect.TriggerServiceClient
}

// ListTriggers filters a trigger list.
type ListTriggers struct {
	// ProjectID narrows to one project. Empty lists the caller's triggers in
	// every project.
	ProjectID string
	// Limit caps how many the iterator yields; zero means all.
	Limit int
}

// Get returns one trigger by id or by name. An id is tried first (one round
// trip); a name needs a list and a match. A name that matches triggers in more
// than one project is [ErrAmbiguous]: the error lists their ids, and the client
// never guesses.
func (t *TriggersClient) Get(ctx context.Context, idOrName string) (*Trigger, error) {
	const op = "triggers.get"
	if idOrName == "" {
		return nil, t.c.fail(op, ErrInvalidArgument, "trigger id or name is required")
	}
	resp, err := t.triggers.GetTrigger(ctx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: idOrName}))
	if err == nil {
		return resp.Msg.GetTrigger(), nil
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		return nil, t.c.wrap(op, err)
	}

	list, err := t.triggers.ListTriggers(ctx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	if err != nil {
		return nil, t.c.wrap(op, err)
	}
	var matches []*Trigger
	for _, tr := range list.Msg.GetTriggers() {
		if tr.GetName() == idOrName {
			matches = append(matches, tr)
		}
	}
	switch len(matches) {
	case 0:
		return nil, t.c.fail(op, ErrNotFound, "no trigger with id or name %q", idOrName)
	case 1:
		return matches[0], nil
	}
	// Names are unique per PROJECT, so the same name can exist in two projects.
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, fmt.Sprintf("%s (project %s)", m.GetId(), m.GetProjectId()))
	}
	return nil, t.c.fail(op, ErrAmbiguous, "%q matches %d triggers across projects; use an id: %s", idOrName, len(matches), strings.Join(ids, ", "))
}

// List lists the caller's triggers, newest first. The server returns them in
// one response; the iterator is the same shape as every other list so a future
// paged response does not change callers.
func (t *TriggersClient) List(ctx context.Context, f ListTriggers) *Iter[*Trigger] {
	return newIter(ctx, f.Limit, func(ctx context.Context, _ string) ([]*Trigger, string, error) {
		req := &reliantv1.ListTriggersRequest{}
		if f.ProjectID != "" {
			req.ProjectId = &f.ProjectID
		}
		resp, err := t.triggers.ListTriggers(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, "", t.c.wrap("triggers.list", err)
		}
		return resp.Msg.GetTriggers(), "", nil
	})
}

// Fired is the result of a manual fire.
type Fired struct {
	// TriggerID is the trigger that was fired, resolved from the id or name.
	TriggerID string
	// FireWorkflowID identifies the asynchronous firing. Match it against
	// TriggerEvent.FireWorkflowID to find the firing's outcome and the run it
	// launched, or use [TriggersClient.Fire] with a follow-up
	// [TriggersClient.Events].
	FireWorkflowID string
}

// Fire fires a trigger now, by id or name. A manual fire is the caller saying
// "run this", so it skips the trigger's enabled and overlap checks.
//
// The firing is asynchronous: Fire returns once it is accepted, not once a run
// exists. List [TriggersClient.Events] for the outcome and the run it launched.
func (t *TriggersClient) Fire(ctx context.Context, idOrName string) (*Fired, error) {
	const op = "triggers.fire"
	tr, err := t.Get(ctx, idOrName)
	if err != nil {
		return nil, err
	}
	resp, err := t.triggers.FireTrigger(ctx, connect.NewRequest(&reliantv1.FireTriggerRequest{Id: tr.GetId()}))
	if err != nil {
		return nil, t.c.wrap(op, err)
	}
	return &Fired{TriggerID: tr.GetId(), FireWorkflowID: resp.Msg.GetFireWorkflowId()}, nil
}

// EventFilter filters a trigger's firings.
type EventFilter struct {
	// Outcomes keeps only firings with one of these outcomes. Empty keeps all.
	Outcomes []TriggerEventOutcome
	// PageSize is how many to request per round trip (server default applies
	// when zero). Limit caps the total the iterator yields; zero means all.
	PageSize int
	Limit    int
}

// Events lists a trigger's firings, newest first, following page tokens. Each
// says whether it launched, was skipped or failed, and for a launch which run
// it started (TriggerEvent.ChatId is the run's session; Run.State its state).
func (t *TriggersClient) Events(ctx context.Context, triggerID string, f EventFilter) *Iter[*TriggerEvent] {
	return newIter(ctx, f.Limit, func(ctx context.Context, token string) ([]*TriggerEvent, string, error) {
		if triggerID == "" {
			return nil, "", t.c.fail("triggers.events", ErrInvalidArgument, "trigger id is required")
		}
		req := &reliantv1.ListTriggerEventsRequest{TriggerId: triggerID, Limit: int32(f.PageSize), Outcomes: f.Outcomes}
		if token != "" {
			req.PageToken = &token
		}
		resp, err := t.triggers.ListTriggerEvents(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, "", t.c.wrap("triggers.events", err)
		}
		return resp.Msg.GetEvents(), resp.Msg.GetNextPageToken(), nil
	})
}

// LaunchEvent returns the firing that launched a session, or nil when it has
// none (the session predates trigger events, or was started by hand).
func (t *TriggersClient) LaunchEvent(ctx context.Context, sessionID string) (*TriggerEvent, error) {
	resp, err := t.triggers.GetLaunchEvent(ctx, connect.NewRequest(&reliantv1.GetLaunchEventRequest{ChatId: sessionID}))
	if err != nil {
		return nil, t.c.wrap("triggers.launchEvent", err)
	}
	return resp.Msg.Event, nil
}
