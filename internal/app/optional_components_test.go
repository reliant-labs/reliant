// Copyright (c) 2025 Reliant Labs

package app

import (
	"testing"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

// TestOptionalWorkerComponentsAreNilSelfHosted pins the optional
// components' "not deployed": each is an untyped nil interface on a
// self-hosted worker, never a nil client wrapped in one, so the graph treats
// it as absent and the tools report that they are unavailable.
func TestOptionalWorkerComponentsAreNilSelfHosted(t *testing.T) {
	selfHosted := &configv1.ControlPlaneConfig{}

	moves, err := NewOrgMoves(OrgMovesDeps{ControlPlane: selfHosted})
	if err != nil || moves != nil {
		t.Fatalf("NewOrgMoves self-hosted = %v, %v; want nil, nil", moves, err)
	}
	if moves, _ := NewOrgMoves(OrgMovesDeps{Router: &WorkerDaemonRouter{}, Triggers: &WorkerTrigger{}, ControlPlane: selfHosted}); moves != nil {
		t.Fatalf("NewOrgMoves without a control plane client = %v; want nil", moves)
	}
	if c := NewChangeController(ChangeControllerDeps{ControlPlane: selfHosted}); c != nil {
		t.Fatalf("NewChangeController self-hosted = %v; want nil", c)
	}
	if c := newMachineController(nil, nil, nil); c != nil {
		t.Fatalf("newMachineController without a router = %v; want nil", c)
	}
	if c := newMachineController(nil, &WorkerDaemonRouter{}, nil); c != nil {
		t.Fatalf("newMachineController without a control plane = %v; want nil", c)
	}
}
