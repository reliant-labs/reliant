// Copyright (c) 2025 Reliant Labs

package services

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
