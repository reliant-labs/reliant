// Copyright (c) 2026 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
	"github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1/reliantv1connect"
)

func inboxItems(n int) []*reliantv1.InboxItem {
	out := make([]*reliantv1.InboxItem, n)
	for i := range out {
		out[i] = &reliantv1.InboxItem{
			ItemId: fmt.Sprintf("i-%d", i),
			Kind:   reliantv1.InboxItemKind_INBOX_ITEM_KIND_APPROVAL,
		}
	}
	return out
}

func TestInboxListFollowsTruncationUntilComplete(t *testing.T) {
	all := inboxItems(250)
	var limits []int32
	f := &fake{listInbox: func(r *reliantv1.ListInboxRequest) (*reliantv1.ListInboxResponse, error) {
		limits = append(limits, r.GetLimit())
		items, cut := all, false
		if int(r.GetLimit()) < len(all) {
			items, cut = all[:r.GetLimit()], true
		}
		return &reliantv1.ListInboxResponse{Items: items, Truncated: cut}, nil
	}}
	c, _ := f.start(t)

	got, err := c.Inbox.List(context.Background(), ListInbox{}).All()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 250 || got[0].GetItemId() != "i-0" || got[249].GetItemId() != "i-249" {
		t.Fatalf("got %d items", len(got))
	}
	if !slices.Equal(limits, []int32{100, 1000}) {
		t.Fatalf("limits asked = %v, want [100 1000]", limits)
	}
}

func TestInboxListFilters(t *testing.T) {
	items := []*reliantv1.InboxItem{
		{ItemId: "a", Kind: reliantv1.InboxItemKind_INBOX_ITEM_KIND_APPROVAL, TriggerId: "t-1", TriggerName: "nightly"},
		{ItemId: "q", Kind: reliantv1.InboxItemKind_INBOX_ITEM_KIND_QUESTION, TriggerId: "t-2", TriggerName: "weekly"},
		{ItemId: "f", Kind: reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING, TriggerId: "t-1", TriggerName: "nightly"},
	}
	var req *reliantv1.ListInboxRequest
	f := &fake{listInbox: func(r *reliantv1.ListInboxRequest) (*reliantv1.ListInboxResponse, error) {
		req = r
		return &reliantv1.ListInboxResponse{Items: items}, nil
	}}
	c, _ := f.start(t)
	ctx := context.Background()

	ids := func(f ListInbox) []string {
		t.Helper()
		got, err := c.Inbox.List(ctx, f).All()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range got {
			out = append(out, it.GetItemId())
		}
		return out
	}

	if got := ids(ListInbox{ProjectID: "p-1"}); len(got) != 3 || req.GetProjectId() != "p-1" {
		t.Fatalf("project filter: %v, request %v", got, req)
	}
	if got := ids(ListInbox{Kinds: []InboxItemKind{reliantv1.InboxItemKind_INBOX_ITEM_KIND_QUESTION, reliantv1.InboxItemKind_INBOX_ITEM_KIND_APPROVAL}}); !slices.Equal(got, []string{"a", "q"}) {
		t.Fatalf("kinds: %v", got)
	}
	if got := ids(ListInbox{Automation: "t-1"}); !slices.Equal(got, []string{"a", "f"}) {
		t.Fatalf("automation by id: %v", got)
	}
	if got := ids(ListInbox{Automation: "weekly"}); !slices.Equal(got, []string{"q"}) {
		t.Fatalf("automation by name: %v", got)
	}
	if got := ids(ListInbox{Limit: 1}); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("limit: %v", got)
	}
}

// A kind filter is applied after the server's cut, so a match beyond the first
// 100 items must still be found.
func TestInboxListFilterSeesPastTheServersCut(t *testing.T) {
	all := inboxItems(300)
	all[299].Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE
	f := &fake{listInbox: func(r *reliantv1.ListInboxRequest) (*reliantv1.ListInboxResponse, error) {
		if int(r.GetLimit()) < len(all) {
			return &reliantv1.ListInboxResponse{Items: all[:r.GetLimit()], Truncated: true}, nil
		}
		return &reliantv1.ListInboxResponse{Items: all}, nil
	}}
	c, _ := f.start(t)
	got, err := c.Inbox.List(context.Background(), ListInbox{Kinds: []InboxItemKind{reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE}}).All()
	if err != nil || len(got) != 1 || got[0].GetItemId() != "i-299" {
		t.Fatalf("got %v, %v", got, err)
	}
}

// An inbox still cut off at the most the client asks for is not presented as
// complete: the items arrive, then the walk ends with ErrTruncated.
func TestInboxListReportsTruncationAtTheCap(t *testing.T) {
	all := inboxItems(3)
	f := &fake{listInbox: func(r *reliantv1.ListInboxRequest) (*reliantv1.ListInboxResponse, error) {
		return &reliantv1.ListInboxResponse{Items: all, Truncated: true}, nil
	}}
	c, _ := f.start(t)
	got, err := c.Inbox.List(context.Background(), ListInbox{}).All()
	if len(got) != len(all) {
		t.Fatalf("got %d items, want the %d the server returned", len(got), len(all))
	}
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
}

func TestInboxListErrorIsWrapped(t *testing.T) {
	f := &fake{listInbox: func(*reliantv1.ListInboxRequest) (*reliantv1.ListInboxResponse, error) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("scope"))
	}}
	c, _ := f.start(t)
	_, err := c.Inbox.List(context.Background(), ListInbox{}).All()
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
}

func TestInboxCountsAsksForNoItems(t *testing.T) {
	var req *reliantv1.ListInboxRequest
	f := &fake{listInbox: func(r *reliantv1.ListInboxRequest) (*reliantv1.ListInboxResponse, error) {
		req = r
		return &reliantv1.ListInboxResponse{BlockingCount: 3, HasInformational: true, OtherProjectsCount: 2}, nil
	}}
	c, _ := f.start(t)
	n, err := c.Inbox.Counts(context.Background(), "p-1")
	if err != nil {
		t.Fatal(err)
	}
	if req.Limit == nil || req.GetLimit() != 0 || req.GetProjectId() != "p-1" {
		t.Fatalf("request = %v", req)
	}
	if n.Blocking != 3 || !n.HasInformational || n.OtherProjects != 2 {
		t.Fatalf("counts = %+v", n)
	}
}

func TestInboxDismissAndRestore(t *testing.T) {
	var dismissed, restored []string
	f := &fake{
		dismissInbox: func(r *reliantv1.DismissInboxItemRequest) (*reliantv1.DismissInboxItemResponse, error) {
			if slices.Contains(r.GetItemIds(), "bad") {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown item"))
			}
			dismissed = r.GetItemIds()
			return &reliantv1.DismissInboxItemResponse{}, nil
		},
		restoreInbox: func(r *reliantv1.RestoreInboxItemRequest) (*reliantv1.RestoreInboxItemResponse, error) {
			restored = r.GetItemIds()
			return &reliantv1.RestoreInboxItemResponse{}, nil
		},
	}
	c, _ := f.start(t)
	ctx := context.Background()

	if err := c.Inbox.Dismiss(ctx, "a", "b"); err != nil || !slices.Equal(dismissed, []string{"a", "b"}) {
		t.Fatalf("dismiss: %v %v", err, dismissed)
	}
	if err := c.Inbox.Restore(ctx, "a"); err != nil || !slices.Equal(restored, []string{"a"}) {
		t.Fatalf("restore: %v %v", err, restored)
	}
	if err := c.Inbox.Dismiss(ctx, "bad"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unknown id: %v", err)
	}
	if err := c.Inbox.Dismiss(ctx); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("no ids (dismiss): %v", err)
	}
	if err := c.Inbox.Restore(ctx); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("no ids (restore): %v", err)
	}
	if n := len(f.callLog()); n != 3 {
		t.Fatalf("calls = %v: an empty request must not reach the server", f.callLog())
	}
}

func TestApprovalsList(t *testing.T) {
	var req *reliantv1.ListApprovalsByChatRequest
	f := &fake{listApprovals: func(r *reliantv1.ListApprovalsByChatRequest) (*reliantv1.ListApprovalsByChatResponse, error) {
		req = r
		return &reliantv1.ListApprovalsByChatResponse{Approvals: []*reliantv1.Approval{{Id: "a-1"}, {Id: "a-2"}}}, nil
	}}
	c, _ := f.start(t)
	got, err := c.Approvals.List(context.Background(), "chat-1")
	if err != nil || len(got) != 2 || req.GetChatId() != "chat-1" {
		t.Fatalf("got %v, %v, request %v", got, err, req)
	}
	if _, err := c.Approvals.List(context.Background(), ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("no chat: %v", err)
	}
}

func TestApprovalsListForRunKeepsOnlyThatRunsApprovals(t *testing.T) {
	fd := &feed{
		approval: []*reliantv1.Approval{
			{Id: "mine", WorkflowId: ptr("r-1"), Status: reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING},
			{Id: "child", WorkflowId: ptr("c-1"), Status: reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING},
			{Id: "other-run", WorkflowId: ptr("r-2"), Status: reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING},
		},
		root: &reliantv1.WorkflowExecution{Id: "r-1", Thread: "th-1", Children: []*reliantv1.WorkflowExecution{{Id: "c-1", Thread: "th-2"}}},
	}
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)

	got, err := c.Approvals.ListForRun(context.Background(), "r-1")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range got {
		ids = append(ids, a.GetId())
	}
	if !slices.Equal(ids, []string{"mine", "child"}) {
		t.Fatalf("approvals = %v", ids)
	}
}

// The clients have no way to decide an approval or manage a machine, and the
// generated clients carry no such procedure either.
func TestNoDecisionOrMachineMethods(t *testing.T) {
	for _, v := range []any{&ApprovalsClient{}, &InboxClient{}, &GatesClient{}} {
		ty := reflect.TypeOf(v)
		for _, name := range []string{"Approve", "Deny", "BatchApprove", "BatchDeny", "CleanupStorage", "ForgetStorageWorktree"} {
			if _, ok := ty.MethodByName(name); ok {
				t.Errorf("%s has a %s method", ty, name)
			}
		}
	}
	var _ = reliantv1connect.NewApprovalServiceClient(http.DefaultClient, "http://x")
	for _, ty := range []reflect.Type{
		reflect.TypeOf((*reliantv1connect.ApprovalServiceClient)(nil)).Elem(),
		reflect.TypeOf((*reliantv1connect.InboxServiceClient)(nil)).Elem(),
	} {
		for _, name := range []string{"Approve", "Deny", "BatchApprove", "BatchDeny", "CleanupStorage", "ForgetStorageWorktree"} {
			if _, ok := ty.MethodByName(name); ok {
				t.Errorf("generated %s has a %s method", ty.Name(), name)
			}
		}
	}
}
