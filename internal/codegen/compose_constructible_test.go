package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The pre-codegen contract check stays silent on two contract.go shapes it
// judges "not a Service-shape package" — no interface at all, and a catalogue
// of interfaces with no Deps and no constructor. Composition used to compose
// both anyway, write `pkg.New(pkg.Deps{})` calls that could not compile, and
// leave the validate build to blame internal/app/compose.go. It must refuse
// instead, naming each package and what is wrong with it, and write nothing.
func TestGenerateCompose_RefusesUnconstructibleComponents(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "orders", "orders", "")
	// An interface catalogue: two interfaces, no Deps, no constructor.
	writeContract(t, dir, "internal/catalog", `package catalog

type Reader interface{ Read() int }

type Writer interface{ Write(int) }
`)
	// No interface, and a constructor that does not take Deps.
	writeContract(t, dir, "internal/legacy", `package legacy

type Deps struct{}

type Impl struct{ x int }

func New(x int) *Impl { return &Impl{x: x} }
`)

	err := GenerateCompose(composeInput(t, dir, "catalog", "legacy"))
	if err == nil {
		t.Fatal("a composed package compose.go cannot construct must fail generate")
	}
	msg := err.Error()
	for _, want := range []string{
		"forge cannot construct these components",
		"  - internal/catalog\n",
		"declares no `type Deps struct`, and compose.go calls catalog.New(catalog.Deps{…})",
		"declares no constructor: no `func New`, and no func marked `// forge:constructor`",
		"  - internal/legacy\n",
		"internal/legacy/contract.go:7: `New` is `func(x int) *Impl`",
		"so its only parameter must be `Deps`",
		"//forge:external-component",
		"`//forge:not-a-component: <why>`",
		"`//forge:exclude-contract: <why>`",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("refusal missing %q:\n%s", want, msg)
		}
	}
	// The handler is fine; only the broken packages are named.
	if strings.Contains(msg, "internal/handlers/orders") {
		t.Fatalf("a constructible handler must not be named:\n%s", msg)
	}
	// Handler-only advice is for handlers, workers and operators.
	if strings.Contains(msg, "A handler, worker or operator") {
		t.Fatalf("the refusal named no handler, worker or operator, so it must not advise on one:\n%s", msg)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "internal", "app", "compose.go")); !os.IsNotExist(statErr) {
		t.Fatalf("a refused composition must not write compose.go (stat err: %v)", statErr)
	}
}

// The markers the refusal names are the way out: a package that says the
// server does not construct it is never asked to be constructible.
func TestGenerateCompose_UnconstructibleNotAComponentIsNotRefused(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "orders", "orders", "")
	writeContract(t, dir, "internal/catalog", `//forge:not-a-component: an interface catalogue; nothing constructs it
package catalog

type Reader interface{ Read() int }

type Writer interface{ Write(int) }
`)

	if err := GenerateCompose(composeInput(t, dir, "catalog")); err != nil {
		t.Fatalf("a not-a-component package is not composed, so its shape cannot fail composition: %v", err)
	}
	if out := readInject(t, dir); strings.Contains(out, "catalog") {
		t.Fatalf("compose.go must not construct the not-a-component package:\n%s", out)
	}
}

// The check asks only whether compose.go's call compiles, not whether the
// package has the canonical contract shape: a constructor taking Deps and
// returning a concrete type, with no interface in the package, composes.
func TestGenerateCompose_ConstructibleWithoutInterfaceComposes(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "orders", "orders", "")
	writeContract(t, dir, "internal/client", `package client

type Deps struct{}

type Client struct{}

func New(d Deps) *Client { return &Client{} }
`)

	if err := GenerateCompose(composeInput(t, dir, "client")); err != nil {
		t.Fatalf("a constructor compose.go can call must compose: %v", err)
	}
	if out := readInject(t, dir); !containsNormalized(out, "client.New(client.Deps{") {
		t.Fatalf("compose.go must construct the package:\n%s", out)
	}
}

func TestComposeConstructorProblems(t *testing.T) {
	cases := []struct {
		name string
		src  string // contract.go; "" leaves the directory absent
		want []string
	}{
		{name: "canonical", src: "type Service interface{}\ntype Deps struct{}\nfunc New(d Deps) Service { return nil }\n"},
		{name: "fallible", src: "type Service interface{}\ntype Deps struct{}\nfunc New(d Deps) (Service, error) { return nil, nil }\n"},
		{name: "concrete return", src: "type Deps struct{}\ntype C struct{}\nfunc New(Deps) *C { return nil }\n"},
		{name: "variadic tail", src: "type Deps struct{}\ntype Opt func()\nfunc New(d Deps, opts ...Opt) int { return 0 }\n"},
		{name: "marked constructor", src: "type Deps struct{}\n// forge:constructor\nfunc Open(d Deps) int { return 0 }\n"},
		{name: "constructor is a var", src: "type Deps struct{}\nvar New = func(d Deps) int { return 0 }\n"},
		{name: "directory absent"},
		{name: "does not parse", src: "type Deps struct{\n"},
		{
			name: "no Deps",
			src:  "type Service interface{}\nfunc New() Service { return nil }\n",
			want: []string{"declares no `type Deps struct`, and compose.go calls p.New(p.Deps{…})", "so its only parameter must be `Deps`"},
		},
		{
			name: "Deps is an interface",
			src:  "type Deps interface{}\nfunc New(d Deps) int { return 0 }\n",
			want: []string{"p/contract.go:3: `Deps` is an interface"},
		},
		{
			name: "no constructor",
			src:  "type Deps struct{}\n",
			want: []string{"declares no constructor: no `func New`"},
		},
		{
			name: "wrong parameter",
			src:  "type Deps struct{}\nfunc New(x int) int { return x }\n",
			want: []string{"p/contract.go:4: `New` is `func(x int) int` — compose.go calls p.New(p.Deps{…}), so its only parameter must be `Deps`"},
		},
		{
			name: "extra parameter",
			src:  "type Deps struct{}\nfunc New(d Deps, x int) int { return x }\n",
			want: []string{"`New` is `func(d Deps, x int) int` — compose.go calls p.New(p.Deps{…}), so its only parameter must be `Deps`"},
		},
		{
			name: "returns nothing",
			src:  "type Deps struct{}\nfunc New(d Deps) {}\n",
			want: []string{"it returns nothing"},
		},
		{
			name: "second result not error",
			src:  "type Deps struct{}\nfunc New(d Deps) (int, int) { return 0, 0 }\n",
			want: []string{"its second result is not `error`"},
		},
		{
			name: "three results",
			src:  "type Deps struct{}\nfunc New(d Deps) (int, int, error) { return 0, 0, nil }\n",
			want: []string{"it returns 3 values"},
		},
		{
			name: "generic",
			src:  "type Deps struct{}\nfunc New[T any](d Deps) int { return 0 }\n",
			want: []string{"it has type parameters"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projectDir := t.TempDir()
			dir := filepath.Join(projectDir, "p")
			if tc.src != "" {
				writeContract(t, projectDir, "p", "package p\n\n"+tc.src)
			}
			got := composeConstructorProblems(projectDir, dir, "p")
			if len(tc.want) == 0 {
				if len(got) != 0 {
					t.Fatalf("want no problems, got %q", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("want %d problem(s), got %d: %q", len(tc.want), len(got), got)
			}
			for i, want := range tc.want {
				if !strings.Contains(got[i], want) {
					t.Fatalf("problem %d = %q, want substring %q", i, got[i], want)
				}
			}
		})
	}
}
