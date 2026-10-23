// The remedy the multi-impl refusal prescribes has to actually work.
//
// The reported sequence (control-plane C6, internal/registryauth): a package
// gains a SECOND implementation of an exported interface, so forge's
// forge-exclude-contract-multi-impl rule correctly refuses the package's
// `//forge:exclude-contract`. Its fix hint says to move the interface into a
// contract.go and mark it `//forge:contract` — which the author did. That
// satisfied the exclusion gate and immediately traded it for a finding from
// the SHAPE rule, because a contract.go makes the package a contract package
// and the shape rule then wants `func(Deps) <Contract>`.
//
// The package's own constructor returns `*Handler`, a concrete type, and no
// marker changes that: `//forge:constructor` frees the constructor's NAME,
// never its signature. So the hint the shape rule offered in turn —
// "add a `//forge:constructor` marker" — was a no-op on this shape, and the
// author's only way out was a forge.yaml `contracts.exclude`, which covers
// the shape rule but not the gate.
//
// These tests pin both halves: the remedy composes, and where it cannot, the
// message says something true.

package contractcheck

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// writeSeamPackage writes the reported shape: a marked multi-impl SEAM
// interface in contract.go with its in-package default implementation, and a
// constructor in a sibling file returning the package's own concrete type.
// ctorDoc is prepended to the constructor's doc comment.
func writeSeamPackage(t *testing.T, ctorDoc string) string {
	t.Helper()
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "registryauth")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "contract.go"), `package registryauth

import "context"

// PushGate is the quota seam: two interchangeable implementations, which is
// what makes it a contract rather than indirection.
//
//forge:contract
type PushGate interface {
	PushAllowed(ctx context.Context, orgID string) (bool, string)
}

// AllowAllPushes is the production default implementation.
type AllowAllPushes struct{}

func (AllowAllPushes) PushAllowed(context.Context, string) (bool, string) { return true, "" }
`))
	must(t, writeFile(filepath.Join(pkgDir, "realm.go"), `package registryauth

type Deps struct{ Push PushGate }

// Handler is the token realm this package serves.
type Handler struct{}

`+ctorDoc+`func New(deps Deps) (*Handler, error) { return &Handler{}, nil }
`))
	return tmp
}

// TestInternalContracts_MarkedSeamWithConcreteConstructorIsAccepted is the
// fix. A package whose marked contract is a SEAM — an interface with
// interchangeable implementations, which is exactly what the multi-impl rule
// demands be moved into contract.go — is not a package whose constructor
// returns that interface. Its constructor builds the package's own concrete
// type and TAKES the seam as a dependency.
//
// Demanding `func(Deps) PushGate` there is demanding that the quota gate
// construct a quota gate. So the shape rule accepts a constructor returning
// the package's own concrete type when the marked contract is implemented
// in-package by something OTHER than that type, which is the seam shape and
// not the component shape.
func TestInternalContracts_MarkedSeamWithConcreteConstructorIsAccepted(t *testing.T) {
	for _, ctorDoc := range []string{
		"",                      // the shape as the author first wrote it
		"//forge:constructor\n", // and with the remedy the old message prescribed
	} {
		tmp := writeSeamPackage(t, ctorDoc)
		fs, err := Inspect(context.Background(), tmp,
			Options{Rules: []Rule{RuleInternalPackageContractNames}})
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		if got := findingsForRule(fs, string(RuleInternalPackageContractNames)); len(got) != 0 {
			t.Errorf("ctorDoc=%q: moving a multi-impl seam into contract.go is the remedy the "+
				"exclusion gate prescribes; the shape rule must not then refuse it. Got %d finding(s):\n%s",
				ctorDoc, len(got), AsResult(fs).FormatText())
		}
	}
}

// TestInternalContracts_ComponentConstructorStillRequiresTheSignature is the
// guard on the above. The seam allowance keys on the marked contract having
// another in-package implementation; a package whose marked contract is its
// OWN behavioral surface (implemented by the unexported type its constructor
// returns) is the ordinary component shape, and the signature requirement
// still holds there.
func TestInternalContracts_ComponentConstructorStillRequiresTheSignature(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "mailer")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "contract.go"), `package mailer

//forge:contract
type Mailer interface { Send(to string) error }

type Deps struct{}

type mailer struct{}

func (mailer) Send(string) error { return nil }

// Open returns the package's own type instead of its contract, so the
// injector has nothing to bind.
//forge:constructor
func Open(d Deps) (*mailer, error) { return &mailer{}, nil }
`))
	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleInternalPackageContractNames}})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleInternalPackageContractNames))
	if len(got) != 1 {
		t.Fatalf("a component whose constructor does not return its contract must still be "+
			"refused; got %d finding(s):\n%s", len(got), AsResult(fs).FormatText())
	}
	if !strings.Contains(got[0].Message, "Open") {
		t.Errorf("the finding must name the near miss the author wrote; got: %s", got[0].Message)
	}
}

// TestInternalContracts_ConstructorFindingDoesNotPrescribeTheMarkerForASignatureGap
// pins the MESSAGE. `//forge:constructor` frees a constructor's NAME; it has
// never had any effect on its signature. Prescribing it to an author whose
// signature is the thing being refused sends them to add a line that changes
// nothing — which is what happened, and what cost the reporting agent the
// detour into forge.yaml.
//
// The marker is still the right advice for a differently-NAMED constructor,
// so the message must offer it for that and only that.
func TestInternalContracts_ConstructorFindingDoesNotPrescribeTheMarkerForASignatureGap(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "store")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "contract.go"), `package store

//forge:contract
type Store interface { Get(k string) string }

type Deps struct{}

type store struct{}

func (store) Get(string) string { return "" }

// Connect is marked, so its NAME is already accepted; its signature is not.
//forge:constructor
func Connect(dsn string) (*store, error) { return &store{}, nil }
`))
	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleInternalPackageContractNames}})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleInternalPackageContractNames))
	if len(got) != 1 {
		t.Fatalf("expected 1 constructor finding, got %d:\n%s", len(got), AsResult(fs).FormatText())
	}
	text := got[0].Message + got[0].Remediation
	// The marker may be MENTIONED — saying "it already frees the name, and
	// that is all it does" is the useful correction. What it must not do is
	// instruct the author to ADD it, which is the no-op they will try and
	// discard.
	for _, prescription := range []string{
		"add a `//forge:constructor`",
		"put `//forge:constructor`",
		"works once you put",
	} {
		if strings.Contains(text, prescription) {
			t.Errorf("this constructor is ALREADY marked, so %q is advice that cannot change the "+
				"verdict:\nmessage: %s\nremediation: %s",
				prescription, got[0].Message, got[0].Remediation)
		}
	}
	// And it must say the marker is not the lever here, so the author does
	// not go looking for a second place to put it.
	if !strings.Contains(text, "that is all it does") {
		t.Errorf("the finding must say the marker is already satisfied and inert for a signature "+
			"gap:\nmessage: %s", got[0].Message)
	}
	// It must still say what IS wrong and what to change.
	for _, want := range []string{"func(Deps) Store", "Connect"} {
		if !strings.Contains(text, want) {
			t.Errorf("the finding must name %q:\nmessage: %s\nremediation: %s",
				want, got[0].Message, got[0].Remediation)
		}
	}
}
