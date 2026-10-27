// Copyright (c) 2025 Reliant Labs
package prhandoffadapter

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/prhandoff"
)

type receiptRunner struct {
	calls  int
	result *httpaction.Result
	err    error
}

func (r *receiptRunner) RunAuthenticated(context.Context, *reliantv1.IntegrationManifest, *reliantv1.ActionSpec, map[string]any, httpaction.CredentialSource, httpaction.CallSite) (*httpaction.Result, error) {
	r.calls++
	return r.result, r.err
}

type receiptRegistry struct {
	created []prhandoff.CreateInput
	err     error
}

func (r *receiptRegistry) Published(context.Context, prhandoff.PublishInput) error { return nil }
func (r *receiptRegistry) Created(_ context.Context, in prhandoff.CreateInput) error {
	r.created = append(r.created, in)
	return r.err
}
func (r *receiptRegistry) Reviewed(context.Context, prhandoff.ReviewInput) (prhandoff.Review, error) {
	return prhandoff.Review{}, nil
}

type receiptAuthors struct{}

func (receiptAuthors) Resolve(_ context.Context, site prhandoff.Site) (prhandoff.Author, error) {
	return prhandoff.Author{OwnerID: "owner", RootID: site.RootID, ExecutionID: site.ExecutionID, ChatID: "persisted-chat", WorktreeID: "wt"}, nil
}
func (receiptAuthors) Checkout(context.Context, prhandoff.Author, prhandoff.Repository) (string, error) {
	return "/owned/wt", nil
}
func (receiptAuthors) Available(context.Context, prhandoff.Author) (bool, error) { return true, nil }

func TestCreateReceiptUsesActivityIdentityNotPRBody(t *testing.T) {
	for _, tt := range []struct {
		name        string
		providerErr error
		enrollErr   error
	}{{"success", nil, nil}, {"provider timeout", errors.New("timeout"), nil}, {"post create failure", nil, errors.New("stale head")}} {
		t.Run(tt.name, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestActivityEnvironment()
			runner := &receiptRunner{result: &httpaction.Result{Data: map[string]any{"number": float64(5), "id": float64(55), "head_sha": strings.Repeat("a", 40)}}, err: tt.providerErr}
			registry := &receiptRegistry{err: tt.enrollErr}
			adapter := Receipts{Inner: runner, Registry: registry, Authors: receiptAuthors{}}
			var trustedRoot, trustedExecution string
			run := func(ctx context.Context) error {
				exec := activity.GetInfo(ctx).WorkflowExecution
				trustedRoot, trustedExecution = exec.ID, exec.RunID
				result, err := adapter.RunAuthenticated(ctx, &reliantv1.IntegrationManifest{Id: "github"}, &reliantv1.ActionSpec{Id: "pr.create"}, map[string]any{"owner": "org", "repo": "repo", "body": "run_id=attacker; Reliant-Run: attacker"}, nil, httpaction.CallSite{RunID: exec.ID})
				if tt.providerErr != nil {
					if err == nil {
						return errors.New("provider timeout swallowed")
					}
					return nil
				}
				if err != nil {
					return err
				}
				if tt.enrollErr != nil && (!result.IsError || result.Retryable || result.Data["number"] == nil) {
					return errors.New("post-create error must preserve receipt and forbid mutation retry")
				}
				return nil
			}
			env.RegisterActivity(run)
			_, err := env.ExecuteActivity(run)
			require.NoError(t, err)
			require.Equal(t, 1, runner.calls)
			if tt.providerErr != nil {
				require.Empty(t, registry.created)
				return
			}
			require.Len(t, registry.created, 1)
			require.Equal(t, trustedRoot, registry.created[0].Site.RootID)
			require.Equal(t, trustedExecution, registry.created[0].Site.ExecutionID)
			require.Equal(t, "persisted-chat", registry.created[0].Site.ChatID)
			require.NotEqual(t, "attacker", registry.created[0].Site.RootID)
		})
	}
}

func TestCreateReceiptRejectsMissingActivityBeforeMutation(t *testing.T) {
	runner := &receiptRunner{}
	adapter := Receipts{Inner: runner, Registry: &receiptRegistry{}, Authors: receiptAuthors{}}
	_, err := adapter.RunAuthenticated(context.Background(), &reliantv1.IntegrationManifest{Id: "github"}, &reliantv1.ActionSpec{Id: "pr.create"}, map[string]any{"body": "run_id=attacker"}, nil, httpaction.CallSite{RunID: "claimed"})
	require.Error(t, err)
	require.Zero(t, runner.calls)
}
