package testkit_test

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// fakeDeps stands in for a generated <svc>.Deps struct: a couple of
// collaborator fields, one of which a test wants to swap for a mock.
type fakeDeps struct {
	Name  string
	Mock  any
	Count int
}

// fakeService is the assembled thing the factory returns.
type fakeService struct {
	deps fakeDeps
}

func TestScenarioBuilder_ZeroValueDefaults(t *testing.T) {
	t.Parallel()
	svc := testkit.NewScenario(func(t *testing.T, d fakeDeps) *fakeService {
		return &fakeService{deps: d}
	}).Build(t)

	if svc.deps != (fakeDeps{}) {
		t.Fatalf("expected zero-value deps, got %+v", svc.deps)
	}
}

func TestScenarioBuilder_WithOverridesOneField(t *testing.T) {
	t.Parallel()
	mock := struct{ id int }{id: 7}

	svc := testkit.NewScenario(func(t *testing.T, d fakeDeps) *fakeService {
		return &fakeService{deps: d}
	}).
		With(func(d *fakeDeps) { d.Mock = mock }).
		Build(t)

	if svc.deps.Mock != any(mock) {
		t.Fatalf("mock collaborator not injected: %+v", svc.deps)
	}
	if svc.deps.Name != "" || svc.deps.Count != 0 {
		t.Fatalf("untouched fields should stay zero: %+v", svc.deps)
	}
}

func TestScenarioBuilder_OverridesApplyInOrder(t *testing.T) {
	t.Parallel()
	svc := testkit.NewScenario(func(t *testing.T, d fakeDeps) fakeDeps {
		return d
	}).
		With(func(d *fakeDeps) { d.Count = 1 }).
		With(func(d *fakeDeps) { d.Count = 2 }).
		Build(t)

	if svc.Count != 2 {
		t.Fatalf("later override should win: got %d, want 2", svc.Count)
	}
}

func TestScenarioBuilder_WithDepsThenTweak(t *testing.T) {
	t.Parallel()
	base := fakeDeps{Name: "base", Count: 5}

	got := testkit.NewScenario(func(t *testing.T, d fakeDeps) fakeDeps {
		return d
	}).
		WithDeps(base).
		With(func(d *fakeDeps) { d.Count = 9 }).
		Build(t)

	if got.Name != "base" {
		t.Fatalf("WithDeps base field lost: %+v", got)
	}
	if got.Count != 9 {
		t.Fatalf("tweak after WithDeps should win: %+v", got)
	}
}

func TestScenarioBuilder_NilMutateIsNoOp(t *testing.T) {
	t.Parallel()
	got := testkit.NewScenario(func(t *testing.T, d fakeDeps) fakeDeps { return d }).
		With(nil).
		With(func(d *fakeDeps) { d.Name = "x" }).
		Build(t)
	if got.Name != "x" {
		t.Fatalf("nil mutate should be skipped, real one applied: %+v", got)
	}
}
