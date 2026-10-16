// Copyright (c) 2025 Reliant Labs

package forge

import (
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// TestForgeRPCDeadlinesCoverEveryProcedure pins the request deadline the
// api-server grants each ForgeService method.
//
// The regression: no ForgeService method had an entry, so every one inherited
// the interceptor's 10s default and was cancelled inside its own 15–110s
// budget. A cancelled cluster read comes back as an empty UNREACHABLE reply,
// which the web classified as "not a forge project" — the owner saw
// "control-plane has no forge.yaml" for a project that has one. Walking the
// service DESCRIPTOR rather than a hand list means a method added later fails
// here until it is given a budget.
func TestForgeRPCDeadlinesCoverEveryProcedure(t *testing.T) {
	deadlines := ForgeRPCDeadlines()
	service := reliantv1.File_reliant_v1_forge_proto.Services().ByName("ForgeService")
	if service == nil {
		t.Fatal("ForgeService descriptor not found")
	}

	const interceptorDefault = 10 * time.Second
	methods := service.Methods()
	for i := 0; i < methods.Len(); i++ {
		procedure := "/" + string(service.FullName()) + "/" + string(methods.Get(i).Name())
		deadline, ok := deadlines[procedure]
		if !ok {
			t.Errorf("%s has no request deadline; it would inherit the %s interceptor default", procedure, interceptorDefault)
			continue
		}
		if deadline <= interceptorDefault {
			t.Errorf("%s deadline %s does not exceed the %s default", procedure, deadline, interceptorDefault)
		}
	}
}

// forgeGatewayRouteTimeout is the request timeout prod's gateway grants
// /reliant.v1.ForgeService/* (control-plane deploy/kcl/prod/ingress.k,
// _forge_routes). The catch-all route keeps Envoy's 15s default; every forge
// deadline is above that, and prod cut GetTopology and GetEnvStatus at 15s with
// a bare 504 until the route existed (2026-10-15). It is 120s — the daemon's
// forgeInvocationTimeout — and a deadline past it would be cut at the gateway
// instead of answering with its own classified result.
const forgeGatewayRouteTimeout = 120 * time.Second

func TestForgeRPCDeadlinesFitTheGatewayRoute(t *testing.T) {
	for procedure, deadline := range ForgeRPCDeadlines() {
		if deadline >= forgeGatewayRouteTimeout {
			t.Errorf("%s deadline %s reaches the gateway's %s route timeout; the gateway would answer 504 before the api-server answers",
				procedure, deadline, forgeGatewayRouteTimeout)
		}
	}
}

// The budgets that both daemon tabs of the env page wait on, against what the
// commands were measured to take on control-plane (4 envs, one hosted, loaded
// cloud machine, 2026-10-16): `env status --json` 26–30s, `env status prod
// --json` 24–51s. A budget under the measurement kills every run — the 15s
// topology budget did.
//
// The floors differ because the commands are bounded differently. Topology
// (no --verify) has no timeout of its own in forge, so ours is the only bound
// and needs clear headroom over the measurement. `env status <env>` is bounded
// by forge's own 60s release-half timeout (defaultEnvStatusReleaseTimeout);
// ours must sit above THAT, so a slow cluster read ends in forge's considered
// UNREACHABLE verdict rather than in our kill.
func TestForgeBudgetsCoverMeasuredRuns(t *testing.T) {
	const forgeOwnEnvStatusTimeout = 60 * time.Second
	cases := map[string]struct {
		budgetMs int32
		floor    time.Duration
	}{
		"topology (env status, every env)": {forgeTopologyTimeoutMs, 30 * time.Second * 3 / 2},
		"env_status (env status <env>)":    {forgeEnvStatusTimeoutMs, forgeOwnEnvStatusTimeout},
	}
	for name, c := range cases {
		budget := time.Duration(c.budgetMs) * time.Millisecond
		if budget <= c.floor {
			t.Errorf("%s: budget %s must exceed %s", name, budget, c.floor)
		}
	}
	if measured := 51 * time.Second; time.Duration(forgeEnvStatusTimeoutMs)*time.Millisecond <= measured {
		t.Errorf("env_status budget is under the slowest measured run (%s)", measured)
	}
	// env_status and env_verify run the same argv; they cannot disagree about
	// how long it may take.
	if forgeEnvStatusTimeoutMs != forgeEnvVerifyTimeoutMs {
		t.Errorf("env_status budget %d != env_verify budget %d for the same `env status <env>` argv",
			forgeEnvStatusTimeoutMs, forgeEnvVerifyTimeoutMs)
	}
}

// The request must outlive the dispatch, or the handler never gets to return
// its own classified timeout.
func TestForgeRPCDeadlinesOutliveDispatchBudgets(t *testing.T) {
	deadlines := ForgeRPCDeadlines()
	cases := map[string]int32{
		"/reliant.v1.ForgeService/GetEnvStatus": forgeEnvStatusTimeoutMs,
		"/reliant.v1.ForgeService/GetEnvShape":  forgeEnvStatusTimeoutMs,
		"/reliant.v1.ForgeService/GetTopology":  forgeTopologyVerifyTimeoutMs,
		"/reliant.v1.ForgeService/DiffEnv":      forgeDeployPlanTimeoutMs,
		"/reliant.v1.ForgeService/GetAudit":     forgeAuditTimeoutMs,
	}
	for procedure, budgetMs := range cases {
		budget := time.Duration(budgetMs) * time.Millisecond
		if deadlines[procedure] <= budget {
			t.Errorf("%s: request deadline %s must exceed dispatch budget %s", procedure, deadlines[procedure], budget)
		}
	}
}
