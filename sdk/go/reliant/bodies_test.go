// Copyright (c) 2026 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
	"github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1/reliantv1connect"
)

func TestContentBlockBodiesBatchesAndAsksAgainForTheDeferred(t *testing.T) {
	var mu sync.Mutex
	var asked [][]string
	f := &fake{getContentBlockBodies: func(r *reliantv1.GetContentBlockBodiesRequest) (*reliantv1.GetContentBlockBodiesResponse, error) {
		mu.Lock()
		asked = append(asked, r.GetBlockIds())
		mu.Unlock()
		if r.GetChatId() != "s-1" || !r.GetFull() {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat %q full %v", r.GetChatId(), r.GetFull()))
		}
		// A byte budget that fits two bodies: the rest of the batch is deferred.
		resp := &reliantv1.GetContentBlockBodiesResponse{}
		for i, id := range r.GetBlockIds() {
			switch {
			case i >= 2:
				resp.Deferred = append(resp.Deferred, id)
			case id == "gone":
				resp.NotFound = append(resp.NotFound, id)
			default:
				resp.Bodies = append(resp.Bodies, &reliantv1.ContentBlockBody{BlockId: id, Input: &id})
			}
		}
		return resp, nil
	}}
	c, _ := f.start(t)

	ids := []string{"gone"}
	for i := range 149 {
		ids = append(ids, fmt.Sprintf("b-%d", i))
	}
	ids = append(ids, "b-0", "")
	got, err := c.Runs.ContentBlockBodies(context.Background(), "s-1", ids, ContentBlockBodiesOptions{Full: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Bodies) != 149 || got.Bodies["b-148"].GetInput() != "b-148" {
		t.Errorf("got %d bodies (b-148: %v), want all 149 distinct ids", len(got.Bodies), got.Bodies["b-148"])
	}
	if !reflect.DeepEqual(got.NotFound, []string{"gone"}) {
		t.Errorf("NotFound = %v, want [gone]", got.NotFound)
	}
	if len(asked[0]) != MaxContentBlockBodyIDs {
		t.Errorf("first request named %d ids, want %d", len(asked[0]), MaxContentBlockBodyIDs)
	}
	for _, batch := range asked {
		if len(batch) > MaxContentBlockBodyIDs {
			t.Fatalf("a request named %d ids, over the %d the server takes", len(batch), MaxContentBlockBodyIDs)
		}
	}
}

func TestContentBlockBodiesRefusesToLoopAndNeedsASession(t *testing.T) {
	f := &fake{getContentBlockBodies: func(r *reliantv1.GetContentBlockBodiesRequest) (*reliantv1.GetContentBlockBodiesResponse, error) {
		return &reliantv1.GetContentBlockBodiesResponse{Deferred: r.GetBlockIds()}, nil
	}}
	c, _ := f.start(t)
	if _, err := c.Runs.ContentBlockBodies(context.Background(), "s-1", []string{"a"}, ContentBlockBodiesOptions{}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a server that answers nothing: err = %v, want ErrUnavailable rather than a loop", err)
	}
	if _, err := c.Runs.ContentBlockBodies(context.Background(), "", []string{"a"}, ContentBlockBodiesOptions{}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("no session: err = %v, want ErrInvalidArgument", err)
	}
}

// A server that settles the other ids but defers one on every response (and
// names an id nobody asked for) cannot keep the loop alive: once the deferred
// id is all that is left, a response that answers none of it fails.
func TestContentBlockBodiesEndsWhenOneIDIsAlwaysDeferred(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	f := &fake{getContentBlockBodies: func(r *reliantv1.GetContentBlockBodiesRequest) (*reliantv1.GetContentBlockBodiesResponse, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		resp := &reliantv1.GetContentBlockBodiesResponse{Deferred: []string{"x"}}
		for _, id := range append(r.GetBlockIds(), "unasked") {
			if id != "x" {
				resp.Bodies = append(resp.Bodies, &reliantv1.ContentBlockBody{BlockId: id})
			}
		}
		return resp, nil
	}}
	c, _ := f.start(t)
	if _, err := c.Runs.ContentBlockBodies(context.Background(), "s-1", []string{"a", "x", "b"}, ContentBlockBodiesOptions{}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable rather than a loop", err)
	}
	if calls != 2 {
		t.Errorf("made %d requests, want 2", calls)
	}
}

// The generated client carries tool_bodies on GetChatUpdates for a caller
// that reads a session's feed itself.
func TestGetChatUpdatesCarriesToolBodies(t *testing.T) {
	var got ToolBodyMode
	f := &fake{getChatUpdates: func(r *reliantv1.GetChatUpdatesRequest) (*reliantv1.GetChatUpdatesResponse, error) {
		got = r.GetToolBodies()
		return &reliantv1.GetChatUpdatesResponse{}, nil
	}}
	c, _ := f.start(t)
	chat := reliantv1connect.NewChatServiceClient(c.HTTPClient(), c.BaseURL())
	mode := ToolBodiesStub
	if _, err := chat.GetChatUpdates(context.Background(), connect.NewRequest(&reliantv1.GetChatUpdatesRequest{ChatId: "s-1", ToolBodies: &mode})); err != nil {
		t.Fatal(err)
	}
	if got != ToolBodiesStub {
		t.Errorf("server saw tool_bodies %v, want STUB", got)
	}
}
