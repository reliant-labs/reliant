package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// direct is the rendered direct delegation of a method with no parameters
// or one ctx parameter.
func direct(method, params, results, call string) string {
	return "func (o *forgeMiddlewareService) " + method + "(" + params + ")" + results + " {\n\treturn o.inner." + call + "\n}"
}

// TestObservedDecorator_LifecycleMethodsDelegateDirectly is the regression for
// a long-running component (Run(ctx) error, supervised by the composition
// graph) whose decorator routed Run through the chain: one span and one log
// line lasting the whole process, unless the author knew to write
// `// forge:no-observe`. Lifecycle methods now delegate directly by default,
// carry no Op and no method id, while a business method beside them is still
// observed.
func TestObservedDecorator_LifecycleMethodsDelegateDirectly(t *testing.T) {
	dir := writeContract(t, `package assigner

import "context"

type Service interface {
	Run(ctx context.Context) error
	Drain(ctx context.Context) error
	Ready(ctx context.Context) error
	Close() error
	Assign(ctx context.Context, job string) error
}
`)
	// A module, so the decorator also attaches WithMethodIDs.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/assigner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := genDecorator(t, dir, "Service", "assigner")

	for _, want := range []string{
		direct("Run", "ctx context.Context", " error", "Run(ctx)"),
		direct("Drain", "ctx context.Context", " error", "Drain(ctx)"),
		direct("Ready", "ctx context.Context", " error", "Ready(ctx)"),
		direct("Close", "", " error", "Close()"),
		"// Run is part of the component's lifecycle, not a unit of work, so it",
		`observe.RunOp(o.chain, ctx, forgeMiddlewareServiceOps.Assign,`,
		`"assigner.Assign"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("decorator missing %q\n---\n%s", want, got)
		}
	}
	for _, name := range []string{"Run", "Drain", "Ready", "Close"} {
		for _, banned := range []string{"Ops." + name, `"assigner.` + name + `"`} {
			if strings.Contains(got, banned) {
				t.Errorf("lifecycle method %s must not be routed through the chain (found %q):\n%s", name, banned, got)
			}
		}
	}
}

// TestObservedDecorator_LifecycleNameWithBusinessSignatureStaysObserved: the
// exemption needs the lifecycle SIGNATURE as well as the name, so a business
// method that happens to be called Run, Close or Ready is still observed.
func TestObservedDecorator_LifecycleNameWithBusinessSignatureStaysObserved(t *testing.T) {
	dir := writeContract(t, `package jobs

import "context"

type Service interface {
	Run(ctx context.Context, job Job) (Result, error)
	Close(ctx context.Context, id string) error
	Ready() bool
	Drain(ctx context.Context) (int, error)
}

type Job struct{ ID string }
type Result struct{ OK bool }
`)
	got := genDecorator(t, dir, "Service", "jobs")
	for _, name := range []string{"Run", "Close", "Ready", "Drain"} {
		if !strings.Contains(got, "forgeMiddlewareServiceOps."+name+",") {
			t.Errorf("%s has a business signature and must route through the chain:\n%s", name, got)
		}
	}
	if strings.Contains(got, "lifecycle") {
		t.Errorf("no method here is a lifecycle method:\n%s", got)
	}
}

// TestObservedDecorator_ObserveMarkerOptsLifecycleBackIn: `// forge:observe`
// on a lifecycle method routes it through the chain again, and
// `// forge:no-observe` still wins over it.
func TestObservedDecorator_ObserveMarkerOptsLifecycleBackIn(t *testing.T) {
	dir := writeContract(t, `package probe

import "context"

type Service interface {
	// Ready is slow enough to want a span.
	// forge:observe
	Ready(ctx context.Context) error
	Run(ctx context.Context) error //forge:observe
	// forge:observe
	// forge:no-observe
	Close() error
}
`)
	got := genDecorator(t, dir, "Service", "probe")
	for _, want := range []string{
		`observe.RunOp(o.chain, ctx, forgeMiddlewareServiceOps.Ready,`,
		`observe.RunOp(o.chain, ctx, forgeMiddlewareServiceOps.Run,`,
		direct("Close", "", " error", "Close()"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("decorator missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "Ops.Close") {
		t.Errorf("forge:no-observe must win over forge:observe:\n%s", got)
	}
}

// TestObservedDecorator_WorkerLifecycle: a contract shaped as a
// serverkit.Worker (Name() string, Start, Stop) lives in Start and stops in
// Stop, both exempt; its Run is one tick Start calls (a cron worker), which
// is real work and stays observed — as codegen.lifecycleOf supervises Start,
// not Run.
func TestObservedDecorator_WorkerLifecycle(t *testing.T) {
	dir := writeContract(t, `package sweeper

import "context"

type Service interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Run(ctx context.Context) error
}
`)
	got := genDecorator(t, dir, "Service", "sweeper")
	for _, want := range []string{
		direct("Start", "ctx context.Context", " error", "Start(ctx)"),
		direct("Stop", "ctx context.Context", " error", "Stop(ctx)"),
		`observe.RunOp(o.chain, ctx, forgeMiddlewareServiceOps.Run,`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("decorator missing %q\n---\n%s", want, got)
		}
	}

	// Without Name() string it is not a worker: Start and Stop are ordinary
	// methods, and Run is the lifecycle.
	dir = writeContract(t, `package timer

import "context"

type Service interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Run(ctx context.Context) error
}
`)
	got = genDecorator(t, dir, "Service", "timer")
	for _, want := range []string{
		`observe.RunOp(o.chain, ctx, forgeMiddlewareServiceOps.Start,`,
		`observe.RunOp(o.chain, ctx, forgeMiddlewareServiceOps.Stop,`,
		direct("Run", "ctx context.Context", " error", "Run(ctx)"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("decorator missing %q\n---\n%s", want, got)
		}
	}
}
