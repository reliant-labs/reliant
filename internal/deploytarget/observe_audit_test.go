package deploytarget

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The observation AUDIT: every registered provider must either observe
// for real, or DECLARE that it cannot and say why.
//
// # Why this test exists rather than a per-provider test each
//
// Per-provider tests are already here and they are good at what they do:
// they pin each observer's verdicts against real wire shapes. What none
// of them can catch is the thing most likely to actually happen — a
// SEVENTH provider gets added, nobody writes an observer or a test for
// it, and every existing test still passes. The board reads clean for an
// environment forge measured part of, which is the precise failure the
// Observe verb was introduced to make impossible.
//
// So this test derives its subject list from the REGISTRY (Registry.IDs)
// rather than restating it. A provider that reaches NewRegistry reaches
// this test, with no second place to remember to update. That property is
// itself asserted below, because a "comprehensive" audit that silently
// enumerates nothing is worse than no audit at all.
//
// # What counts as passing
//
// Exactly two outcomes are acceptable for a provider, and BOTH are
// checked in full — a provider cannot pass by being half of each:
//
//  1. It observes. It returns no ErrObservationUnsupported, and it names
//     every service it was handed. (What the verdicts should be for given
//     inputs is the per-provider tests' job; this asserts only that the
//     provider engaged with the question at all.)
//  2. It declares. It returns ErrObservationUnsupported carrying a
//     non-empty, non-placeholder Reason, AND an Observed whose every item
//     reads HealthUnknown with a detail — so a caller that ignores the
//     error still cannot read green.
//
// A provider that returns a bare nil error and an EMPTY report — the
// natural shape of a stub someone added to satisfy the compiler — matches
// neither, and fails.

// auditSubject is one provider under audit, with a group shaped so the
// provider has something to speak about. The group matters: handing every
// provider an empty ServiceGroup would let each one return an empty
// report, which passes "no item is healthy" vacuously.
type auditSubject struct {
	id    string
	group ServiceGroup
}

// auditGroupFor builds a populated group for a provider id.
//
// It returns ok=false for an id it does not know, and the test FAILS on
// that rather than skipping it. That is the enforcement mechanism: a new
// provider added to NewRegistry with no entry here fails the audit
// immediately, at which point the author must decide what a populated
// group for it looks like — which is the moment they also discover
// whether it can observe. Skipping unknown ids would turn this whole test
// into the silent-green board it exists to prevent.
func auditGroupFor(id string) (ServiceGroup, bool) {
	switch id {
	case "k8s-cluster":
		return ServiceGroup{
			Env:       "prod",
			Cluster:   "gke_example_prod",
			Namespace: "app-prod",
			Services: []ResolvedService{
				{Name: "api", K8sCluster: &K8sClusterSpec{Replicas: 1}},
			},
		}, true
	case "compose":
		// A compose file that CANNOT exist, so the observation resolves
		// to a deterministic unknown instead of depending on whatever
		// docker-compose.yml and containers happen to be on the machine
		// running the tests. The audit asserts the contract, not the
		// verdict; the verdicts are pinned against canned `compose ps`
		// output in TestComposeObserve_* with an injected runner.
		return ServiceGroup{
			Env: "dev",
			Services: []ResolvedService{
				{Name: "api", Compose: &ComposeSpec{ComposeFile: "forge-audit-no-such-compose-file.yml"}},
			},
		}, true
	case "host-infra":
		// Port 0 binds nothing, so the port probe cannot adopt a stray
		// postgres that happens to be on 5432 — a shared dev box usually
		// has one, and the audit must not depend on it.
		return ServiceGroup{
			Env: "dev",
			Services: []ResolvedService{
				{Name: "postgres", HostInfra: &HostInfraSpec{Engine: "postgres", Port: 0}},
			},
		}, true
	case "firebase":
		return ServiceGroup{
			Env:       "prod",
			Frontends: []FirebaseFrontend{{Name: "web"}},
		}, true
	case "static-site":
		return ServiceGroup{
			Env:         "prod",
			StaticSites: []StaticSiteFrontend{{Name: "web"}},
		}, true
	case HostedProviderID:
		// The zero-value provider has no client, so it must report unknown
		// with a reason rather than inventing a verdict.
		return ServiceGroup{
			Env:      "prod",
			Hosted:   &HostedTarget{Endpoint: "https://cp.example", PushBase: "ghcr.io/acme"},
			Services: []ResolvedService{{Name: "api", Hosted: &HostedWorkload{Tier: HostedTierWorkload}}},
		}, true
	default:
		return ServiceGroup{}, false
	}
}

// auditSubjects enumerates the canonical registry. Note that it reads
// Registry.IDs rather than listing providers — see the file comment.
func auditSubjects(t *testing.T) []auditSubject {
	t.Helper()
	ids := NewRegistry().IDs()
	if len(ids) == 0 {
		t.Fatal("NewRegistry registered no providers; the audit would enumerate nothing and pass vacuously")
	}
	out := make([]auditSubject, 0, len(ids))
	for _, id := range ids {
		group, ok := auditGroupFor(id)
		if !ok {
			t.Fatalf("provider %q is registered but the observation audit has no group for it.\n"+
				"This is the audit working, not a broken test: a new deploy target must either\n"+
				"implement Observe or return unsupported with a permanent reason, and it needs an\n"+
				"entry in auditGroupFor so this test can hold it to that. Add one.", id)
		}
		out = append(out, auditSubject{id: id, group: group})
	}
	return out
}

// TestEveryRegisteredProviderObservesOrDeclares is the audit.
func TestEveryRegisteredProviderObservesOrDeclares(t *testing.T) {
	if testing.Short() {
		t.Skip("runs Observe on every registered deploy provider, which probes real host tools; runs in task test")
	}
	reg := NewRegistry()
	for _, subject := range auditSubjects(t) {
		t.Run(subject.id, func(t *testing.T) {
			p := reg.Lookup(subject.id)
			if p == nil {
				t.Fatalf("Registry.IDs returned %q but Lookup does not resolve it", subject.id)
			}

			auditProvider(t, p, subject.group)
		})
	}
}

// auditProvider is THE AUDIT — the whole rule, in one function.
//
// It takes a *testing.T rather than returning findings so that failures
// name the exact assertion, and so the sabotage test can run the REAL
// audit against a deliberately broken provider by passing it a throwaway
// T and checking that it failed. That is the point of factoring it out:
// a sabotage test that re-implemented these checks would prove its own
// copy is load-bearing and say nothing about the audit.
func auditProvider(t *testing.T, p Provider, group ServiceGroup) {
	t.Helper()
	id := p.Name()
	obs, err := p.Observe(context.Background(), group)

	// Attribution holds either way. An unattributed report cannot be
	// joined back to the tier it came from in a merged multi-group board.
	if obs.ProviderID != id {
		t.Errorf("Observed.ProviderID = %q, want %q", obs.ProviderID, id)
	}
	if obs.ObservedAt.IsZero() {
		t.Errorf("Observed.ObservedAt is zero; a caller cannot tell how stale this observation is")
	}

	// Every declared name must appear, in BOTH outcomes. This is what
	// stops a provider passing by returning nothing at all.
	want := observableNames(group)
	if len(want) == 0 {
		t.Fatalf("audit group for %q declares no observable names, so every assertion "+
			"below is vacuous; give it at least one service or frontend", id)
	}
	got := map[string]bool{}
	for _, item := range obs.Items {
		got[item.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("%s: declared %q is absent from the report; a provider must speak for "+
				"everything it was handed, even if only to say 'unknown'", id, name)
		}
	}

	if errors.Is(err, ErrObservationUnsupported) {
		assertHonestDeclination(t, id, obs, err)
		return
	}
	// The observing branch. It tolerates a non-nil error that is NOT the
	// sentinel: these providers shell out to kubectl/docker, and in a
	// unit-test environment those are absent or point nowhere. What is
	// NOT tolerated is claiming health nobody measured.
	assertRealObservation(t, id, obs)
}

// assertHonestDeclination checks outcome 2 in full: a reason a human can
// act on, and a report no careless caller can read as green.
func assertHonestDeclination(t *testing.T, id string, obs Observed, err error) {
	t.Helper()

	var use ObservationUnsupportedError
	if !errors.As(err, &use) {
		t.Fatalf("%s returned the sentinel but not an ObservationUnsupportedError; "+
			"a caller cannot learn WHY", id)
	}
	if use.Provider != id {
		t.Errorf("ObservationUnsupportedError.Provider = %q, want %q", use.Provider, id)
	}
	reason := strings.TrimSpace(use.Reason)
	if reason == "" {
		t.Fatalf("%s declined with no reason; an unsupported with no explanation tells a user "+
			"nothing they can act on", id)
	}

	// A declination must be PERMANENT and say so. "Not implemented yet"
	// is the shape this rule exists to keep out of the codebase: it
	// reports the same unknown as a structural limit while implying
	// someone will fix it, and nothing makes that promise come due.
	for _, banned := range []string{"not implemented yet", "not yet implemented", "not yet built", "todo", "coming soon"} {
		if strings.Contains(strings.ToLower(reason), banned) {
			t.Errorf("%s declines with a TEMPORARY reason (%q contains %q).\n"+
				"A provider that cannot observe must say why it can never observe. If it is "+
				"genuinely implementable, implement it; a gap mislabeled as temporary is one "+
				"nobody is accountable for.", id, reason, banned)
		}
	}

	for _, item := range obs.Items {
		if item.Health != HealthUnknown {
			t.Errorf("%s item %q health = %v; a provider that declined must report unknown, "+
				"never a measured verdict", id, item.Name, item.Health)
		}
		if strings.TrimSpace(item.Detail) == "" {
			t.Errorf("%s item %q has no Detail; an unknown with no reason is indistinguishable "+
				"from a provider that silently failed", id, item.Name)
		}
		if item.Replicas != nil || item.Digest != "" {
			t.Errorf("%s item %q reports measurements (replicas=%v digest=%q) it did not take",
				id, item.Name, item.Replicas, item.Digest)
		}
	}
}

// assertRealObservation checks outcome 1: the provider engaged.
//
// It deliberately does NOT assert a particular verdict. This test runs
// with no cluster, no docker daemon and no host postgres, so the honest
// result for every observing provider here is unknown or absent — and
// pinning which one would be pinning the test environment, not the
// contract. What IS asserted is that the provider cannot claim health it
// did not measure, and that every item carries a reason when it is not
// healthy.
func assertRealObservation(t *testing.T, id string, obs Observed) {
	t.Helper()
	for _, item := range obs.Items {
		if item.Health == HealthHealthy {
			t.Errorf("%s item %q reports HEALTHY with no cluster, docker daemon or host process "+
				"available to measure; a provider must not synthesize health from its own records",
				id, item.Name)
		}
		if item.Health != HealthHealthy && strings.TrimSpace(item.Detail) == "" {
			t.Errorf("%s item %q is %v with no Detail; Detail is required whenever health is "+
				"not healthy", id, item.Name, item.Health)
		}
	}
}

// TestAuditEnumeratesEveryRegisteredProvider is the audit's own control.
//
// The audit's value rests entirely on one property: that its subject list
// is DERIVED from the registry rather than restated. If Registry.IDs ever
// stopped reflecting what NewRegistry registers — or if someone
// "simplified" auditSubjects into a literal — the audit would keep
// passing while covering less, which is the silent-green failure in its
// purest form.
//
// So this asserts the derivation directly: every provider NewRegistry can
// look up is enumerated, and every enumerated id resolves.
func TestAuditEnumeratesEveryRegisteredProvider(t *testing.T) {
	reg := NewRegistry()
	ids := reg.IDs()

	enumerated := map[string]bool{}
	for _, id := range ids {
		enumerated[id] = true
		if reg.Lookup(id) == nil {
			t.Errorf("Registry.IDs returned %q but Lookup does not resolve it", id)
		}
	}

	// Every provider NewRegistry constructs, by its own Name(). Listed
	// here on purpose — this is the ONE place a literal list is correct,
	// because its whole job is to disagree with the registry if the
	// registry's enumeration ever goes wrong.
	for _, p := range []Provider{
		K8sClusterProvider{}, ComposeProvider{},
		HostInfraProvider{}, FirebaseProvider{}, StaticSiteProvider{},
		HostedProvider{},
	} {
		if !enumerated[p.Name()] {
			t.Errorf("provider %q is registered by NewRegistry but Registry.IDs did not "+
				"enumerate it; the audit would silently skip it", p.Name())
		}
	}
}

// TestAuditRejectsAProviderWithNoObserver is the SABOTAGE, kept as a
// permanent test rather than run once by hand.
//
// It registers a provider that satisfies the Provider interface with a
// stub Observe — the exact shape someone produces when the compiler
// demands a method and they have nothing to put in it: no error, no
// items, nothing measured. The audit's assertions are then run against
// it, and must FAIL.
//
// Without this, the audit is a test that has only ever been shown to pass.
// This is what demonstrates it is load-bearing.
func TestAuditRejectsAProviderWithNoObserver(t *testing.T) {
	group := ServiceGroup{
		Env: "prod",
		Services: []ResolvedService{
			{Name: "api"},
			{Name: "worker"},
		},
	}

	// Each of these is a DIFFERENT way to fail the rule, and each is
	// caught by a different assertion in auditProvider. Running all three
	// proves no single check is carrying the whole audit.
	sabotage := []struct {
		name string
		p    Provider
		// why names the specific assertion this case must trip, so a
		// failure here says which control stopped being load-bearing.
		why string
	}{
		{
			name: "silent stub",
			p:    stubProvider{},
			why:  "names none of the services it was handed (the report-every-name check)",
		},
		{
			name: "dishonest green",
			p:    greenStubProvider{},
			why:  "claims HealthHealthy with nothing measured (the no-synthesized-health check)",
		},
		{
			name: "temporary reason",
			p:    temporaryStubProvider{},
			why:  "declines with \"not implemented yet\" (the permanent-reason check)",
		},
		{
			name: "green despite declining",
			p:    contradictoryStubProvider{},
			why:  "returns the sentinel AND items that read healthy (the careless-caller check)",
		},
	}

	for _, tc := range sabotage {
		t.Run(tc.name, func(t *testing.T) {
			// Run THE REAL AUDIT against the broken provider, with a
			// throwaway T to catch its failures. Re-implementing the
			// checks here instead would prove only that the copy works.
			probe := &testing.T{}
			auditProvider(probe, tc.p, group)
			if !probe.Failed() {
				t.Errorf("the audit ACCEPTED %q, which %s.\n"+
					"A provider that does not observe must not be able to ship; this control "+
					"is no longer load-bearing.", tc.p.Name(), tc.why)
			}
		})
	}
}

// TestAuditAcceptsAnHonestProvider is the other half of the sabotage,
// and it is not optional.
//
// A test asserting "the audit fails on broken input" is satisfied by an
// audit that fails on EVERYTHING — which would pass the four cases above
// while telling us nothing. So: a provider that genuinely declines, in
// the shape the rule prescribes, must PASS the same audit unchanged.
func TestAuditAcceptsAnHonestProvider(t *testing.T) {
	group := ServiceGroup{
		Env:      "prod",
		Services: []ResolvedService{{Name: "api"}},
	}
	probe := &testing.T{}
	auditProvider(probe, honestStubProvider{}, group)
	if probe.Failed() {
		t.Error("the audit REJECTED a provider that declines correctly with a permanent reason; " +
			"an audit that fails on everything cannot distinguish a real observer from a stub")
	}
}

// stubProvider is the compiler-satisfying no-op: Observe returns an empty
// report and no error, measuring nothing and naming nobody.
type stubProvider struct{}

func (stubProvider) Name() string                               { return "stub" }
func (stubProvider) Deploy(context.Context, ServiceGroup) error { return nil }
func (stubProvider) Observe(context.Context, ServiceGroup) (Observed, error) {
	return Observed{}, nil
}

// greenStubProvider names everything and claims health for all of it,
// without measuring anything.
type greenStubProvider struct{}

func (greenStubProvider) Name() string                               { return "green-stub" }
func (greenStubProvider) Deploy(context.Context, ServiceGroup) error { return nil }
func (greenStubProvider) Observe(_ context.Context, group ServiceGroup) (Observed, error) {
	obs := Observed{ProviderID: "green-stub"}
	for _, name := range observableNames(group) {
		obs.Items = append(obs.Items, ObservedItem{Name: name, Health: HealthHealthy})
	}
	return obs, nil
}

// temporaryStubProvider declines correctly in shape, with a reason that
// describes a gap rather than a structural limit.
type temporaryStubProvider struct{}

func (temporaryStubProvider) Name() string                               { return "temporary-stub" }
func (temporaryStubProvider) Deploy(context.Context, ServiceGroup) error { return nil }
func (temporaryStubProvider) Observe(_ context.Context, group ServiceGroup) (Observed, error) {
	return unsupported("temporary-stub",
		"observing this target is not implemented yet", observableNames(group))
}

// contradictoryStubProvider returns the sentinel — so a careful caller
// sees "unsupported" — while its Observed reads healthy. This is the
// precise shape the belt-and-braces rule in observe.go exists to stop: a
// caller that logs the value without checking the error reads green for
// something nobody measured.
type contradictoryStubProvider struct{}

func (contradictoryStubProvider) Name() string                               { return "contradictory-stub" }
func (contradictoryStubProvider) Deploy(context.Context, ServiceGroup) error { return nil }
func (contradictoryStubProvider) Observe(_ context.Context, group ServiceGroup) (Observed, error) {
	obs, err := unsupported("contradictory-stub",
		"this target is opaque to forge and always will be", observableNames(group))
	for i := range obs.Items {
		obs.Items[i].Health = HealthHealthy
	}
	return obs, err
}

// honestStubProvider declines exactly as the rule prescribes: the
// sentinel, a permanent reason, every name present, every item unknown
// with a detail. It must PASS the audit — see
// TestAuditAcceptsAnHonestProvider.
type honestStubProvider struct{}

func (honestStubProvider) Name() string                               { return "honest-stub" }
func (honestStubProvider) Deploy(context.Context, ServiceGroup) error { return nil }
func (honestStubProvider) Observe(_ context.Context, group ServiceGroup) (Observed, error) {
	return unsupported("honest-stub",
		"this target is deployed through an opaque third-party API that reports no state back, "+
			"so forge has nothing to read and no future version will",
		observableNames(group))
}
