package codegen

import (
	"reflect"
	"testing"
)

// mapResolver resolves a Deps field type to a producer FieldName via a
// fixed type->producer map. Conventional/external deps (types absent from
// the map) resolve to "" — no edge.
type mapResolver map[string]string

func (m mapResolver) Resolve(_ BuildComponent, depsType string) string { return m[depsType] }

func comp(name, field string, deps ...DepsField) BuildComponent {
	return BuildComponent{Name: name, FieldName: field, VarName: name, Deps: deps}
}

func dep(name, typ string) DepsField { return DepsField{Name: name, Type: typ} }

func orderFields(p BuildPlan) []string {
	out := make([]string, 0, len(p.Order))
	for _, c := range p.Order {
		out = append(out, c.FieldName)
	}
	return out
}

func TestComputeBuildPlan_TypeTopoOrdersProducerFirst(t *testing.T) {
	// billing.Deps.Users typed user.Service => construct user first,
	// resolved by TYPE not by the field name "Users".
	comps := []BuildComponent{
		comp("billing", "Billing", dep("Users", "user.Service"), dep("Logger", "*slog.Logger")),
		comp("user", "User", dep("Repo", "Repository")),
	}
	resolver := mapResolver{"user.Service": "User"}

	plan := ComputeBuildPlan(comps, resolver)
	if plan.HasCycle() {
		t.Fatalf("unexpected cycle: %v", plan.Cycles)
	}
	got := orderFields(plan)
	want := []string{"User", "Billing"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	// Exactly one edge: Billing -> User via Users field.
	if len(plan.Edges) != 1 {
		t.Fatalf("edges = %v, want 1", plan.Edges)
	}
	e := plan.Edges[0]
	if e.Consumer != "Billing" || e.Producer != "User" || e.Field != "Users" || e.Type != "user.Service" {
		t.Fatalf("edge = %+v", e)
	}
}

func TestComputeBuildPlan_ConventionalDepsAreNotEdges(t *testing.T) {
	// Logger/Config/Repo resolve to "" (no producer) so they never order.
	comps := []BuildComponent{
		comp("user", "User", dep("Logger", "*slog.Logger"), dep("Config", "*config.Config"), dep("Repo", "Repository")),
	}
	plan := ComputeBuildPlan(comps, mapResolver{})
	if len(plan.Edges) != 0 {
		t.Fatalf("edges = %v, want none", plan.Edges)
	}
	if got := orderFields(plan); !reflect.DeepEqual(got, []string{"User"}) {
		t.Fatalf("order = %v", got)
	}
}

func TestComputeBuildPlan_Diamond(t *testing.T) {
	// audit is the shared producer (per-binary singleton). user and
	// billing both consume it; billing also consumes user. Order must be
	// audit, user, billing.
	comps := []BuildComponent{
		comp("audit", "Audit", dep("Repo", "Repository")),
		comp("user", "User", dep("Audit", "audit.Service")),
		comp("billing", "Billing", dep("Users", "user.Service"), dep("Audit", "audit.Service")),
	}
	resolver := mapResolver{"audit.Service": "Audit", "user.Service": "User"}
	plan := ComputeBuildPlan(comps, resolver)
	if plan.HasCycle() {
		t.Fatalf("unexpected cycle: %v", plan.Cycles)
	}
	got := orderFields(plan)
	want := []string{"Audit", "User", "Billing"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	// Audit is consumed twice -> two edges into it from distinct
	// consumers; the diamond is handled without a true cycle.
	auditEdges := 0
	for _, e := range plan.Edges {
		if e.Producer == "Audit" {
			auditEdges++
		}
	}
	if auditEdges != 2 {
		t.Fatalf("audit edges = %d, want 2", auditEdges)
	}
}

func TestComputeBuildPlan_CycleDetectedNotSilentlyBroken(t *testing.T) {
	// billing<->user mutual dependency. Both must be reported as a cycle
	// with the back-edges surfaced (so build.go emits two-phase setters),
	// never silently dropped.
	comps := []BuildComponent{
		comp("billing", "Billing", dep("Users", "user.Service")),
		comp("user", "User", dep("Billing", "billing.Service")),
	}
	resolver := mapResolver{"user.Service": "User", "billing.Service": "Billing"}
	plan := ComputeBuildPlan(comps, resolver)
	if !plan.HasCycle() {
		t.Fatalf("expected a cycle, got none; order=%v", orderFields(plan))
	}
	wantCycle := []string{"Billing", "User"}
	if !reflect.DeepEqual(plan.Cycles, wantCycle) {
		t.Fatalf("cycles = %v, want %v", plan.Cycles, wantCycle)
	}
	// Both components are still in Order (constructed, not dropped).
	if got := orderFields(plan); !reflect.DeepEqual(got, []string{"Billing", "User"}) {
		t.Fatalf("order = %v, want both components present", got)
	}
	// Both back-edges are surfaced for the two-phase setter stubs.
	if len(plan.CycleEdges) != 2 {
		t.Fatalf("cycle edges = %v, want 2", plan.CycleEdges)
	}
}

func TestComputeBuildPlan_CycleEdgesOnlyBetweenCycleMembers(t *testing.T) {
	// A consumer (report) that merely depends on a cycle member must NOT
	// be listed as a cycle edge — it is orderable once the cycle resolves.
	comps := []BuildComponent{
		comp("billing", "Billing", dep("Users", "user.Service")),
		comp("user", "User", dep("Billing", "billing.Service")),
		comp("report", "Report", dep("Users", "user.Service")),
	}
	resolver := mapResolver{"user.Service": "User", "billing.Service": "Billing"}
	plan := ComputeBuildPlan(comps, resolver)
	if !plan.HasCycle() {
		t.Fatalf("expected cycle")
	}
	for _, e := range plan.CycleEdges {
		if e.Consumer == "Report" {
			t.Fatalf("Report must not be a cycle edge: %+v", e)
		}
	}
}

func TestComputeBuildPlan_Deterministic(t *testing.T) {
	// Independent components must come out in a stable (FieldName) order
	// regardless of input order.
	a := []BuildComponent{comp("c", "C"), comp("a", "A"), comp("b", "B")}
	b := []BuildComponent{comp("b", "B"), comp("c", "C"), comp("a", "A")}
	pa := ComputeBuildPlan(a, mapResolver{})
	pb := ComputeBuildPlan(b, mapResolver{})
	if !reflect.DeepEqual(orderFields(pa), orderFields(pb)) {
		t.Fatalf("nondeterministic: %v vs %v", orderFields(pa), orderFields(pb))
	}
	if want := []string{"A", "B", "C"}; !reflect.DeepEqual(orderFields(pa), want) {
		t.Fatalf("order = %v, want %v", orderFields(pa), want)
	}
}

func TestComputeBuildPlan_SelfReferenceIsNotAnEdge(t *testing.T) {
	comps := []BuildComponent{comp("user", "User", dep("Self", "user.Service"))}
	plan := ComputeBuildPlan(comps, mapResolver{"user.Service": "User"})
	if plan.HasCycle() {
		t.Fatalf("self-reference should not be a cycle: %v", plan.Cycles)
	}
	if len(plan.Edges) != 0 {
		t.Fatalf("self-reference should produce no edge: %v", plan.Edges)
	}
}

func TestServiceKeyResolver_MatchesPackageQualifiedAndPointer(t *testing.T) {
	// Unambiguous clauses resolve via the clause fallback (the consumer
	// here has no import block, exercising the fallback path).
	comps := []BuildComponent{
		{FieldName: "User", ServiceTypeKey: "m/internal/user.Service", compPackageKey: "user.Service"},
		{FieldName: "Audit", ServiceTypeKey: "m/internal/audit.Service", compPackageKey: "audit.Service"},
		{FieldName: "Leaf"}, // no service key — produces nothing
	}
	r := NewServiceKeyResolver(comps)
	consumer := BuildComponent{FieldName: "Consumer"}
	if got := r.Resolve(consumer, "user.Service"); got != "User" {
		t.Fatalf("user.Service -> %q, want User", got)
	}
	if got := r.Resolve(consumer, "*audit.Service"); got != "Audit" {
		t.Fatalf("*audit.Service -> %q, want Audit", got)
	}
	if got := r.Resolve(consumer, "Repository"); got != "" {
		t.Fatalf("conventional dep should not resolve, got %q", got)
	}
	if got := r.Resolve(consumer, "leaf.Service"); got != "" {
		t.Fatalf("leaf has no service key, got %q", got)
	}
}

// TestServiceKeyResolver_ImportPathDisambiguatesSameClause is the FIX #1
// regression: a domain pkg internal/billing and a handler pkg
// internal/handlers/billing BOTH `package billing` must resolve to the
// correct DISTINCT producer based on the consumer's import block, not
// collide on the bare clause "billing.Service".
func TestServiceKeyResolver_ImportPathDisambiguatesSameClause(t *testing.T) {
	const mod = "example.com/proj"
	domainPath := mod + "/internal/billing"
	handlerPath := mod + "/internal/handlers/billing"
	comps := []BuildComponent{
		{FieldName: "BillingDomain", ServiceTypeKey: domainPath + ".Service", compPackageKey: "billing.Service"},
		{FieldName: "Billing", ServiceTypeKey: handlerPath + ".Service", compPackageKey: "billing.Service"},
	}
	r := NewServiceKeyResolver(comps)

	// A consumer that imports the DOMAIN billing under clause "billing"
	// resolves billing.Service to the domain producer.
	domainConsumer := BuildComponent{FieldName: "Reports", compImports: map[string]string{"billing": domainPath}}
	if got := r.Resolve(domainConsumer, "billing.Service"); got != "BillingDomain" {
		t.Fatalf("domain consumer billing.Service -> %q, want BillingDomain", got)
	}

	// A consumer that imports the HANDLER billing resolves to the handler.
	handlerConsumer := BuildComponent{FieldName: "Gateway", compImports: map[string]string{"billing": handlerPath}}
	if got := r.Resolve(handlerConsumer, "billing.Service"); got != "Billing" {
		t.Fatalf("handler consumer billing.Service -> %q, want Billing", got)
	}

	// Without an import block, the shared clause is AMBIGUOUS and must not
	// be guessed — resolves to "" (falls to Infra / loud-missing).
	blind := BuildComponent{FieldName: "Blind"}
	if got := r.Resolve(blind, "billing.Service"); got != "" {
		t.Fatalf("ambiguous clause without imports -> %q, want \"\"", got)
	}
}

// TestServiceKeyResolver_ExplicitImportToExternalDoesNotFallBack: when a
// consumer imports a package that is NOT a registered producer (it is
// external/hand-built on Infra), the resolver must NOT fall back to a
// same-clause producer — the consumer was explicit. This is the
// external-component case (handler `Domain user.Service` must resolve to
// infra.<Field>, never to a different same-clause handler producer).
func TestServiceKeyResolver_ExplicitImportToExternalDoesNotFallBack(t *testing.T) {
	const mod = "example.com/proj"
	handlerPath := mod + "/internal/handlers/user"
	externalPath := mod + "/internal/user" // filtered out — not a producer
	comps := []BuildComponent{
		// Only the HANDLER user is a producer (external domain user was
		// filtered from the Build graph before the resolver was built).
		{FieldName: "User", ServiceTypeKey: handlerPath + ".Service", compPackageKey: "user.Service"},
	}
	r := NewServiceKeyResolver(comps)

	// The handler consumer imports the EXTERNAL domain user under clause
	// "user". It must NOT resolve to itself / the handler producer.
	consumer := BuildComponent{FieldName: "User", compImports: map[string]string{"user": externalPath}}
	if got := r.Resolve(consumer, "user.Service"); got != "" {
		t.Fatalf("explicit import to external pkg -> %q, want \"\" (falls to Infra)", got)
	}
}
