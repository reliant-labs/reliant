// Copyright (c) 2025 Reliant Labs
package prhandoffadapter

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

type publishAuthors struct{ a prhandoff.Author }

func (a publishAuthors) Resolve(context.Context, prhandoff.Site) (prhandoff.Author, error) {
	return a.a, nil
}
func (a publishAuthors) Checkout(context.Context, prhandoff.Author, prhandoff.Repository) (string, error) {
	return "/workspace/repo", nil
}
func (a publishAuthors) Available(context.Context, prhandoff.Author) (bool, error) { return true, nil }

type publishProof struct{ result runcontinuity.Result }

func (p publishProof) Resolve(context.Context, runcontinuity.Execution) (runcontinuity.Result, error) {
	return p.result, nil
}

type publishGH struct {
	repo prhandoff.Repository
	head string
}

func (g publishGH) Repository(context.Context, string, string) (prhandoff.Repository, error) {
	return g.repo, nil
}
func (g publishGH) BranchHead(context.Context, string, prhandoff.Repository, string) (string, error) {
	return g.head, nil
}
func (g publishGH) Pull(context.Context, string, prhandoff.Repository, int64) (prhandoff.Pull, error) {
	return prhandoff.Pull{}, nil
}
func (g publishGH) Review(context.Context, string, prhandoff.Repository, int64, string) (prhandoff.Completion, error) {
	return prhandoff.Completion{}, nil
}

func TestContinuationPublishRefreshesOriginalAuthorOnlyWithExactProof(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "proven internal successor", false: "wrong current execution"}[valid], func(t *testing.T) {
			database := storetest.New(t)
			ctx := context.Background()
			persistent := store.PRHandoffStore{DB: database.ORM}
			before, after := strings.Repeat("a", 40), strings.Repeat("b", 40)
			repo := prhandoff.Repository{ID: 42, Name: "org/repo"}
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestActivityEnvironment()
			invoke := func(ctx context.Context) error {
				e := activity.GetInfo(ctx).WorkflowExecution
				original := prhandoff.Author{OwnerID: "owner", RootID: e.ID, ExecutionID: "original", ChatID: "chat", WorktreeID: "wt"}
				if err := persistent.RecordPublication(ctx, prhandoff.Publication{ID: "old", Author: original, Repo: repo, Branch: "fix", HeadSHA: before}); err != nil {
					return err
				}
				if err := persistent.Enroll(ctx, prhandoff.Binding{ID: "binding", Author: original, PublicationID: "old", Pull: prhandoff.Pull{Repo: repo, Number: 1, ObjectID: 11, HeadRepoID: 42, Branch: "fix", HeadSHA: before, Open: true}}); err != nil {
					return err
				}
				current := original
				current.ExecutionID = e.RunID
				recipient := runcontinuity.Execution{WorkflowID: e.ID, RunID: e.RunID}
				if !valid {
					recipient.RunID = "unrelated"
				}
				proof := runcontinuity.Result{Original: runcontinuity.Execution{WorkflowID: e.ID, RunID: "original"}, Current: recipient, Live: true, Edges: []runcontinuity.Edge{{From: runcontinuity.Execution{WorkflowID: e.ID, RunID: "original"}, To: recipient}}}
				adapter := ContinuationPublisher{Store: persistent, Bindings: persistent, Authors: publishAuthors{current}, GitHub: publishGH{repo, after}, Continuity: publishProof{proof}, Initial: &receiptRegistry{}}
				err := adapter.Published(ctx, prhandoff.PublishInput{Site: prhandoff.Site{RootID: e.ID, ExecutionID: e.RunID, ChatID: "chat"}, Repository: "org/repo", WorktreePath: "/workspace/repo", Branch: "fix", HeadSHA: after, PreviousHeadSHA: before})
				if !valid {
					require.Error(t, err)
					return nil
				}
				return err
			}
			env.RegisterActivity(invoke)
			_, err := env.ExecuteActivity(invoke)
			require.NoError(t, err)
			binding, err := persistent.Binding(ctx, "owner", 42, 1)
			require.NoError(t, err)
			require.Equal(t, "original", binding.Author.ExecutionID)
			if valid {
				require.Equal(t, after, binding.Pull.HeadSHA)
			} else {
				require.Equal(t, before, binding.Pull.HeadSHA)
			}
		})
	}
}
