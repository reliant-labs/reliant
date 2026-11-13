package question

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// The viewer matrix for QuestionService (control-plane docs/design/org-sharing.md
// §4): every rpc that takes a chat or a project answers a viewer of a shared
// one per viewertest.Matrix.
func TestQuestionServiceViewerMatrix(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestQuestion(t, WithQuestionDeps(Deps{Repo: repo}))
	viewertest.RunService(t, f, svc, "reliant.v1.QuestionService", nil)
}

// A workflow step's question is a resolved prompt: its metadata (the question
// and options the engine rendered) can carry an earlier step's or tool's
// result, so a viewer gets the card's identity and status without it. An
// agent's ask_user question is the model's own words, already in the viewer's
// transcript as the tool call's input, and stays whole. The owner reads both.
func TestViewerQuestionsCarryNoResolvedPrompt(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	step := f.SeedResolvedStep(t)
	ctx := context.Background()
	resolved := `{"question":"Deploy with ` + step.Secret + `?"}`
	asked := `{"question":"Which branch?"}`
	callID := "call-ask-" + f.ChatID
	for _, q := range []*db.Question{
		{ID: "q-step-" + f.ChatID, ChatID: f.ChatID, WorkflowID: step.WorkflowID, ThreadID: step.WorkflowID, StepID: "confirm",
			Status: core.QuestionStatusPending, Metadata: &resolved, Async: true, CreatedAt: viewertest.SeededAt},
		{ID: "q-ask-" + f.ChatID, ChatID: f.ChatID, WorkflowID: step.WorkflowID, ThreadID: step.WorkflowID, StepID: "ask_user",
			Status: core.QuestionStatusPending, Metadata: &asked, ToolCallID: &callID, Async: true, CreatedAt: viewertest.SeededAt},
	} {
		if err := repo.CreateQuestion(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	svc := NewTestQuestion(t, WithQuestionDeps(Deps{Repo: repo}))
	list := func(ctx context.Context) map[string]*reliantv1.QuestionInfo {
		resp, err := svc.ListAsyncQuestions(ctx, connect.NewRequest(&reliantv1.ListAsyncQuestionsRequest{ChatId: f.ChatID}))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]*reliantv1.QuestionInfo{}
		for _, q := range resp.Msg.GetQuestions() {
			out[q.GetQuestionId()] = q
		}
		return out
	}
	viewed := list(f.Viewer)
	if q := viewed["q-step-"+f.ChatID]; q == nil || q.Metadata != nil || q.GetStatus() == "" {
		t.Fatalf("a viewer's workflow question: %v, want it listed without its metadata", q)
	}
	if q := viewed["q-ask-"+f.ChatID]; q == nil || q.GetMetadata() != asked {
		t.Fatalf("a viewer's ask_user question: %v, want it whole", q)
	}
	if q := list(f.Owner)["q-step-"+f.ChatID]; q == nil || q.GetMetadata() != resolved {
		t.Fatalf("the owner's workflow question: %v, want it whole", q)
	}
}
