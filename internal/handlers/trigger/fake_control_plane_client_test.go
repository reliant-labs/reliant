package trigger

import (
	"context"

	"github.com/reliant-labs/reliant/internal/controlplane"
)

// fakeControlPlaneClient is the controlplane.Client trigger_automation_test.go
// drives. internal/handlers/settings keeps its own copy
// (research/forge-migration/06-p3b-recipe.md §3.7): a shared fake would have
// to leave _test.go, which gives controlplane.Client a second implementation
// and makes forge refuse controlplane's //forge:exclude-contract.
type fakeControlPlaneClient struct {
	issueKey       string
	issueRotated   bool
	issueErr       error
	issueCallCount int
	mints          []mintCall
	mintErr        error
	revokes        []string
	lastIssueJWT   string
	lastDevice     string
}

func (f *fakeControlPlaneClient) MintLLMKey(_ context.Context, jwt, deviceName string) (controlplane.LLMKey, error) {
	f.issueCallCount++
	f.lastIssueJWT, f.lastDevice = jwt, deviceName
	if f.issueErr != nil {
		return controlplane.LLMKey{}, f.issueErr
	}
	return controlplane.LLMKey{Plaintext: f.issueKey, Rotated: f.issueRotated}, nil
}

// DeleteCurrentUserAccount satisfies controlplane.Client. The trigger tests
// never exercise account deletion; account's own tests cover that path.
func (f *fakeControlPlaneClient) DeleteCurrentUserAccount(context.Context, string) (*controlplane.AccountDeletionResult, error) {
	return &controlplane.AccountDeletionResult{}, nil
}

func (f *fakeControlPlaneClient) PreviewAccountDeletionWallet(context.Context, string) (*controlplane.AccountDeletionWalletQuote, error) {
	return nil, nil
}

func (f *fakeControlPlaneClient) CloneRepoOntoDaemon(context.Context, string, controlplane.CloneRepoRequest) (controlplane.CloneRepoResult, error) {
	return controlplane.CloneRepoResult{}, nil
}

type mintCall struct{ JWT, DaemonID, Name string }

func (f *fakeControlPlaneClient) MintDaemonResumeToken(_ context.Context, jwt, daemonID, name string) (controlplane.DaemonResumeToken, error) {
	f.mints = append(f.mints, mintCall{jwt, daemonID, name})
	if f.mintErr != nil {
		return controlplane.DaemonResumeToken{}, f.mintErr
	}
	return controlplane.DaemonResumeToken{Plaintext: "rlat_resume_" + daemonID}, nil
}

func (f *fakeControlPlaneClient) RevokeDaemonResumeTokens(_ context.Context, _, daemonID string) error {
	f.revokes = append(f.revokes, daemonID)
	return nil
}
