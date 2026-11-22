// Copyright (c) 2026 Reliant Labs
package workspacecreate_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workspacecreate"
)

// The router's error for a daemon's recovery refusal is what Finish waits
// out, however a Machine wraps it; any other daemon failure is not.
func TestRouterRecoveryRefusalIsWaitedOut(t *testing.T) {
	refusal := toolexec.DaemonReportedFailure("worktree.create", toolexec.ErrRecoveryFinalizing.Error())
	if !workspacecreate.IsRecoveryRefusal(fmt.Errorf("worktree.create: %w", refusal)) {
		t.Fatalf("%v is not recognised as a recovery refusal", refusal)
	}
	other := toolexec.DaemonReportedFailure("worktree.create", "fatal: invalid reference: main")
	if workspacecreate.IsRecoveryRefusal(other) {
		t.Fatalf("%v is recognised as a recovery refusal", other)
	}
	if workspacecreate.IsRecoveryRefusal(errors.New(toolexec.ErrRecoveryFinalizing.Error())) {
		t.Fatal("bare text is not the router's refusal")
	}
}
