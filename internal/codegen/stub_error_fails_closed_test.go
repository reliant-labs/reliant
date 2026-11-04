// File: internal/codegen/stub_error_fails_closed_test.go
//
// The auto-stub's default for a method whose last result is `error`.
//
// The zero value of `error` is nil, and nil is not an empty answer: it
// claims the call SUCCEEDED. From a gate the application consults before
// acting it means PERMITTED — a measured dogfood run wrote a test asserting
// that an under-privileged caller could not obtain an upload URL, and it
// failed because the generated stub had let the call through. From a lookup
// it means FOUND, and the handler dereferences the nil value beside it.
//
// Forge already makes the opposite choice one generator over: MockService,
// projected from the same contract.go, returns contractkit.MockNotSet for
// a method whose Func field was never assigned. Two generated doubles for
// one interface with opposite safety defaults is the defect — this pins
// them to the same one.
//
// The rule is deliberately structural rather than name-based. Matching on
// a naming convention instead would fail open for every gate spelled some
// other way, which is most of them — and this generator stubs EVERY
// interface on a handler's Deps, not a special category of them.
//
// The fixtures below use a quota gate rather than a permission one on
// purpose: the defect is a property of the SIGNATURE, and naming the
// fixture after one domain invites a future reader to narrow the rule to
// it.

package codegen

import (
	"strings"
	"testing"
)

// quotaStubFixture is a handler package whose Deps carry a cross-package
// gate seam — the shape the run hit. CheckQuota returns bare `error`
// (the fail-open case), OrgIDsFor returns a value plus error (which must
// keep its zero-value default: there is a real result to be empty).
func quotaStubFixture(t *testing.T) string {
	t.Helper()
	return writeStubFixtureModule(t, "internal/handlers/documents", map[string]string{
		"internal/policy/policy.go": `package policy

import "context"

// Tier is a named scalar, so the stub's zero for it is the universal form.
type Tier int

// Service is the gate a handler consults before acting.
type Service interface {
	CheckQuota(ctx context.Context, userID, orgID string, min Tier) error
	OrgIDsFor(ctx context.Context, userID string) ([]string, error)
	Close() error
}
`,
		"internal/handlers/documents/service.go": `package documents

import (
	"log/slog"

	"example.com/proj/internal/policy"
)

type Deps struct {
	Logger *slog.Logger
	Policy policy.Service
}
`,
	})
}

func methodsByName(t *testing.T, stub DepsAutoStub) map[string]InterfaceMethod {
	t.Helper()
	out := map[string]InterfaceMethod{}
	for _, m := range stub.Methods {
		out[m.Name] = m
	}
	return out
}

// TestComputeAutoStubs_ErrorOnlyMethodFailsClosed is the finding itself.
// `CheckQuota(...) error` must not stub to `return nil`: nil is ALLOW,
// and a test that never overrides the field would assert against a check
// that was switched off.
func TestComputeAutoStubs_ErrorOnlyMethodFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks or builds fixture modules via the go toolchain; runs in task test")
	}
	handlerDir := quotaStubFixture(t)

	stubs, unresolved := computeAutoStubs(handlerDir, "")
	if len(unresolved) != 0 {
		t.Fatalf("policy.Service should resolve, got unresolved %v", unresolved)
	}
	if len(stubs) != 1 {
		t.Fatalf("expected exactly one auto-stub (Policy), got %d: %v", len(stubs), stubs)
	}
	byName := methodsByName(t, stubs[0])

	require, ok := byName["CheckQuota"]
	if !ok {
		t.Fatalf("missing CheckQuota in %v", stubs[0].Methods)
	}
	if require.ReturnStatement == "return nil" {
		t.Fatalf("CheckQuota stubs to `return nil`, which is ALLOW.\n"+
			"A handler test that does not override Deps.Policy runs with the check disabled, "+
			"and a bug that skips the gate passes its tests.\n"+
			"Want a fail-closed default, as MockService already emits for an unset method.\n"+
			"got: %q", require.ReturnStatement)
	}
	if !strings.Contains(require.ReturnStatement, "StubNotConfigured") {
		t.Errorf("CheckQuota should return the named stub-not-configured error so the "+
			"failure says WHICH method was left unconfigured.\ngot: %q", require.ReturnStatement)
	}
}

// TestComputeAutoStubs_ErrorOnlyAppliesToEveryErrorOnlyMethod pins that the
// rule is structural, not a name heuristic. `Close() error` is not a gate
// method and still returns only error — a stub cannot know it succeeded.
func TestComputeAutoStubs_ErrorOnlyAppliesToEveryErrorOnlyMethod(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks or builds fixture modules via the go toolchain; runs in task test")
	}
	handlerDir := quotaStubFixture(t)

	stubs, _ := computeAutoStubs(handlerDir, "")
	if len(stubs) != 1 {
		t.Fatalf("expected one auto-stub, got %d", len(stubs))
	}
	closeM, ok := methodsByName(t, stubs[0])["Close"]
	if !ok {
		t.Fatalf("missing Close in %v", stubs[0].Methods)
	}
	if closeM.ReturnStatement == "return nil" {
		t.Errorf("Close() error stubs to `return nil` — a name-based rule that exempted it " +
			"would fail open for every gate spelled some other way")
	}
}

// TestComputeAutoStubs_ValuePlusErrorRefusesUnprogrammed is where the rule
// used to stop, and the reason it no longer does.
//
// A method returning (T, error) was exempted on the theory that (nil, nil) is
// a legible "nothing found". Handlers do not read it that way: a nil error
// means the lookup SUCCEEDED, so the value is dereferenced, and a not-found
// or failure branch could not be reached through the stub at all. The stub
// now refuses here too — the same default a contract mock makes — and a test
// that needs an answer programs OrgIDsForFunc. That the rendered stub
// compiles and behaves so is pinned end to end in stub_programmable_test.go.
func TestComputeAutoStubs_ValuePlusErrorRefusesUnprogrammed(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks or builds fixture modules via the go toolchain; runs in task test")
	}
	handlerDir := quotaStubFixture(t)

	stubs, _ := computeAutoStubs(handlerDir, "")
	if len(stubs) != 1 {
		t.Fatalf("expected one auto-stub, got %d", len(stubs))
	}
	orgIDs, ok := methodsByName(t, stubs[0])["OrgIDsFor"]
	if !ok {
		t.Fatalf("missing OrgIDsFor in %v", stubs[0].Methods)
	}
	want := `return nil, testkit.StubNotConfigured("StubPolicy", "OrgIDsFor")`
	if orgIDs.ReturnStatement != want {
		t.Errorf("an unprogrammed (T, error) method must refuse, not answer (nil, nil) — "+
			"a handler reads a nil error as found.\n got: %q\nwant: %q",
			orgIDs.ReturnStatement, want)
	}
}

// TestComputeAutoStubs_LocalErrorOnlyMethodFailsClosed covers the other
// branch. computeAutoStubs resolves a bare-identifier Deps type through
// ParseLocalInterfaces, an entirely separate path from the cross-package
// resolver — a fix applied to only one of them leaves half the surface
// failing open.
func TestComputeAutoStubs_LocalErrorOnlyMethodFailsClosed(t *testing.T) {
	handlerDir := writeStubFixtureModule(t, "internal/handlers/documents", map[string]string{
		"internal/handlers/documents/service.go": `package documents

import (
	"context"
	"log/slog"
)

// QuotaChecker is declared IN this package, so the local branch handles it.
type QuotaChecker interface {
	CheckQuota(ctx context.Context, userID, orgID string) error
}

type Deps struct {
	Logger *slog.Logger
	Quota  QuotaChecker
}
`,
	})

	stubs, _ := computeAutoStubs(handlerDir, "")
	if len(stubs) != 1 {
		t.Fatalf("expected one auto-stub (Quota), got %d: %v", len(stubs), stubs)
	}
	require, ok := methodsByName(t, stubs[0])["CheckQuota"]
	if !ok {
		t.Fatalf("missing CheckQuota in %v", stubs[0].Methods)
	}
	if require.ReturnStatement == "return nil" {
		t.Errorf("a LOCALLY-declared interface's error-only method stubs to ALLOW — the local " +
			"and cross-package branches must agree, or half the surface still fails open")
	}
}
