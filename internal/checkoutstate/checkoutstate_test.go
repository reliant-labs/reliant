// Copyright (c) 2025 Reliant Labs
package checkoutstate

import (
	"testing"

	"github.com/stretchr/testify/assert"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func TestOn(t *testing.T) {
	rows := []*db.ProjectDaemon{
		{DaemonID: "a", InstallState: core.ProjectInstallFailed, InstallError: "auth"},
		{DaemonID: "b", InstallState: core.ProjectInstallInstalling},
	}
	cases := []struct {
		daemon string
		want   reliantv1.ProjectCheckoutState
		reason string
	}{
		{"a", reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONE_FAILED, "auth"},
		{"b", reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING, ""},
		{"c", reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT, ""},
		// Unknown machine: only an in-flight clone counts.
		{"", reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING, ""},
	}
	for _, c := range cases {
		got, reason := On(rows, c.daemon)
		assert.Equal(t, c.want, got, "daemon %q", c.daemon)
		assert.Equal(t, c.reason, reason, "daemon %q", c.daemon)
	}
}
