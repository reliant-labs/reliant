package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

// recordingProvider is a deploytarget.Provider that records the groups it
// was asked to deploy and fails the ones whose first service is named in
// failOn. It stands in for compose/host-infra so the dispatch contract can
// be tested without docker or a postgres binary.
type recordingProvider struct {
	id       string
	deployed *[]string
	failOn   map[string]error
}

func (p recordingProvider) Name() string { return p.id }

func (p recordingProvider) Deploy(_ context.Context, group deploytarget.ServiceGroup) error {
	for _, svc := range group.Services {
		*p.deployed = append(*p.deployed, svc.Name)
		if err, bad := p.failOn[svc.Name]; bad {
			return err
		}
	}
	return nil
}

// Observe satisfies the Provider interface. This double stands in for
// compose/host-infra in the DEPLOY dispatch contract, which never
// observes; it declines rather than reporting a health nothing measured.
func (p recordingProvider) Observe(_ context.Context, _ deploytarget.ServiceGroup) (deploytarget.Observed, error) {
	return deploytarget.Observed{ProviderID: p.id}, deploytarget.ObservationUnsupportedError{
		Provider: p.id,
		Reason:   "test double for the deploy dispatch; observation is not part of these tests",
	}
}

// TestDeployInfraGroups_AttemptsEveryGroupAfterAFailure is the regression
// test for the bug where infra bring-up abandoned every remaining group the
// moment one failed.
//
// The lived failure: postgres could not bind its port because another stack
// held it, the loop returned, and the dev IdP — a DIFFERENT group — was
// never started at all. The run then died several steps later reading a PAT
// file the IdP had never got to write, with nothing in that message
// connecting it to a port collision.
//
// Two things are asserted, and both matter. That the later group was still
// attempted (the bug), and that the failure is still REPORTED (the fix must
// not turn a real failure into a silent one).
func TestDeployInfraGroups_AttemptsEveryGroupAfterAFailure(t *testing.T) {
	var deployed []string
	boom := errors.New("bind :5432: address already in use")

	groups := []deploytarget.ServiceGroup{
		{
			ProviderID: "host-infra",
			Services:   []deploytarget.ResolvedService{{Name: "postgres"}},
		},
		{
			ProviderID: "compose",
			Services:   []deploytarget.ResolvedService{{Name: "idp"}},
		},
	}
	providers := map[string]deploytarget.Provider{
		"host-infra": recordingProvider{
			id: "host-infra", deployed: &deployed,
			failOn: map[string]error{"postgres": boom},
		},
		"compose": recordingProvider{id: "compose", deployed: &deployed},
	}

	err := deployInfraGroups(context.Background(), groups, providers)
	if err == nil {
		t.Fatal("expected the postgres failure to be reported, got nil")
	}
	if !errors.Is(err, boom) {
		t.Errorf("failure should wrap the provider's error, got: %v", err)
	}
	// The error names WHICH services the failed group left undeployed —
	// "compose: exit status 1" alone sends the reader nowhere.
	if !strings.Contains(err.Error(), "postgres") {
		t.Errorf("error should name the failing service, got: %v", err)
	}

	// The actual regression: the idp group must have been attempted even
	// though the group before it failed.
	var sawIDP bool
	for _, name := range deployed {
		if name == "idp" {
			sawIDP = true
		}
	}
	if !sawIDP {
		t.Fatalf("a failing group abandoned the ones after it — deployed only %v; "+
			"every infra group must be attempted", deployed)
	}
}

// TestDeployInfraGroups_AggregatesEveryFailure confirms that when more than
// one group fails, the report names all of them. Reporting only the first
// would send someone to fix one problem, re-run, and meet the next.
func TestDeployInfraGroups_AggregatesEveryFailure(t *testing.T) {
	var deployed []string
	groups := []deploytarget.ServiceGroup{
		{ProviderID: "host-infra", Services: []deploytarget.ResolvedService{{Name: "postgres"}}},
		{ProviderID: "compose", Services: []deploytarget.ResolvedService{{Name: "idp"}}},
	}
	providers := map[string]deploytarget.Provider{
		"host-infra": recordingProvider{
			id: "host-infra", deployed: &deployed,
			failOn: map[string]error{"postgres": errors.New("port held")},
		},
		"compose": recordingProvider{
			id: "compose", deployed: &deployed,
			failOn: map[string]error{"idp": errors.New("image pull failed")},
		},
	}

	err := deployInfraGroups(context.Background(), groups, providers)
	if err == nil {
		t.Fatal("expected both failures to be reported, got nil")
	}
	for _, want := range []string{"port held", "image pull failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregated error is missing %q; got: %v", want, err)
		}
	}
}

// TestDeployInfraGroups_SkipsApplicationGroups confirms the infra pre-warm
// touches only infrastructure. A cluster or external group is an
// APPLICATION — the deploy phase owns it, and deploying it here would ship
// the app before it had been built.
func TestDeployInfraGroups_SkipsApplicationGroups(t *testing.T) {
	var deployed []string
	groups := []deploytarget.ServiceGroup{
		{ProviderID: "k8s-cluster", Services: []deploytarget.ResolvedService{{Name: "api"}}},
		{ProviderID: "external", Services: []deploytarget.ResolvedService{{Name: "edge"}}},
		{ProviderID: "host-infra", Services: []deploytarget.ResolvedService{{Name: "postgres"}}},
	}
	providers := map[string]deploytarget.Provider{
		"host-infra": recordingProvider{id: "host-infra", deployed: &deployed},
	}

	if err := deployInfraGroups(context.Background(), groups, providers); err != nil {
		t.Fatalf("deployInfraGroups: %v", err)
	}
	if len(deployed) != 1 || deployed[0] != "postgres" {
		t.Fatalf("only infrastructure should be pre-warmed, got %v", deployed)
	}
}
