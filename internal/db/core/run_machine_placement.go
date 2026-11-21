// Copyright (c) 2025 Reliant Labs
package core

import (
	"errors"
	"time"
)

// ErrRunMachinePlacementNotFound is a run (or node) with no recorded
// placement yet.
var ErrRunMachinePlacementNotFound = errors.New("run machine placement not found")

// RunMachinePlacement is where a workflow's `daemon: {pool: …}` or
// labels-only `daemon: {labels: …}` placed one run (Scope "") or one node
// of it (Scope: the node's path). It holds a slot on its member while the
// run's workflow is live (run_machine_placements).
type RunMachinePlacement struct {
	WorkflowID string
	Scope      string
	UserID     string
	ChatID     string
	// MachinePoolID is the pool placed on; "" for a labels placement (an
	// unnamed pool) or a pool deleted since.
	MachinePoolID string
	// Selector is the labels the member was chosen by.
	Selector  map[string]string
	DaemonID  string
	CreatedAt time.Time
}

// RunPlacementChat is what placing a run reads about its chat: whose it is,
// its project, and the pool and member a trigger placed it on ("" when none).
type RunPlacementChat struct {
	ID             string
	UserID         string
	ProjectID      string
	MachinePoolID  string
	ActiveDaemonID string
}
