package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeContract writes contract.go for a package at rolePath under projectDir,
// verbatim — for fixtures that need a header marker or imports, which
// writeComponentDeps does not take.
func writeContract(t *testing.T, projectDir, rolePath, src string) {
	t.Helper()
	dir := filepath.Join(projectDir, filepath.FromSlash(rolePath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "contract.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
}

// cliauthContract is reliant's shape: a CLI login flow whose Deps are a
// concrete config struct from another package, a dep interface declared
// beside the contract, and a func — none of which the server provides, and
// none of which it should.
func cliauthContract(header string) string {
	return header + `package cliauth

import (
	"net/http"

	"example.com/proj/internal/credentials"
)

type Service interface{ Login() error }

type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

type Deps struct {
	Dirs    credentials.Dirs
	HTTP    HTTPDoer
	OpenURL func(string) error
}

func New(d Deps) Service { return nil }
`
}

func composeInput(t *testing.T, dir string, pkgs ...string) InjectGenInput {
	t.Helper()
	packages, err := PackageDataFromNames(pkgs, dir)
	if err != nil {
		t.Fatalf("PackageDataFromNames: %v", err)
	}
	return InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
		Packages:   packages,
	}
}

// A not-a-component package is not composed: no Components field, no
// construction, and — the point — its Deps are never resolved, so a Deps set
// the server cannot (and should not) provide does not fail generate. An
// unmarked package beside it is composed exactly as before.
func TestGenerateCompose_NotAComponentIsNotComposed(t *testing.T) {
	dir := newInjectProject(t)
	writeContract(t, dir, "internal/handlers/orders",
		"package orders\n\nimport \"example.com/proj/internal/pricing\"\n\ntype Service interface{ Do() }\n\ntype Deps struct {\n\tPricing pricing.Service\n}\n\nfunc New(d Deps) Service { return nil }\n")
	writeComponentDeps(t, dir, "internal", "pricing", "pricing", "")
	writeContract(t, dir, "internal/cliauth", cliauthContract("//forge:not-a-component: CLI-only: built by the login command\n"))

	if err := GenerateCompose(composeInput(t, dir, "cliauth", "pricing")); err != nil {
		t.Fatalf("a not-a-component package's Deps must never be resolved; got: %v", err)
	}
	out := readInject(t, dir)
	if strings.Contains(strings.ToLower(out), "cliauth") {
		t.Fatalf("compose.go must not import, declare or construct the not-a-component package:\n%s", out)
	}
	for _, want := range []string{"c.Pricing =", "c.Orders =", "Pricing: pricingInst,"} {
		if !containsNormalized(out, want) {
			t.Fatalf("the unmarked package and the handler must compose as before (missing %q):\n%s", want, out)
		}
	}

	// lifecycle.go reads the same composed set.
	if err := GenerateLifecycle(composeInput(t, dir, "cliauth", "pricing")); err != nil {
		t.Fatalf("GenerateLifecycle: %v", err)
	}
}

// A server component that REQUIRES a not-a-component package by type makes
// the marker false. forge refuses, naming the marker, its reason and the three
// ways out — instead of the missing-provider error, whose remedy (put it on
// Infra) is the wrong design for a CLI collaborator.
func TestGenerateCompose_NotAComponentRequiredByServerIsRefused(t *testing.T) {
	dir := newInjectProject(t)
	writeContract(t, dir, "internal/handlers/orders",
		"package orders\n\nimport \"example.com/proj/internal/cliauth\"\n\ntype Service interface{ Do() }\n\ntype Deps struct {\n\tAuth cliauth.Service\n}\n\nfunc New(d Deps) Service { return nil }\n")
	writeContract(t, dir, "internal/cliauth", cliauthContract("//forge:not-a-component: CLI-only: built by the login command\n"))

	err := GenerateCompose(composeInput(t, dir, "cliauth"))
	if err == nil {
		t.Fatal("a server component requiring a not-a-component package must fail generate")
	}
	msg := err.Error()
	for _, want := range []string{
		"Orders.Deps.Auth (cliauth.Service)",
		"internal/cliauth",
		`"CLI-only: built by the login command"`,
		"delete the marker",
		"//forge:external-component",
		"//forge:optional-dep",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "has no provider") {
		t.Fatalf("the contradiction must be reported as such, not as a missing provider:\n%s", msg)
	}
}

// An OPTIONAL consumer already accepts nil — which is exactly what a package
// the server does not have provides — so it is not a contradiction.
func TestGenerateCompose_NotAComponentOptionalConsumerIsNil(t *testing.T) {
	dir := newInjectProject(t)
	writeContract(t, dir, "internal/handlers/orders",
		"package orders\n\nimport \"example.com/proj/internal/cliauth\"\n\ntype Service interface{ Do() }\n\ntype Deps struct {\n\t// forge:optional-dep\n\tAuth cliauth.Service\n}\n\nfunc New(d Deps) Service { return nil }\n")
	writeContract(t, dir, "internal/cliauth", cliauthContract("//forge:not-a-component: CLI-only\n"))

	if err := GenerateCompose(composeInput(t, dir, "cliauth")); err != nil {
		t.Fatalf("an optional consumer must not be refused: %v", err)
	}
	if out := readInject(t, dir); !strings.Contains(out, "Auth: nil,") {
		t.Fatalf("optional dep on a not-a-component package should be typed nil:\n%s", out)
	}
}

// The derivable half of the decision: an unmarked package whose Deps have no
// provider, and which NO service, worker or operator reaches, gets the marker
// named as a remedy beside the Infra one. It stays a hint — the same shape is
// a server package whose consumer is not written yet.
func TestGenerateCompose_MissingProviderOnUnconsumedPackageNamesTheMarker(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "orders", "orders", "")
	writeContract(t, dir, "internal/cliauth", cliauthContract(""))

	err := GenerateCompose(composeInput(t, dir, "cliauth"))
	if err == nil {
		t.Fatal("an unmarked package with unresolvable Deps must still fail generate")
	}
	msg := err.Error()
	for _, want := range []string{
		"Cliauth.Deps.Dirs (credentials.Dirs) has no provider",
		"Cliauth.Deps.OpenURL (func(string) error) has no provider",
		"No service, worker or operator requires internal/cliauth",
		"//forge:not-a-component: <who constructs it>",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q:\n%s", want, msg)
		}
	}
}

// Once a server component consumes the package — directly or through another
// package — it IS a server package, and the hint would be wrong.
func TestGenerateCompose_MissingProviderOnConsumedPackageOmitsTheHint(t *testing.T) {
	dir := newInjectProject(t)
	writeContract(t, dir, "internal/handlers/orders",
		"package orders\n\nimport \"example.com/proj/internal/session\"\n\ntype Service interface{ Do() }\n\ntype Deps struct {\n\tSession session.Service\n}\n\nfunc New(d Deps) Service { return nil }\n")
	// orders → session → cliauth: cliauth is reached transitively.
	writeContract(t, dir, "internal/session",
		"package session\n\nimport \"example.com/proj/internal/cliauth\"\n\ntype Service interface{ Do() }\n\ntype Deps struct {\n\tAuth cliauth.Service\n}\n\nfunc New(d Deps) Service { return nil }\n")
	writeContract(t, dir, "internal/cliauth", cliauthContract(""))

	err := GenerateCompose(composeInput(t, dir, "cliauth", "session"))
	if err == nil {
		t.Fatal("cliauth's Deps still have no provider")
	}
	if msg := err.Error(); strings.Contains(msg, "forge:not-a-component") {
		t.Fatalf("a package the server reaches must not be pointed at not-a-component:\n%s", msg)
	}
}

// The reporters (`forge project audit`, `forge project graph`) read a
// package's composition off the same markers compose.go obeys, so they list
// every contract package and can say which ones the server does not build.
func TestDiscoverInternalPackages_Composition(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal", "pricing", "pricing", "")
	writeContract(t, dir, "internal/natsbus", "//forge:external-component\npackage natsbus\n\ntype Service interface{ Do() }\n")
	writeContract(t, dir, "internal/cliauth", cliauthContract("//forge:not-a-component: CLI-only\n"))

	pkgs, err := DiscoverInternalPackages(dir)
	if err != nil {
		t.Fatalf("DiscoverInternalPackages: %v", err)
	}
	got := map[string]string{}
	for _, p := range pkgs {
		got[p.Name] = p.Composition + "|" + p.CompositionReason
	}
	want := map[string]string{
		"pricing": CompositionComposed + "|",
		"natsbus": CompositionExternal + "|",
		"cliauth": CompositionNotAComponent + "|CLI-only",
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s: composition = %q, want %q (all: %v)", name, got[name], w, got)
		}
	}
}

func TestUnconsumedPackages(t *testing.T) {
	comps := []BuildComponent{
		{FieldName: "Orders", compRoleRoot: "internal/handlers", ImportPath: "internal/handlers/orders"},
		{FieldName: "Sweep", compRoleRoot: "internal/workers", ImportPath: "internal/workers/sweep"},
		{FieldName: "Session", compRoleRoot: "internal", ImportPath: "internal/session"},
		{FieldName: "Store", compRoleRoot: "internal", ImportPath: "internal/store"},
		{FieldName: "Cliauth", compRoleRoot: "internal", ImportPath: "internal/cliauth"},
		{FieldName: "Keyring", compRoleRoot: "internal", ImportPath: "internal/keyring"},
	}
	edges := []BuildEdge{
		{Consumer: "Orders", Producer: "Session"},
		{Consumer: "Session", Producer: "Store"},
		// cliauth → keyring: consumed, but only by another unreached package.
		{Consumer: "Cliauth", Producer: "Keyring"},
	}
	got := unconsumedPackages(comps, edges)
	if len(got) != 2 || got["Cliauth"] != "internal/cliauth" || got["Keyring"] != "internal/keyring" {
		t.Fatalf("unconsumedPackages = %v, want Cliauth and Keyring only", got)
	}
}
