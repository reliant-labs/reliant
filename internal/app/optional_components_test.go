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

// TestControlPlaneVariantsAreNilSelfHosted pins the api-server's two
// control-plane clients: absent (an untyped nil Client) when self-hosted,
// and the internal-service one also without the secret.
func TestControlPlaneVariantsAreNilSelfHosted(t *testing.T) {
	selfHosted := &configv1.ControlPlaneConfig{}
	caller, err := NewCallerControlPlane(CallerControlPlaneDeps{ControlPlane: selfHosted})
	if err != nil || caller != nil || caller.Client() != nil {
		t.Fatalf("NewCallerControlPlane self-hosted = %v, %v; want nil, nil", caller, err)
	}
	internal, err := NewInternalControlPlane(InternalControlPlaneDeps{ControlPlane: selfHosted})
	if err != nil || internal != nil || internal.Client() != nil {
		t.Fatalf("NewInternalControlPlane self-hosted = %v, %v; want nil, nil", internal, err)
	}
	hosted := &configv1.ControlPlaneConfig{Url: "https://cp.example.test"}
	if caller, _ := NewCallerControlPlane(CallerControlPlaneDeps{ControlPlane: hosted}); caller.Client() == nil {
		t.Fatal("NewCallerControlPlane hosted = nil; want a client")
	}
	if internal, _ := NewInternalControlPlane(InternalControlPlaneDeps{ControlPlane: hosted}); internal.Client() != nil {
		t.Fatal("NewInternalControlPlane without the secret = a client; want nil")
	}
}
