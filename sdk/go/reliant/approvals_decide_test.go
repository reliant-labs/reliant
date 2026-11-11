// Copyright (c) 2026 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"errors"
	"slices"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

func refusal(reason, scopes string) *connect.Error {
	e := connect.NewError(connect.CodePermissionDenied, errors.New("refused"))
	e.Meta().Set(headerErrorReason, reason)
	if scopes != "" {
		e.Meta().Set(headerErrorScopes, scopes)
	}
	return e
}

func TestApprovalsDecideSendsTheRequest(t *testing.T) {
	var ap *reliantv1.ApproveRequest
	var dn *reliantv1.DenyRequest
	var ba *reliantv1.BatchApproveRequest
	var bd *reliantv1.BatchDenyRequest
	f := &fake{
		approve: func(r *reliantv1.ApproveRequest) (*reliantv1.ApproveResponse, error) {
			ap = r
			return &reliantv1.ApproveResponse{Success: true, Message: "ok"}, nil
		},
		deny: func(r *reliantv1.DenyRequest) (*reliantv1.DenyResponse, error) {
			dn = r
			return &reliantv1.DenyResponse{Success: true}, nil
		},
		batchApprove: func(r *reliantv1.BatchApproveRequest) (*reliantv1.BatchApproveResponse, error) {
			ba = r
			return &reliantv1.BatchApproveResponse{Success: true, Approved: 2, ChatId: "c-1", Message: "Approved 2 requests"}, nil
		},
		batchDeny: func(r *reliantv1.BatchDenyRequest) (*reliantv1.BatchDenyResponse, error) {
			bd = r
			return &reliantv1.BatchDenyResponse{Success: true, Denied: 1, ChatId: "c-1"}, nil
		},
	}
	c, _ := f.start(t)
	ctx := context.Background()

	d, err := c.Approvals.Approve(ctx, "a-1", DecideOptions{ActionTaken: "Deploy Now"})
	if err != nil || d.Message != "ok" || ap.GetRequestId() != "a-1" || ap.GetActionTaken() != "Deploy Now" {
		t.Fatalf("approve: %v %v %v", d, err, ap)
	}
	if _, err := c.Approvals.Deny(ctx, "a-2", DecideOptions{Reason: "too risky"}); err != nil || dn.GetRequestId() != "a-2" || dn.GetDenialReason() != "too risky" || dn.ActionTaken != nil {
		t.Fatalf("deny: %v %v", err, dn)
	}
	b, err := c.Approvals.BatchApprove(ctx, []string{"a-1", "a-3"}, DecideOptions{})
	if err != nil || b.Decided != 2 || b.ChatID != "c-1" || !slices.Equal(ba.GetRequestIds(), []string{"a-1", "a-3"}) || ba.ActionTaken != nil {
		t.Fatalf("batch approve: %v %v %v", b, err, ba)
	}
	b, err = c.Approvals.BatchDeny(ctx, []string{"a-4"}, DecideOptions{Reason: "no"})
	if err != nil || b.Decided != 1 || bd.GetDenialReason() != "no" {
		t.Fatalf("batch deny: %v %v %v", b, err, bd)
	}
}

func TestApprovalsDecideRequiresIDs(t *testing.T) {
	c, _ := (&fake{}).start(t)
	ctx := context.Background()
	_, e1 := c.Approvals.Approve(ctx, "", DecideOptions{})
	_, e2 := c.Approvals.Deny(ctx, "", DecideOptions{})
	_, e3 := c.Approvals.BatchApprove(ctx, nil, DecideOptions{})
	_, e4 := c.Approvals.BatchDeny(ctx, nil, DecideOptions{})
	for i, err := range []error{e1, e2, e3, e4} {
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("call %d: %v", i, err)
		}
	}
}

func TestApprovalsDecideRefusals(t *testing.T) {
	cases := map[string]struct {
		err   *connect.Error
		want  []error
		scope []string
	}{
		"scope missing": {refusal("scope_missing", "approvals:decide"), []error{ErrScopeMissing, ErrPermissionDenied}, []string{"approvals:decide"}},
		"machine bound": {refusal("machine_credential_cannot_decide", ""), []error{ErrMachineCredential, ErrPermissionDenied}, nil},
		"not pending":   {connect.NewError(connect.CodeFailedPrecondition, errors.New("already approved")), []error{ErrFailedPrecondition}, nil},
		"not found":     {connect.NewError(connect.CodeNotFound, errors.New("approval not found")), []error{ErrNotFound}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fake{
				approve:      func(*reliantv1.ApproveRequest) (*reliantv1.ApproveResponse, error) { return nil, tc.err },
				deny:         func(*reliantv1.DenyRequest) (*reliantv1.DenyResponse, error) { return nil, tc.err },
				batchApprove: func(*reliantv1.BatchApproveRequest) (*reliantv1.BatchApproveResponse, error) { return nil, tc.err },
				batchDeny:    func(*reliantv1.BatchDenyRequest) (*reliantv1.BatchDenyResponse, error) { return nil, tc.err },
			}
			c, _ := f.start(t)
			ctx := context.Background()
			_, e1 := c.Approvals.Approve(ctx, "a", DecideOptions{})
			_, e2 := c.Approvals.Deny(ctx, "a", DecideOptions{})
			_, e3 := c.Approvals.BatchApprove(ctx, []string{"a"}, DecideOptions{})
			_, e4 := c.Approvals.BatchDeny(ctx, []string{"a"}, DecideOptions{})
			for i, err := range []error{e1, e2, e3, e4} {
				for _, w := range tc.want {
					if !errors.Is(err, w) {
						t.Errorf("call %d: %v is not %v", i, err, w)
					}
				}
				var re *Error
				if !errors.As(err, &re) || !slices.Equal(re.Scopes, tc.scope) {
					t.Errorf("call %d: scopes = %+v", i, re)
				}
			}
		})
	}
}

func TestApprovalNotPendingReason(t *testing.T) {
	e := connect.NewError(connect.CodeFailedPrecondition, errors.New("already resolved"))
	e.Meta().Set(headerErrorReason, "approval_not_pending")
	f := &fake{approve: func(*reliantv1.ApproveRequest) (*reliantv1.ApproveResponse, error) { return nil, e }}
	c, _ := f.start(t)
	_, err := c.Approvals.Approve(context.Background(), "a", DecideOptions{})
	if !errors.Is(err, ErrApprovalNotPending) || !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("%v", err)
	}
}

func TestApprovalsDecideDeclinedIsNotApplied(t *testing.T) {
	f := &fake{approve: func(*reliantv1.ApproveRequest) (*reliantv1.ApproveResponse, error) {
		return &reliantv1.ApproveResponse{Success: false, Message: "nope"}, nil
	}}
	c, _ := f.start(t)
	if _, err := c.Approvals.Approve(context.Background(), "a", DecideOptions{}); !errors.Is(err, ErrNotApplied) {
		t.Fatalf("%v", err)
	}
}
