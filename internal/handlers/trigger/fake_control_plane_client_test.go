package trigger

import (
	"context"

	"github.com/reliant-labs/reliant/internal/controlplane"
)

// fakeControlPlaneClient is the AutomationTokens trigger_automation_test.go
// drives.
type fakeControlPlaneClient struct {
	mints   []mintCall
	mintErr error
	revokes []string
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
