package codegen

import (
	"path/filepath"
	"strings"
	"testing"
)

// writeTypedProject writes a go.mod plus the given files (path → source) so
// the go/types producer inference can load a real, type-checking universe.
func writeTypedProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := newInjectProject(t)
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/proj\n\ngo 1.23\n")
	for rel, src := range files {
		mustWrite(t, filepath.Join(dir, filepath.FromSlash(rel)), src)
	}
	return dir
}

// stripeContract is an adapter-shaped producer: New returns the contract
// interface, and its concrete impl has three methods.
const stripeContract = `package stripe

type Service interface {
	Charge() error
	Refund() error
	Customer() error
}

type service struct{}

func (service) Charge() error   { return nil }
func (service) Refund() error   { return nil }
func (service) Customer() error { return nil }

type Deps struct{}

func New(d Deps) Service { return service{} }
`

const emptyInfra = "package app\n\ntype Infra struct{}\n"

func composeWith(t *testing.T, dir string, pkgs ...string) error {
	t.Helper()
	var data []BootstrapPackageData
	for _, p := range pkgs {
		data = append(data, BootstrapPackageData{Name: p, Package: p, ImportPath: p, FieldName: strings.ToUpper(p[:1]) + p[1:], VarName: p})
	}
	return GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Packages:   data,
	})
}

// A consumer declaring the narrow interface it needs — forge's own "declare
// interfaces where they are CONSUMED" rule — must resolve to the one
// component that satisfies it, not fail with "no provider".
func TestGenerateCompose_ConsumerNarrowInterfaceResolvesByAssignability(t *testing.T) {
	dir := writeTypedProject(t, map[string]string{
		"internal/app/providers.go":   emptyInfra,
		"internal/stripe/contract.go": stripeContract,
		"internal/enrollment/contract.go": `package enrollment

type Payments interface {
	Charge() error
	Refund() error
}

type Service interface{ Enroll() error }

type service struct{ d Deps }

func (s service) Enroll() error { return s.d.Payments.Charge() }

type Deps struct {
	Payments Payments
}

func New(d Deps) Service { return service{d: d} }
`,
	})
	if err := composeWith(t, dir, "enrollment", "stripe"); err != nil {
		t.Fatalf("narrow consumer interface must resolve by assignability: %v", err)
	}
	out := readInject(t, dir)
	if !containsNormalized(out, "Payments: stripeInst,") {
		t.Fatalf("Enrollment.Payments should be wired to the stripe producer:\n%s", out)
	}
	if si, ei := strings.Index(out, "stripe.New("), strings.Index(out, "enrollment.New("); si < 0 || ei < 0 || si > ei {
		t.Fatalf("stripe must be constructed before enrollment:\n%s", out)
	}
}

// A type alias of the producer's contract is the same type to go/types but a
// different string to the exact-key resolver.
func TestGenerateCompose_AliasOfProducerContractResolves(t *testing.T) {
	dir := writeTypedProject(t, map[string]string{
		"internal/app/providers.go":   emptyInfra,
		"internal/stripe/contract.go": stripeContract,
		"internal/enrollment/contract.go": `package enrollment

import "example.com/proj/internal/stripe"

type Payments = stripe.Service

type Service interface{ Enroll() error }

type service struct{}

func (service) Enroll() error { return nil }

type Deps struct {
	Payments Payments
}

func New(d Deps) Service { return service{} }
`,
	})
	if err := composeWith(t, dir, "enrollment", "stripe"); err != nil {
		t.Fatalf("alias dep must resolve: %v", err)
	}
	if out := readInject(t, dir); !containsNormalized(out, "Payments: stripeInst,") {
		t.Fatalf("aliased dep should be wired to the stripe producer:\n%s", out)
	}
}

// Two components satisfying one consumer interface is a choice forge must
// not make silently.
func TestGenerateCompose_AmbiguousInterfaceProviderIsLoud(t *testing.T) {
	paypal := strings.ReplaceAll(stripeContract, "package stripe", "package paypal")
	dir := writeTypedProject(t, map[string]string{
		"internal/app/providers.go":   emptyInfra,
		"internal/stripe/contract.go": stripeContract,
		"internal/paypal/contract.go": paypal,
		"internal/enrollment/contract.go": `package enrollment

type Payments interface{ Charge() error }

type Service interface{ Enroll() error }

type service struct{}

func (service) Enroll() error { return nil }

type Deps struct {
	Payments Payments
}

func New(d Deps) Service { return service{} }
`,
	})
	err := composeWith(t, dir, "enrollment", "paypal", "stripe")
	if err == nil {
		t.Fatalf("expected an ambiguity error, got nil:\n%s", readInject(t, dir))
	}
	for _, want := range []string{"MORE THAN ONE", "Enrollment.Deps.Payments", "Paypal, Stripe"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q:\n%v", want, err)
		}
	}
}

// An Infra field satisfying the interface is an explicit binding: it wins
// over inference (no ambiguity, no producer edge).
func TestGenerateCompose_InfraBindingBeatsInferredProducer(t *testing.T) {
	dir := writeTypedProject(t, map[string]string{
		"internal/app/providers.go": `package app

import "example.com/proj/internal/stripe"

type Infra struct {
	Gateway stripe.Service
}
`,
		"internal/stripe/contract.go": stripeContract,
		"internal/enrollment/contract.go": `package enrollment

type Payments interface{ Charge() error }

type Service interface{ Enroll() error }

type service struct{}

func (service) Enroll() error { return nil }

type Deps struct {
	Payments Payments
}

func New(d Deps) Service { return service{} }
`,
	})
	if err := composeWith(t, dir, "enrollment", "stripe"); err != nil {
		t.Fatalf("compose: %v", err)
	}
	if out := readInject(t, dir); !containsNormalized(out, "Payments: infra.Gateway,") {
		t.Fatalf("an assignable Infra field must win over an inferred producer:\n%s", out)
	}
}
