package testkit

import (
	"testing"
)

// ScenarioBuilder assembles a service-under-test together with a typed
// override value in a few lines, so a test (or an LLM exploring the code)
// can stand up a real instance with one collaborator swapped for a mock
// without re-deriving the per-service factory boilerplate.
//
// The type parameter D is the service's dependency value — almost always
// the generated `<svc>.Deps` struct that the project's
// pkg/app/testing.go factories already accept via With<Svc>Deps(deps).
// S is the assembled thing Build returns (a *Service, a Service interface,
// or an (server, client) pair via a struct).
//
// # Why a builder when the factories already exist
//
// The generated NewTest<Svc>(t, opts...) factories are the composition
// roots: they fill the cross-cutting deps (logger/config), auto-stub
// required collaborators, and call <svc>.New. ScenarioBuilder does NOT
// duplicate that — it composes with it. You hand Build the factory (closed
// over the generated NewTest<Svc> + its With<Svc>Deps option) as the
// assemble func; the builder's only job is to accumulate typed overrides
// onto a zero-value D and apply them in order before assembly. The result
// is the "stand up a service with one mocked collaborator in ~3 lines"
// ergonomic the redesign note (§7g) calls for:
//
//	svc := testkit.NewScenario(func(t *testing.T, d user.Deps) user.Service {
//	    return app.NewTestSvcUser(t, app.WithSvcUserDeps(d))
//	}).
//	    With(func(d *user.Deps) { d.Audit = mockAudit }). // swap one collaborator
//	    Build(t)
//
// Everything not overridden falls through to the generated factory's
// defaults (discard logger, auto-stubbed collaborators), so the mock
// above is the only thing the test states.
//
// # Composing with a real DB
//
// Build receives *testing.T, so the assemble closure can also reach for
// NewMigratedPostgresDB / LoadFixture to give the service a real seeded
// repository. See the example tests in the downstream apps.
type ScenarioBuilder[D any, S any] struct {
	assemble  func(t *testing.T, deps D) S
	overrides []func(*D)
}

// NewScenario starts a builder from the zero value of D. assemble is the
// adapter that turns an effective D into the assembled instance — typically
// a one-line closure over the generated NewTest<Svc> factory and its
// With<Svc>Deps option (see the type doc). assemble must not be nil; Build
// fails the test if it is.
func NewScenario[D any, S any](assemble func(t *testing.T, deps D) S) *ScenarioBuilder[D, S] {
	return &ScenarioBuilder[D, S]{assemble: assemble}
}

// With registers a typed mutation against the dependency value. Mutations
// apply in registration order at Build time, so a later With can override
// an earlier one. This is the functional-options seam for injecting a mock
// collaborator, a real repository, or any other field on D:
//
//	b.With(func(d *billing.Deps) { d.Users = mockUsers })
//
// Returns the builder for chaining.
func (b *ScenarioBuilder[D, S]) With(mutate func(*D)) *ScenarioBuilder[D, S] {
	if mutate != nil {
		b.overrides = append(b.overrides, mutate)
	}
	return b
}

// WithDeps replaces the accumulated dependency value wholesale with deps.
// Later With mutations still apply on top, so this is the "start from a
// fully-specified Deps, then tweak one field" entry point. Equivalent to a
// With that assigns *d = deps, but reads more clearly at the call site.
func (b *ScenarioBuilder[D, S]) WithDeps(deps D) *ScenarioBuilder[D, S] {
	b.overrides = append(b.overrides, func(d *D) { *d = deps })
	return b
}

// Build applies every registered override to a zero-value D in order and
// hands the result to the assemble func, returning the assembled instance.
// It marks itself a test helper so failures point at the caller.
func (b *ScenarioBuilder[D, S]) Build(t *testing.T) S {
	t.Helper()
	if b.assemble == nil {
		t.Fatal("testkit: ScenarioBuilder.Build called with nil assemble func (use NewScenario)")
	}
	var deps D
	for _, mutate := range b.overrides {
		mutate(&deps)
	}
	return b.assemble(t, deps)
}
