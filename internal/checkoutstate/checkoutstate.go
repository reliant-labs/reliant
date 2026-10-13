// Copyright (c) 2025 Reliant Labs

// Package checkoutstate says what state a project's checkout is in on one
// machine, from the project's clone records (project_daemons). The Files tab
// (FileSystemProxyService) and the terminal (TerminalWSHandler) both explain
// a missing project directory with it.
//
//forge:exclude-contract: pure reading of clone records the caller already fetched; no I/O, state or time
package checkoutstate

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// On reads one machine's clone record for a project. With no
// machine to match (the router could not say), only an in-flight clone counts:
// claiming CLONE_FAILED from some other machine's record would be wrong, and
// CLONING is the one state that resolves itself.
func On(rows []*core.ProjectDaemon, daemonID string) (reliantv1.ProjectCheckoutState, string) {
	for _, row := range rows {
		if row == nil {
			continue
		}
		if daemonID != "" && row.DaemonID != daemonID {
			continue
		}
		switch row.InstallState {
		case core.ProjectInstallInstalling:
			return reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING, ""
		case core.ProjectInstallFailed:
			if daemonID != "" {
				return reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONE_FAILED, row.InstallError
			}
		}
	}
	return reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT, ""
}
