// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

func TestTriggersGetByIDIsOneRoundTrip(t *testing.T) {
	f := &fake{getTrigger: func(r *reliantv1.GetTriggerRequest) (*reliantv1.GetTriggerResponse, error) {
		return &reliantv1.GetTriggerResponse{Trigger: &reliantv1.Trigger{Id: r.GetId(), Name: "nightly"}}, nil
	}}
	c, _ := f.start(t)
	tr, err := c.Triggers.Get(context.Background(), "t-1")
	if err != nil || tr.GetId() != "t-1" {
		t.Fatalf("Get = %v, %v", tr, err)
	}
	if len(f.callLog()) != 1 {
		t.Fatalf("an id lookup took %d calls: %v", len(f.callLog()), f.callLog())
	}
}

func TestTriggersGetFallsBackToNameAndNeverGuessesBetweenProjects(t *testing.T) {
	list := []*reliantv1.Trigger{
		{Id: "t-1", Name: "nightly", ProjectId: "p-1"},
		{Id: "t-2", Name: "weekly", ProjectId: "p-1"},
		{Id: "t-3", Name: "dupe", ProjectId: "p-1"},
		{Id: "t-4", Name: "dupe", ProjectId: "p-2"},
	}
	f := &fake{
		getTrigger: func(*reliantv1.GetTriggerRequest) (*reliantv1.GetTriggerResponse, error) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no such trigger"))
		},
		listTriggers: func(*reliantv1.ListTriggersRequest) (*reliantv1.ListTriggersResponse, error) {
			return &reliantv1.ListTriggersResponse{Triggers: list}, nil
		},
	}
	c, _ := f.start(t)
	ctx := context.Background()

	if tr, err := c.Triggers.Get(ctx, "weekly"); err != nil || tr.GetId() != "t-2" {
		t.Fatalf("by name = %v, %v", tr, err)
	}
	_, err := c.Triggers.Get(ctx, "dupe")
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("a name in two projects = %v, want ErrAmbiguous", err)
	}
	for _, id := range []string{"t-3", "t-4", "p-1", "p-2"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("the ambiguity error %q does not list %s", err, id)
		}
	}
	if _, err := c.Triggers.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown name = %v, want ErrNotFound", err)
	}
}

func TestTriggersGetDoesNotMaskAnAuthFailureAsAMissingTrigger(t *testing.T) {
	f := &fake{getTrigger: func(*reliantv1.GetTriggerRequest) (*reliantv1.GetTriggerResponse, error) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("bad token"))
	}}
	c, _ := f.start(t)
	_, err := c.Triggers.Get(context.Background(), "x")
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated, not a name lookup", err)
	}
	if len(f.callLog()) != 1 {
		t.Fatalf("fell through to a list after an auth failure: %v", f.callLog())
	}
}

func TestTriggersFireResolvesNameAndReturnsTheFiringID(t *testing.T) {
	var fired string
	f := &fake{
		getTrigger: func(*reliantv1.GetTriggerRequest) (*reliantv1.GetTriggerResponse, error) {
			return nil, connect.NewError(connect.CodeNotFound, nil)
		},
		listTriggers: func(*reliantv1.ListTriggersRequest) (*reliantv1.ListTriggersResponse, error) {
			return &reliantv1.ListTriggersResponse{Triggers: []*reliantv1.Trigger{{Id: "t-9", Name: "nightly"}}}, nil
		},
		fireTrigger: func(r *reliantv1.FireTriggerRequest) (*reliantv1.FireTriggerResponse, error) {
			fired = r.GetId()
			return &reliantv1.FireTriggerResponse{FireWorkflowId: "fire-1"}, nil
		},
	}
	c, _ := f.start(t)
	res, err := c.Triggers.Fire(context.Background(), "nightly")
	if err != nil {
		t.Fatal(err)
	}
	if fired != "t-9" || res.TriggerID != "t-9" || res.FireWorkflowID != "fire-1" {
		t.Fatalf("fired %q, result %+v", fired, res)
	}
}

func TestTriggersListProjectFilter(t *testing.T) {
	var got *reliantv1.ListTriggersRequest
	f := &fake{listTriggers: func(r *reliantv1.ListTriggersRequest) (*reliantv1.ListTriggersResponse, error) {
		got = r
		return &reliantv1.ListTriggersResponse{Triggers: []*reliantv1.Trigger{{Id: "a"}, {Id: "b"}}}, nil
	}}
	c, _ := f.start(t)
	all, err := c.Triggers.List(context.Background(), ListTriggers{}).All()
	if err != nil || len(all) != 2 || got.ProjectId != nil {
		t.Fatalf("all=%v err=%v req=%v", all, err, got)
	}
	if _, err := c.Triggers.List(context.Background(), ListTriggers{ProjectID: "p"}).All(); err != nil || got.GetProjectId() != "p" {
		t.Fatalf("err=%v req=%v", err, got)
	}
}

func TestTriggersEventsFollowsPagesAndFilters(t *testing.T) {
	pages := map[string]*reliantv1.ListTriggerEventsResponse{
		"":   {Events: []*reliantv1.TriggerEvent{{Id: "e1"}, {Id: "e2"}}, NextPageToken: "n1"},
		"n1": {Events: []*reliantv1.TriggerEvent{{Id: "e3"}}},
	}
	var asked []*reliantv1.ListTriggerEventsRequest
	f := &fake{listTriggerEvents: func(r *reliantv1.ListTriggerEventsRequest) (*reliantv1.ListTriggerEventsResponse, error) {
		asked = append(asked, r)
		return pages[r.GetPageToken()], nil
	}}
	c, _ := f.start(t)

	evs, err := c.Triggers.Events(context.Background(), "t-1", EventFilter{
		Outcomes: []TriggerEventOutcome{reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_FAILED},
		PageSize: 2,
	}).All()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range evs {
		ids = append(ids, e.GetId())
	}
	if !reflect.DeepEqual(ids, []string{"e1", "e2", "e3"}) {
		t.Fatalf("ids = %v", ids)
	}
	if len(asked) != 2 || asked[0].GetTriggerId() != "t-1" || asked[0].GetLimit() != 2 || len(asked[0].GetOutcomes()) != 1 || asked[1].GetPageToken() != "n1" {
		t.Fatalf("requests = %v", asked)
	}
	// An empty trigger id is refused before any rpc.
	f2 := &fake{}
	c2, _ := f2.start(t)
	if _, err := c2.Triggers.Events(context.Background(), "", EventFilter{}).All(); !errors.Is(err, ErrInvalidArgument) || len(f2.callLog()) != 0 {
		t.Fatalf("err=%v calls=%v", err, f2.callLog())
	}
}

func TestTriggersLaunchEventMayBeAbsent(t *testing.T) {
	f := &fake{getLaunchEvent: func(r *reliantv1.GetLaunchEventRequest) (*reliantv1.GetLaunchEventResponse, error) {
		if r.GetChatId() == "has" {
			return &reliantv1.GetLaunchEventResponse{Event: &reliantv1.TriggerEvent{Id: "e1"}}, nil
		}
		return &reliantv1.GetLaunchEventResponse{}, nil
	}}
	c, _ := f.start(t)
	if ev, err := c.Triggers.LaunchEvent(context.Background(), "has"); err != nil || ev.GetId() != "e1" {
		t.Fatalf("has = %v, %v", ev, err)
	}
	if ev, err := c.Triggers.LaunchEvent(context.Background(), "none"); err != nil || ev != nil {
		t.Fatalf("none = %v, %v", ev, err)
	}
}

// The way fire is followed to a run: Fire, then Events until the firing shows
// the session it launched, then Follow that run.
func TestFireThenFollowTheRunItLaunched(t *testing.T) {
	fd := &feed{}
	fd.workflowStatus("started", "")
	fd.workflowStatus("completed", "")
	fd.setRoot(completed, reasonOK, "")
	f := &fake{
		getTrigger: func(r *reliantv1.GetTriggerRequest) (*reliantv1.GetTriggerResponse, error) {
			return &reliantv1.GetTriggerResponse{Trigger: &reliantv1.Trigger{Id: "t-1"}}, nil
		},
		fireTrigger: func(*reliantv1.FireTriggerRequest) (*reliantv1.FireTriggerResponse, error) {
			return &reliantv1.FireTriggerResponse{FireWorkflowId: "fire-1"}, nil
		},
	}
	fd.install(f)
	polls := 0
	f.listTriggerEvents = func(*reliantv1.ListTriggerEventsRequest) (*reliantv1.ListTriggerEventsResponse, error) {
		polls++
		ev := &reliantv1.TriggerEvent{Id: "e1", FireWorkflowId: "fire-1", Outcome: reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_PENDING}
		if polls >= 2 { // the firing settles on the second look
			ev.Outcome = reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_LAUNCHED
			chat := "s-1"
			ev.ChatId = &chat
		}
		return &reliantv1.ListTriggerEventsResponse{Events: []*reliantv1.TriggerEvent{ev}}, nil
	}
	c, _ := f.start(t)
	ctx := context.Background()

	fired, err := c.Triggers.Fire(ctx, "t-1")
	if err != nil {
		t.Fatal(err)
	}
	var launched *TriggerEvent
	for i := 0; i < 5 && launched == nil; i++ {
		evs, err := c.Triggers.Events(ctx, fired.TriggerID, EventFilter{Limit: 10}).All()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if e.GetFireWorkflowId() == fired.FireWorkflowID && e.GetOutcome() == reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_LAUNCHED {
				launched = e
			}
		}
	}
	if launched == nil || launched.GetChatId() != "s-1" {
		t.Fatalf("the firing never showed the session it launched (%d polls)", polls)
	}

	// The event carries a session id, so that is what is followed.
	fl, err := c.Runs.FollowSession(ctx, launched.GetChatId(), FollowOptions{Interval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	res, err := fl.Wait()
	if err != nil || !res.Succeeded {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
}
