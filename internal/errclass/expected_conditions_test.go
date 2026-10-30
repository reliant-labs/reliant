// Copyright (c) 2025 Reliant Labs
package errclass_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/errclass"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/integrations/ghaccess"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// Expected conditions — the user has not connected GitHub, a launch waits for
// capacity, a trigger names a workflow that is gone — are user errors: logged
// at INFO with error_class=user and never grouped into an issue. Prod audit
// 2026-10-29: ~60% of 263 open issues were these, each with a NewIssue alert.
// What is ours to fix stays a server fault.
func TestExpectedConditionsAreUserErrors(t *testing.T) {
	for name, err := range map[string]error{
		"GitHub not connected (control plane)":   gitcredentialclient.ErrNotConnected,
		"GitHub needs reconnect (control plane)": gitcredentialclient.ErrNeedsReconnect,
		"App not installed on the repo":          gitcredentialclient.ErrAppNotInstalled,
		"App permissions not accepted":           gitcredentialclient.ErrAppPermissions,
		"owner cannot push":                      gitcredentialclient.ErrOwnerCannotPush,
		"GitHub not connected (access refresh)":  fmt.Errorf("%w: connect GitHub in Settings", ghaccess.ErrNotConnected),
		"GitHub token rejected":                  ghaccess.ErrTokenRejected,
		"delegated credential, not connected": &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: "GitHub is not connected", Err: gitcredentialclient.ErrNotConnected},
		"credential request with no owner": &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: "the run has no owner to act as"},
		"launch waiting for capacity": fmt.Errorf("start run: %w", &launch.DeclinedError{Reason: "waiting for workflow capacity"}),
		"trigger names a missing workflow": &triggers.DeclarationError{Workflow: "release", Name: "merged",
			Reason: "no project workflow with that name"},
	} {
		assert.Equal(t, svcerr.ClassUser, errclass.Classify(err), name)
	}
}

func TestOurFailuresStayServerErrors(t *testing.T) {
	for name, err := range map[string]error{
		"control plane has no App key": gitcredentialclient.ErrAppNotConfigured,
		"delegated credential, control plane has no App key": &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: "the control plane has no GitHub App key", Err: gitcredentialclient.ErrAppNotConfigured},
		"token fetch unavailable": &httpaction.CredentialError{Code: httpaction.CodeUnavailable,
			Message: "could not get a GitHub token right now; try again", Err: errors.New("dial tcp: i/o timeout")},
		"control plane returned no token": &httpaction.CredentialError{Code: httpaction.CodeInternal,
			Message: "control-plane returned no GitHub token"},
	} {
		assert.Equal(t, svcerr.ClassServer, errclass.Classify(err), name)
	}
}
