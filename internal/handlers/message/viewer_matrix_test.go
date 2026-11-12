package message

import (
	"context"
	"testing"

	"connectrpc.com/connect"

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
