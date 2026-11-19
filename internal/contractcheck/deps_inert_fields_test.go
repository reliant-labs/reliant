package contractcheck

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestLintDepsAreInterfaces_InertFieldsAreData is the second half of the
// control-plane false positive at internal/operators/workspace/operator.go:78,
// and the half that the purity walk cannot reach at all.
//
// `*config.WorkspaceConfig` is a bag of eighteen scalars, two probe blocks and
// a couple of string maps. Its docker-storage accessor was rewritten to derive
// that ladder from the data ladder, which routes through
// `resource.ParseQuantity` — a pure value parser in k8s apimachinery. The
// method-purity test cannot clear that without type-checking a third-party
// module, so the rule fired on a struct that plainly holds nothing.
//
// Asking what the struct HOLDS answers it in one step. A collaborator's
// defining property is that it holds something — a handle, a client, a pool —
// and a type whose every field is a primitive, a stdlib value, or a collection
// of those has no field a test could want to substitute. What its accessors
// compute on the way out is then irrelevant.
//
// The fixture is the real shape: scalars, a string map, a nested all-scalar
// probe block, and an accessor that derives one ladder from another through a
// third-party parser.
func TestLintDepsAreInterfaces_InertFieldsAreData(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	must(t, writeFile(filepath.Join(tmp, "go.mod"), "module example.com/app\n\ngo 1.24\n"))

	// A stand-in for k8s apimachinery's resource package: a third-party
	// value parser the purity walk cannot follow.
	resDir := filepath.Join(tmp, "vendorish", "resource")
	must(t, mkdirAll(resDir))
	must(t, writeFile(filepath.Join(resDir, "resource.go"), `package resource

type Quantity struct{ v int64 }

func ParseQuantity(s string) (Quantity, error) { return Quantity{}, nil }
func (q Quantity) Value() int64                { return q.v }
`))

	cfgDir := filepath.Join(tmp, "internal", "config")
	must(t, mkdirAll(cfgDir))
	must(t, writeFile(filepath.Join(cfgDir, "workspace_config.go"), `package config

import "example.com/app/vendorish/resource"

// ProbeConfig is a nested all-scalar block — followed, and itself inert.
type ProbeConfig struct {
	Command             []string
	InitialDelaySeconds int32
	FailureThreshold    int32
}

// WorkspaceConfig holds only scalars, string maps and a nested scalar
// block. There is no field here that could hold a database or a client.
type WorkspaceConfig struct {
	DefaultStorageSize string
	RunAsNonRoot       bool
	WorkspaceFSGroup   int64
	DaemonStorageSizes map[string]string
	LivenessProbe      ProbeConfig
}

func (c *WorkspaceConfig) StorageSizeForTier(tier string) string {
	if size, ok := c.DaemonStorageSizes[tier]; ok && size != "" {
		return size
	}
	return c.DefaultStorageSize
}

// DockerStorageSizeForTier derives one ladder from the other through a
// third-party parser. No purity walk can clear this; the fields can.
func (c *WorkspaceConfig) DockerStorageSizeForTier(tier string) string {
	q, err := resource.ParseQuantity(c.StorageSizeForTier(tier))
	if err != nil {
		return ""
	}
	if q.Value() > 0 {
		return "5Gi"
	}
	return ""
}
`))

	opDir := filepath.Join(tmp, "internal", "workspaceop")
	must(t, mkdirAll(opDir))
	must(t, writeFile(filepath.Join(opDir, "contract.go"), `package workspaceop

type Service interface{ Do() error }
`))
	must(t, writeFile(filepath.Join(opDir, "operator.go"), `package workspaceop

import "example.com/app/internal/config"

type Deps struct {
	WorkspaceConfig *config.WorkspaceConfig // inert fields — silent
}

type service struct{ deps Deps }

func New(d Deps) Service { return &service{deps: d} }
`))

	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got := findingsForRule(fs, string(RuleDepsAreInterfaces)); len(got) != 0 {
		t.Fatalf("a struct whose every field is a primitive, a stdlib value or a collection "+
			"of those holds nothing a test could fake — what its accessors compute cannot "+
			"make it a collaborator. got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
}

// TestLintDepsAreInterfaces_HeldCollaboratorsStillFire is the guard on the
// inert-fields route, and the reason that route is narrow.
//
// The route's whole claim is that a collaborator has to HOLD its handle
// somewhere. Each type below is a way of holding one, or of reaching one
// without a field, and every one must still fire:
//
//   - Pool — holds a pointer. A *T is how a struct holds a shared mutable
//     thing, and no syntactic test here distinguishes *Tier from *sql.DB, so
//     no pointer field is inert.
//   - Runner — holds a func. That is a collaborator with the struct keyword
//     omitted.
//   - Piper — holds a channel.
//   - Wrapped — holds an interface field, which is a substitution point that
//     says a real implementation goes here.
//   - Reacher — holds only a string, and would pass the field test, but its
//     accessor indexes a package-level map. Inert fields say what a type
//     holds and nothing about what it reaches; this is the companion clause.
//   - Deep — holds a nested struct that itself holds a pointer, so the
//     follow-through must not stop at the first level.
//
// What is deliberately NOT here: a type holding an unrecognised VALUE type
// (say `sql.NullString`). That is neither provably inert nor provably a
// handle, so it falls through to the method-purity walk and is judged on its
// bodies. Pinning it either way would be asserting a classification this rule
// does not have — the two field tests are narrow converses, not a partition,
// and the gap between them is where the purity walk still does the work.
func TestLintDepsAreInterfaces_HeldCollaboratorsStillFire(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	must(t, writeFile(filepath.Join(tmp, "go.mod"), "module example.com/app\n\ngo 1.24\n"))

	cfgDir := filepath.Join(tmp, "internal", "config")
	must(t, mkdirAll(cfgDir))
	must(t, writeFile(filepath.Join(cfgDir, "config.go"), `package config

import "database/sql"

var shared = map[string]string{}

type Inner struct{ DB *sql.DB }

// Pool holds a pointer — the ordinary way to hold a handle.
type Pool struct{ db *sql.DB }

func (p *Pool) Name() string { return "pool" }

// Runner holds a func: a collaborator with the struct keyword omitted.
type Runner struct{ fn func(string) string }

func (r *Runner) Name() string { return "runner" }

// Piper holds a channel.
type Piper struct{ ch chan string }

func (p *Piper) Name() string { return "piper" }

// Wrapped holds an interface — an explicit substitution point.
type Wrapped struct{ inner interface{ Do() error } }

func (w *Wrapped) Name() string { return "wrapped" }

// Reacher's fields are inert; its accessor reads package-level state.
type Reacher struct{ prefix string }

func (r *Reacher) Lookup(k string) string { return shared[r.prefix+k] }

// Deep holds a nested struct that itself holds a pointer.
type Deep struct{ inner Inner }

func (d *Deep) Name() string { return "deep" }
`))

	appDir := filepath.Join(tmp, "internal", "orders")
	must(t, mkdirAll(appDir))
	must(t, writeFile(filepath.Join(appDir, "contract.go"), `package orders

type Service interface{ Do() error }
`))
	must(t, writeFile(filepath.Join(appDir, "service.go"), `package orders

import "example.com/app/internal/config"

type Deps struct {
	Pool    *config.Pool    // holds a pointer
	Runner  *config.Runner  // holds a func
	Piper   *config.Piper   // holds a channel
	Wrapped *config.Wrapped // holds an interface
	Reacher *config.Reacher // inert fields, but reaches package state
	Deep    *config.Deep    // nested struct holding a pointer
}

type service struct{ deps Deps }

func New(d Deps) Service { return &service{deps: d} }
`))

	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleDepsAreInterfaces))
	if len(got) != 6 {
		t.Fatalf("the inert-fields route is a discriminator, not an escape hatch — a "+
			"collaborator has to hold its handle somewhere, and every way of holding one "+
			"must still fire. expected 6, got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
	var joined string
	for _, f := range got {
		joined += f.Message + "\n"
	}
	for _, want := range []string{`"Pool"`, `"Runner"`, `"Piper"`, `"Wrapped"`, `"Reacher"`, `"Deep"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected a finding for %s; got:\n%s", want, joined)
		}
	}
}
