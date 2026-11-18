// forge-scaffold (USER-OWNED, ADDITIVELY RECONCILED): forge writes this file
// ONCE (write-if-absent) and NEVER re-renders it wholesale — the OperatorEntry
// shape and RunOperators' body are per-app policy and stay yours. What forge
// DOES keep in sync is the projection: a worker/operator discovered under
// internal/{workers,operators}/ that has no Worker<X>() / Operator<X>()
// accessor here gets one appended, and the matching All* aggregate gains its
// entry — because the per-component subcommand forge scaffolds
// (cmd/<bin>/cmd/workers/<name>.go) calls that accessor directly. Rewrite an
// All* body into anything other than the plain accessor list and forge leaves
// it exactly as you wrote it. `forge project disown internal/app/lifecycle.go`
// stops even the additive pass.
// Source: internal/{workers,operators}/<x>/ package dirs.
//
// The SUPERVISED-component surface. Where mounts_services_gen.go is the HTTP-mount
// surface (which Connect services to mount), this file is the workers and
// operators serverkit runs over the constructed *Components.
//
// Every worker/operator has ONE typed 1:1 accessor (Worker<Field>() /
// Operator<Name>()) that welds its stable label to its typed field — the label
// appears exactly ONCE, as a log annotation, never as a selection key. A
// command composes its OWN supervised set by naming those accessors DIRECTLY in
// its ServeSpec; there is no string-keyed re-selection anywhere on the run path.
//
// THIN DATA + DISPATCH: all the uniform machinery (the WrapWorker runtime
// type-switch, the operator/controller-manager bridge) lives in
// forge/pkg/lifecyclekit. This file is per-app DATA only — one accessor per
// component plus the aggregate All* lists — dispatching into the lib. To
// customize (a late-bound worker cross-edge), `forge project disown
// internal/app/lifecycle.go` and own the bytes.
package app

import (
	"context"
	"log/slog"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/reliant-labs/forge/pkg/lifecyclekit"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

// OperatorEntry is one operator's supervised wiring: its stable label plus the
// two controller-manager hooks (CRD scheme install + controller setup). It
// UNIFIES the AddOperator marker set and the RunOperators controller set into a
// single value, so the markers and the controllers can never disagree. A
// command names its typed Operator<Name>() accessor directly; Serve derives BOTH
// the serverkit marker (via Marker) AND the lifecyclekit controller row
// (AddToScheme + SetupWithManager) from the same entry.
type OperatorEntry struct {
	Name             string
	AddToScheme      func(*runtime.Scheme) error
	SetupWithManager func(ctrl.Manager) error
}

// operatorMarker is the serverkit.Operator (Name-only) view of an OperatorEntry
// — what srv.AddOperator records so serverkit knows an operator is supervised
// and starts the controller manager.
type operatorMarker struct{ name string }

func (m operatorMarker) Name() string { return m.name }

// Marker returns the serverkit.Operator view of this entry (Name only).
func (e OperatorEntry) Marker() serverkit.Operator { return operatorMarker{name: e.Name} }

// WorkerTicker adapts the constructed ticker worker. Each accessor
// welds the worker's stable label to its typed field via lifecyclekit.WrapWorker
// (a runtime type-switch that gives ctx-aware workers per-worker
// cancel-on-shutdown and leaves legacy Start/Stop workers on the legacy path).
func (c *Components) WorkerTicker() serverkit.Worker {
	return lifecyclekit.WrapWorker("ticker", c.Ticker)
}

// AllWorkers returns every worker, in declaration order. The all-services
// `server` command names it directly (ServeSpec.Workers).
func (c *Components) AllWorkers() []serverkit.Worker {
	return []serverkit.Worker{
		c.WorkerTicker(),
	}
}

// AllOperators returns every operator. The all-services `server` command names
// it directly (ServeSpec.Operators).
func (c *Components) AllOperators() []OperatorEntry {
	_ = c
	return nil
}

// HasOperators reports whether the project has any operators configured.
func HasOperators(c *Components) bool { return len(c.AllOperators()) > 0 }

// RunOperators starts the controller manager with one row per supplied operator
// entry. Manager behavior (kubeconfig resolution with graceful no-cluster
// degrade, leader election, scheme / controller registration) lives in
// lifecyclekit.Run (over operatorkit). Blocks until ctx cancels — the caller
// (serverkit, via the Server.RunOperators closure Serve wires from the same
// entries) runs it in a goroutine.
//
// Options is the WHOLE input: operatorkit reads no environment variable, so
// anything the manager does differently in one deployment is a value passed
// here. A lease name that varies per environment, a lease namespace (needed
// to run the manager from a host process, outside the cluster), or non-default
// leader-election timings are config fields — declare them in
// proto/config/v1/config.proto, thread cfg into this function, and set the
// matching Options field.
func RunOperators(ctx context.Context, logger *slog.Logger, healthProbeAddr string, ops []OperatorEntry) error {
	controllers := make([]lifecyclekit.Controller, 0, len(ops))
	for _, op := range ops {
		controllers = append(controllers, lifecyclekit.Controller{
			Name:             op.Name,
			AddToScheme:      op.AddToScheme,
			SetupWithManager: op.SetupWithManager,
		})
	}
	return lifecyclekit.Run(ctx, logger, lifecyclekit.Options{
		LeaderElectionID:       "app-leader",
		HealthProbeBindAddress: healthProbeAddr,
	}, controllers)
}
