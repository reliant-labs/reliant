// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

func TestGatesPendingMergesQuestionsAndPendingApprovalsOnly(t *testing.T) {
	meta := `{"type":"ask_user","questions":[{"question":"Which?","options":[{"label":"A"},{"label":"B"}],"allow_multiple":true},{"question":"Sure?"}]}`
	fd := &feed{
		pending: []*reliantv1.QuestionInfo{
			{QuestionId: "q-1", StepId: "stuck_checkpoint", ThreadId: "th-1", Metadata: &meta},
			{QuestionId: "q-2", StepId: "review", ThreadId: "th-2"},
		},
		approval: []*reliantv1.Approval{
			{Id: "a-1", Title: "Deploy?", Status: reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING},
			{Id: "a-2", Title: "old", Status: reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED},
		},
		// The run's own thread and a child workflow's: both gated threads are the run's.
		root: &reliantv1.WorkflowExecution{Id: "r-1", Thread: "th-1", Children: []*reliantv1.WorkflowExecution{{Id: "c-1", Thread: "th-2"}}},
	}
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)

	gates, err := c.Gates.Pending(context.Background(), "r-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 3 {
		t.Fatalf("gates = %+v, want 2 questions and 1 pending approval", gates)
	}
	q1 := gates[0]
	if q1.Kind != GateQuestion || q1.ID != "q-1" || q1.ThreadID != "th-1" || !q1.Stuck || len(q1.Prompts) != 2 ||
		!q1.Prompts[0].AllowMultiple || q1.Prompts[0].Options[1] != "B" {
		t.Fatalf("q-1 = %+v", q1)
	}
	if gates[1].Stuck || len(gates[1].Prompts) != 0 {
		t.Fatalf("q-2 = %+v: a routine gate without ask_user metadata", gates[1])
	}
	if a := gates[2]; a.Kind != GateApproval || a.ID != "a-1" || a.Title != "Deploy?" {
		t.Fatalf("approval = %+v", a)
	}
}

func TestGatesAnswerBuildsTheReplyTheServerExpects(t *testing.T) {
	var got *reliantv1.ResolveQuestionRequest
	f := &fake{resolveQuestion: func(r *reliantv1.ResolveQuestionRequest) (*reliantv1.ResolveQuestionResponse, error) {
		got = r
		return &reliantv1.ResolveQuestionResponse{Success: true}, nil
	}}
	c, _ := f.start(t)

	err := c.Gates.Answer(context.Background(), "q-1", []Answer{
		{Question: "Which?", Selected: []string{"A"}},
		{Question: "Why?", Freetext: "because"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetQuestionId() != "q-1" || got.GetAction() != "reply" {
		t.Fatalf("request = %v", got)
	}
	var data struct {
		Answers []struct {
			Question string   `json:"question"`
			Selected []string `json:"selected"`
			Freetext string   `json:"freetext"`
		} `json:"answers"`
	}
	if err := json.Unmarshal([]byte(got.GetResponseData()), &data); err != nil {
		t.Fatalf("response_data is not JSON: %q", got.GetResponseData())
	}
	if len(data.Answers) != 2 || data.Answers[0].Selected[0] != "A" || data.Answers[1].Freetext != "because" {
		t.Fatalf("response_data = %s", got.GetResponseData())
	}
	// `selected` is always an array, even when empty: the web client's shape.
	if data.Answers[1].Selected == nil {
		t.Fatalf("selected must be [] not null: %s", got.GetResponseData())
	}
}

func TestGatesAnswerDeclinedIsAnError(t *testing.T) {
	f := &fake{resolveQuestion: func(*reliantv1.ResolveQuestionRequest) (*reliantv1.ResolveQuestionResponse, error) {
		return &reliantv1.ResolveQuestionResponse{Success: false}, nil
	}}
	c, _ := f.start(t)
	err := c.Gates.Answer(context.Background(), "q-1", []Answer{{Question: "x", Selected: []string{"y"}}})
	if !errors.Is(err, ErrNotApplied) {
		t.Fatalf("a question the server did not resolve = %v, want ErrNotApplied", err)
	}
	if err := c.Gates.Answer(context.Background(), "", []Answer{{}}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("no question id: %v", err)
	}
	if err := c.Gates.Answer(context.Background(), "q", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("no answers: %v", err)
	}
}
