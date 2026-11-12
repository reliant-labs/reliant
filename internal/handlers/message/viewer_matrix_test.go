package message

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// The viewer matrix for MessageService (control-plane docs/design/org-sharing.md
// §4): every rpc that takes a chat or a project answers a viewer of a shared
// one per viewertest.Matrix.
func TestMessageServiceViewerMatrix(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestMessage(t, WithMessageDeps(Deps{Repo: repo}))
	viewertest.RunService(t, f, svc, "reliant.v1.MessageService", nil)
}

// A viewer's GetMessage leaves the tool result out, as ListMessages does.
func TestGetMessageElidesResultsForAViewer(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestMessage(t, WithMessageDeps(Deps{Repo: repo}))
	get := func(ctx context.Context) *reliantv1.ContentBlock {
		resp, err := svc.GetMessage(ctx, connect.NewRequest(&reliantv1.GetMessageRequest{MessageId: f.Transcript.ToolMessageID}))
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range resp.Msg.Message.GetContentBlocks() {
			if b.GetId() == f.Transcript.ResultBlockID {
				return b
			}
		}
		t.Fatal("no result block")
		return nil
	}
	if b := get(f.Viewer); b.Content != nil || !b.GetBodyElided() {
		t.Fatalf("viewer: content %q elided %v, want no content", b.GetContent(), b.GetBodyElided())
	}
	if b := get(f.Owner); b.GetContent() == "" {
		t.Fatal("owner: the result is missing")
	}
}

// GetMessage shapes a viewer's answer from the one decision that admitted
// them: membership that answers "member" once and then expires or fails
// cannot hand the viewer the owner's unredacted message.
func TestGetMessageDecidesViewerAccessOnce(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestMessage(t, WithMessageDeps(Deps{Repo: repo}))
	for _, after := range []error{nil, errors.New("control plane down")} {
		m := &viewertest.FirstAnswerOnly{First: f.Membership, After: after}
		resp, err := svc.GetMessage(f.ContextWith(f.ViewerID, m), connect.NewRequest(&reliantv1.GetMessageRequest{MessageId: f.Transcript.ToolMessageID}))
		if err != nil {
			t.Fatalf("after %v: %v", after, err)
		}
		if m.Asked() != 1 {
			t.Errorf("after %v: membership asked %d times, want once", after, m.Asked())
		}
		for _, b := range resp.Msg.Message.GetContentBlocks() {
			if b.GetId() == f.Transcript.ResultBlockID && (b.Content != nil || !b.GetBodyElided()) {
				t.Errorf("after %v: the viewer got the tool result", after)
			}
		}
	}
}

// GetMessage refuses a viewer a hidden message (a compaction summary, a
// mailbox envelope, a sub-agent's result body) as NotFound, before loading
// any of it; its owner still reads it by id.
func TestGetMessageRefusesAViewerAHiddenMessage(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	hidden := f.Hidden
	svc := NewTestMessage(t, WithMessageDeps(Deps{Repo: repo}))
	req := connect.NewRequest(&reliantv1.GetMessageRequest{MessageId: hidden.MessageID})

	_, err := svc.GetMessage(f.Viewer, req)
	if code := svcerr.Code(err); code != connect.CodeNotFound {
		t.Fatalf("viewer: %v (%v), want NotFound", code, err)
	}
	owned, err := svc.GetMessage(f.Owner, req)
	if err != nil {
		t.Fatalf("owner: %v", err)
	}
	if b := owned.Msg.Message.GetContentBlocks(); len(b) != 1 || b[0].GetContent() != hidden.Secret {
		t.Fatalf("owner: %v, want the hidden text", b)
	}
}
