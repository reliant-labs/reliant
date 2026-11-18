package depsurface

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixture writes a module whose tools handler takes a 12-method foreign
// interface and calls two of its methods, one through a local.
func writeFixture(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("loads a fixture module through go/packages (a `go list` child process, several seconds); runs in task test")
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fx\n\ngo 1.22\n",
		"internal/store/store.go": `package store

import "context"

type Daemon struct{ ID string }

// Repository is the wide repository every handler used to take.
type Repository interface {
	Get(ctx context.Context, id string) (*Daemon, error)
	Put(ctx context.Context, d *Daemon) error
	A()
	B()
	C()
	D()
	E()
	F()
	G()
	H()
	I()
	J()
}

// Impl is the one implementation.
type Impl struct{}

func (*Impl) Get(context.Context, string) (*Daemon, error) { return nil, nil }
func (*Impl) Put(context.Context, *Daemon) error            { return nil }
func (*Impl) A()                                             {}
func (*Impl) B()                                             {}
func (*Impl) C()                                             {}
func (*Impl) D()                                             {}
func (*Impl) E()                                             {}
func (*Impl) F()                                             {}
func (*Impl) G()                                             {}
func (*Impl) H()                                             {}
func (*Impl) I()                                             {}
func (*Impl) J()                                             {}
`,
		"pkg/config/config.go": "package config\n\ntype Config struct{ Port int }\n",
		"internal/handlers/tools/service.go": `package tools

import (
	"context"
	"log/slog"

	"example.com/fx/internal/store"
	"example.com/fx/pkg/config"
)

type Deps struct {
	Repo   store.Repository
	Other  store.Repository
	Logger *slog.Logger
	Config *config.Config
	Bag    any
}

type Service struct{ deps Deps }

func New(d Deps) (*Service, error) {
	if d.Repo == nil {
		return nil, nil
	}
	return &Service{deps: d}, nil
}

func (s *Service) Lookup(ctx context.Context, id string) (*store.Daemon, error) {
	return s.deps.Repo.Get(ctx, id)
}

func (s *Service) Save(ctx context.Context, d *store.Daemon) error {
	r := s.deps.Repo
	s.deps.Logger.Info("save")
	_ = s.deps.Config.Port
	return r.Put(ctx, d)
}

func (s *Service) Raw() *store.Impl {
	impl, _ := s.deps.Other.(*store.Impl)
	return impl
}
`,
		"internal/handlers/tools/env.go": `package tools

import (
	stdos "os"
)

type os struct{}

func (os) Getenv(string) string { return "" }

func region() string {
	if v, ok := stdos.LookupEnv("REGION"); ok {
		return v
	}
	return os{}.Getenv("not the process env")
}
`,
	}
	for name, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func fieldNamed(t *testing.T, p *Package, name string) Field {
	t.Helper()
	for _, f := range p.Fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no Deps field %s", name)
	return Field{}
}

func TestAnalyze_UsedMethodsEscapesContainersAndDowncasts(t *testing.T) {
	dir := writeFixture(t)
	dirs, err := FindComponentDirs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(dirs, ",") != "internal/handlers/tools" {
		t.Fatalf("component dirs = %v", dirs)
	}
	pkgs, err := Analyze(dir, dirs)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("%d packages", len(pkgs))
	}
	p := pkgs[0]
	repo := fieldNamed(t, p, "Repo")
	if !repo.Interface || !repo.Foreign || len(repo.Methods) != 12 || repo.Escapes {
		t.Fatalf("Repo = %+v", repo)
	}
	if !repo.Used["Get"] || !repo.Used["Put"] || len(repo.Used) != 2 {
		t.Fatalf("Repo used %v, want Get (through the field) and Put (through a local)", repo.Used)
	}
	if got := len(Unused(repo)); got != 10 {
		t.Fatalf("Repo unused = %d", got)
	}
	if c := fieldNamed(t, p, "Config").Container; c != "" {
		t.Errorf("the root config is no container (K3 is its own rule): %q", c)
	}
	if !fieldNamed(t, p, "Config").RootConfig || fieldNamed(t, p, "Logger").RootConfig {
		t.Errorf("RootConfig: Config=%v Logger=%v, want only Config", fieldNamed(t, p, "Config").RootConfig, fieldNamed(t, p, "Logger").RootConfig)
	}
	if len(p.EnvReads) != 1 || p.EnvReads[0].Call != "os.LookupEnv" || p.EnvReads[0].File != "internal/handlers/tools/env.go" {
		t.Errorf("env reads = %+v, want the aliased os.LookupEnv only (a local type named os is not the package)", p.EnvReads)
	}
	if c := fieldNamed(t, p, "Bag").Container; !strings.Contains(c, "any") {
		t.Errorf("Bag container = %q", c)
	}
	if c := fieldNamed(t, p, "Logger").Container; c != "" {
		t.Errorf("Logger is no container: %q", c)
	}
	if len(p.Downcasts) != 1 || p.Downcasts[0].Field != "Other" || p.Downcasts[0].Target != "*store.Impl" {
		t.Fatalf("downcasts = %+v", p.Downcasts)
	}
}

func TestNarrow_WritesTheConsumerInterfaceAndRetypesTheField(t *testing.T) {
	dir := writeFixture(t)
	res, err := Narrow(dir, "internal/handlers/tools")
	if err != nil {
		t.Fatal(err)
	}
	if res.File != "internal/handlers/tools/deps.go" || len(res.Narrowed) != 1 || res.Narrowed[0].To != "Repository" {
		t.Fatalf("result = %+v", res)
	}
	// Other is only downcast: left alone, with the reason.
	if len(res.Skipped) != 1 || res.Skipped[0].Field != "Other" || !strings.Contains(res.Skipped[0].Reason, "type-asserts") {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
	deps, err := os.ReadFile(filepath.Join(dir, res.File))
	if err != nil {
		t.Fatal(err)
	}
	// goimports-formatted with the module as the local group, as every Go
	// file forge writes: the project's format lint rejects anything else.
	if !strings.Contains(string(deps), "\t\"context\"\n\n\t\"example.com/fx/internal/store\"\n") {
		t.Errorf("deps.go's imports are not goimports-grouped (stdlib, then the module):\n%s", deps)
	}
	for _, want := range []string{DepsFileMarker, "type Repository interface {", "Get(ctx context.Context, id string) (*store.Daemon, error)", "Put(ctx context.Context, d *store.Daemon) error"} {
		if !strings.Contains(string(deps), want) {
			t.Errorf("deps.go lacks %q:\n%s", want, deps)
		}
	}
	svc, _ := os.ReadFile(filepath.Join(dir, "internal/handlers/tools/service.go"))
	if !strings.Contains(string(svc), "Repo   Repository") && !strings.Contains(string(svc), "Repo Repository") {
		t.Errorf("Deps.Repo was not retyped:\n%s", svc)
	}
	// The lint goes quiet: nothing unused through Repo any more.
	pkgs, err := Analyze(dir, []string{"internal/handlers/tools"})
	if err != nil || len(pkgs) != 1 {
		t.Fatalf("re-analyze: %v (%d packages) — the narrowed package must type-check", err, len(pkgs))
	}
	if repo := fieldNamed(t, pkgs[0], "Repo"); repo.Foreign || len(Unused(repo)) != 0 {
		t.Fatalf("after narrowing Repo = %+v", repo)
	}
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the narrowed module does not build: %v\n%s", err, out)
	}
}

// writeRolesFixture writes a module whose handler takes a 20-method foreign
// interface, calls one method itself, fills the field in a setter (a write,
// no use of it), and passes the value on: as two narrower interfaces the
// callee declares (a parameter, a struct literal field, a struct field
// assignment), as a type parameter's constraint and as a generic interface
// in a generic callee's signature. Two other fields leave
// as `any` and as a return value.
func writeRolesFixture(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("loads a fixture module through go/packages (a `go list` child process, several seconds); runs in task test")
	}
	dir := t.TempDir()
	var wide, impl strings.Builder
	for i := 1; i <= 16; i++ {
		fmt.Fprintf(&wide, "\tM%02d()\n", i)
		fmt.Fprintf(&impl, "func (*Impl) M%02d() {}\n", i)
	}
	files := map[string]string{
		"go.mod": "module example.com/fx\n\ngo 1.22\n",
		"internal/store/store.go": `package store

import "context"

type Daemon struct{ ID string }

// Repository is the 20-method repository.
type Repository interface {
	Get(ctx context.Context, id string) (*Daemon, error)
	Put(ctx context.Context, d *Daemon) error
	Lister
	Ping()
` + wide.String() + `}

// Lister is embedded in Repository.
type Lister interface{ List() []Daemon }

type Impl struct{}

func (*Impl) Get(context.Context, string) (*Daemon, error) { return nil, nil }
func (*Impl) Put(context.Context, *Daemon) error            { return nil }
func (*Impl) List() []Daemon                                { return nil }
func (*Impl) Ping()                                         {}
` + impl.String(),
		"internal/threads/threads.go": `package threads

import (
	"context"

	"example.com/fx/internal/store"
)

// Reader is the role NewService declares; it embeds store.Lister.
type Reader interface {
	Get(ctx context.Context, id string) (*store.Daemon, error)
	store.Lister
}

// Writer is the role Deps.Writer declares.
type Writer interface {
	Put(ctx context.Context, d *store.Daemon) error
}

type Service struct{ r Reader }

func NewService(r Reader) *Service { return &Service{r: r} }

type Deps struct{ Writer Writer }

func New(d Deps) *Service { return nil }

func Keep(x any) {}

func Pinger[T interface{ M01() }](t T) {}

// Box is generic: Box[T] in Boxed's signature names Boxed's own T.
type Box[T any] interface{ M02() }

func Boxed[T any](b Box[T]) {}
`,
		"internal/handlers/tools/service.go": `package tools

import (
	"example.com/fx/internal/store"
	"example.com/fx/internal/threads"
)

type Deps struct {
	Repo     store.Repository
	Bagged   store.Repository
	Returned store.Repository
}

type Service struct {
	deps   Deps
	reader threads.Reader
}

func (s *Service) Wire() {
	s.deps.Repo.Ping()
	_ = threads.NewService(s.deps.Repo)
	_ = threads.New(threads.Deps{Writer: s.deps.Repo})
	s.reader = s.deps.Repo
	threads.Pinger(s.deps.Repo)
	threads.Boxed[int](s.deps.Repo)
	threads.Keep(s.deps.Bagged)
	s.deps.Bagged.Ping()
}

// SetRepo fills the field: a write, not a use of its value.
func (s *Service) SetRepo(r store.Repository) { s.deps.Repo = r }

func (s *Service) Repo() store.Repository {
	s.deps.Returned.Ping()
	return s.deps.Returned
}
`,
	}
	for name, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A field passed on as an interface the callee declares counts that
// interface's methods as used — the union, not an exemption — while one
// that leaves as `any` or a return value is still exempt.
func TestAnalyze_PassingOnAsAnInterfaceAddsItsMethods(t *testing.T) {
	dir := writeRolesFixture(t)
	pkgs, err := Analyze(dir, []string{"internal/handlers/tools"})
	if err != nil || len(pkgs) != 1 {
		t.Fatalf("analyze: %v (%d packages)", err, len(pkgs))
	}
	repo := fieldNamed(t, pkgs[0], "Repo")
	if len(repo.Methods) != 20 || repo.Escapes {
		t.Fatalf("Repo: %d methods, Escapes=%v at %s — passing it on as an interface, or filling it, is no escape", len(repo.Methods), repo.Escapes, repo.EscapeAt)
	}
	// Ping called; Get+List as threads.Reader (List through its embedded
	// store.Lister); Put as threads.Writer; M01 as Pinger's constraint; M02
	// as Box[T].
	if got := strings.Join(sortedUsed(repo), ","); got != "Get,List,M01,M02,Ping,Put" {
		t.Errorf("Repo used = %s", got)
	}
	var roles []string
	for _, r := range repo.Roles {
		roles = append(roles, fmt.Sprintf("%s:%v", r.Type, r.Embeddable))
	}
	if got := strings.Join(roles, " "); got != "threads.Reader:true threads.Writer:true interface{ M01 }:false threads.Box[T]:false" {
		t.Errorf("Repo roles = %s (the struct field assignment is the same Reader, recorded once)", got)
	}
	for _, name := range []string{"Bagged", "Returned"} {
		if f := fieldNamed(t, pkgs[0], name); !f.Escapes || len(f.Used) != 1 {
			t.Errorf("%s = %+v, want an escape (into %s) with only Ping used", name, f, map[string]string{"Bagged": "any", "Returned": "a return value"}[name])
		}
	}
}

// narrow-deps writes the union: the callee roles embedded, the rest of the
// used methods spelled out, and the module still builds — every call that
// passes the value on type-checks against the narrowed interface.
func TestNarrow_EmbedsTheRolesTheValueIsPassedOnAs(t *testing.T) {
	dir := writeRolesFixture(t)
	res, err := Narrow(dir, "internal/handlers/tools")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Narrowed) != 1 || res.Narrowed[0].Field != "Repo" || strings.Join(res.Narrowed[0].Embeds, ",") != "threads.Reader,threads.Writer" {
		t.Fatalf("narrowed = %+v", res.Narrowed)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("skipped = %+v, want Bagged and Returned", res.Skipped)
	}
	deps, err := os.ReadFile(filepath.Join(dir, res.File))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"type Repository interface {", "\tthreads.Reader\n", "\tthreads.Writer\n", "\tM01()\n", "\tM02()\n", "\tPing()\n"} {
		if !strings.Contains(string(deps), want) {
			t.Errorf("deps.go lacks %q:\n%s", want, deps)
		}
	}
	for _, covered := range []string{"\tGet(", "\tPut(", "\tList("} {
		if strings.Contains(string(deps), covered) {
			t.Errorf("deps.go spells out %q, which an embedded role already declares:\n%s", covered, deps)
		}
	}
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the narrowed module does not build: %v\n%s\ndeps.go:\n%s", err, out, deps)
	}
	pkgs, err := Analyze(dir, []string{"internal/handlers/tools"})
	if err != nil || len(pkgs) != 1 {
		t.Fatalf("re-analyze: %v (%d packages)", err, len(pkgs))
	}
	if repo := fieldNamed(t, pkgs[0], "Repo"); repo.Foreign || len(Unused(repo)) != 0 {
		t.Fatalf("after narrowing Repo = %+v", repo)
	}
}

func TestNarrow_DepsDeclaredInContractGo(t *testing.T) {
	dir := writeFixture(t)
	svcPath := filepath.Join(dir, "internal/handlers/tools/service.go")
	svc, err := os.ReadFile(svcPath)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(svc), "type Deps struct {")
	end := start + strings.Index(string(svc)[start:], "}\n") + 2
	contract := "package tools\n\nimport (\n\t\"log/slog\"\n\n\t\"example.com/fx/internal/store\"\n\t\"example.com/fx/pkg/config\"\n)\n\n" + string(svc)[start:end]
	rest := strings.Replace(string(svc)[:start]+string(svc)[end:], "\t\"log/slog\"\n", "", 1)
	rest = strings.Replace(rest, "\t\"example.com/fx/pkg/config\"\n", "", 1)
	if err := os.WriteFile(filepath.Join(dir, "internal/handlers/tools/contract.go"), []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(svcPath, []byte(rest), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Narrow(dir, "internal/handlers/tools")
	if err != nil {
		t.Fatal(err)
	}
	if res.File != "internal/handlers/tools/contract.go" || len(res.Narrowed) != 1 {
		t.Fatalf("result = %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(dir, res.File))
	if !strings.Contains(string(got), "type Repository interface {") || !strings.Contains(string(got), "Repo   Repository") {
		t.Fatalf("contract.go was not narrowed in place:\n%s", got)
	}
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the narrowed module does not build: %v\n%s\ncontract.go:\n%s", err, out, got)
	}
}

func TestIfaceName_PrefersTheTypeThenTheFieldThenBoth(t *testing.T) {
	for _, tc := range []struct {
		name, typ, field string
		taken            []string
		want             string
		ok               bool
	}{
		{"the type's own name when free", "Promoter", "Promoter", nil, "Promoter", true},
		{"the field's name when the handler's Service takes the type's", "Service", "DaemonService", []string{"Service"}, "DaemonService", true},
		{"both joined when the field's is taken too", "Store", "Audit", []string{"Store", "Audit"}, "AuditStore", true},
		{"nothing when all three are taken", "Store", "Audit", []string{"Store", "Audit", "AuditStore"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			taken := map[string]bool{}
			for _, n := range tc.taken {
				taken[n] = true
			}
			got, ok := ifaceName(tc.typ, tc.field, taken)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("ifaceName(%q, %q, %v) = %q, %v; want %q, %v", tc.typ, tc.field, tc.taken, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// writeBloatFixture writes a module whose handler calls `used` of the 16
// methods of a foreign interface, one of them returning a third-party type, so
// the narrowed interface needs a stdlib, a third-party and a local import.
func writeBloatFixture(t *testing.T, used int, golangci string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("loads a fixture module through go/packages (a `go list` child process, several seconds); runs in task test")
	}
	var methods, calls strings.Builder
	for i := 1; i <= 16; i++ {
		fmt.Fprintf(&methods, "\tM%02d(ctx context.Context) error\n", i)
		if i <= used {
			fmt.Fprintf(&calls, "\t_ = s.deps.Repo.M%02d(ctx)\n", i)
		}
	}
	files := map[string]string{
		"go.mod":                  "module example.com/fx\n\ngo 1.22\n\nrequire other.example/lib v0.0.0\n\nreplace other.example/lib => ./third_party/lib\n",
		"third_party/lib/go.mod":  "module other.example/lib\n\ngo 1.22\n",
		"third_party/lib/lib.go":  "package lib\n\ntype Thing struct{}\n",
		"internal/store/store.go": "package store\n\nimport (\n\t\"context\"\n\t\"time\"\n\n\t\"other.example/lib\"\n)\n\ntype Row struct{}\n\ntype Repository interface {\n" + methods.String() + "\tWhen() time.Time\n\tThing() lib.Thing\n\tRow() Row\n}\n",
		"internal/handlers/tools/service.go": `package tools

import (
	"context"
	"time"

	"example.com/fx/internal/store"
	"other.example/lib"
)

type Deps struct {
	Repo store.Repository
}

type Service struct{ deps Deps }

func (s *Service) Run(ctx context.Context) (time.Time, lib.Thing, store.Row) {
` + calls.String() + `	return s.deps.Repo.When(), s.deps.Repo.Thing(), s.deps.Repo.Row()
}
`,
	}
	if golangci != "" {
		files[".golangci.yml"] = golangci
	}
	dir := t.TempDir()
	for name, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestNarrow_ImportsAreGoimportsGrouped(t *testing.T) {
	dir := writeBloatFixture(t, 3, "")
	res, err := Narrow(dir, "internal/handlers/tools")
	if err != nil {
		t.Fatal(err)
	}
	const want = "import (\n\t\"context\"\n\t\"time\"\n\n\t\"other.example/lib\"\n\n\t\"example.com/fx/internal/store\"\n)"
	for _, f := range []string{res.File, "internal/handlers/tools/service.go"} {
		got, _ := os.ReadFile(filepath.Join(dir, f))
		if !strings.Contains(string(got), want) {
			t.Errorf("%s imports are not stdlib / third-party / local grouped:\n%s", f, got)
		}
	}
}

func TestNarrow_ImportsHonourGolangciLocalPrefixes(t *testing.T) {
	cfg := "version: \"2\"\nformatters:\n  enable:\n    - goimports\n  settings:\n    goimports:\n      local-prefixes:\n        - other.example\n"
	dir := writeBloatFixture(t, 3, cfg)
	res, err := Narrow(dir, "internal/handlers/tools")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, res.File))
	const want = "\t\"example.com/fx/internal/store\"\n\n\t\"other.example/lib\"\n)"
	if !strings.Contains(string(got), want) {
		t.Errorf("other.example is the configured local prefix and goes last:\n%s", got)
	}
}

func TestNarrow_WarnsWhenTheNarrowedInterfaceExceedsInterfacebloat(t *testing.T) {
	cfg := "version: \"2\"\nlinters:\n  enable:\n    - interfacebloat\n  settings:\n    interfacebloat:\n      max: 12\n"
	// 13 calls + When + Thing + Row = 16 methods > 12.
	dir := writeBloatFixture(t, 13, cfg)
	res, err := Narrow(dir, "internal/handlers/tools")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Bloated) != 1 {
		t.Fatalf("bloat warnings = %+v", res.Bloated)
	}
	b := res.Bloated[0]
	if b.Interface != "Repository" || b.Methods != 16 || b.Limit != 12 || !strings.Contains(b.LimitSource, ".golangci.yml") || !b.Suppressed {
		t.Errorf("warning = %+v", b)
	}
	if msg := b.String(); !strings.Contains(msg, "nolint:interfacebloat") || !strings.Contains(msg, "split the package") {
		t.Errorf("message lacks the remedy: %s", msg)
	}
	// The project runs interfacebloat, so the type carries the reasoned
	// directive — directly above it, where golangci-lint reads it — and the
	// interface is NOT split into embedded roles to dodge the count.
	deps, _ := os.ReadFile(filepath.Join(dir, res.File))
	const directive = "//nolint:interfacebloat // forge fix narrow-deps: exactly the 16 of store.Repository's 19 methods this package calls through Deps.Repo, one dependency; shrink it by splitting the package, not the interface.\ntype Repository interface {\n\tM01("
	if !strings.Contains(string(deps), directive) {
		t.Errorf("deps.go lacks the reasoned directive above the unsplit type:\n%s", deps)
	}

	// Under the limit: quiet, and no directive.
	dir = writeBloatFixture(t, 3, cfg)
	if res, err = Narrow(dir, "internal/handlers/tools"); err != nil || len(res.Bloated) != 0 {
		t.Fatalf("6 methods under 12: %+v, %v", res, err)
	}
	if deps, _ := os.ReadFile(filepath.Join(dir, res.File)); strings.Contains(string(deps), "nolint") {
		t.Errorf("an interface under the limit carries a directive:\n%s", deps)
	}
}

func TestNarrow_BloatLimitDefaultsToGolangcis(t *testing.T) {
	dir := writeBloatFixture(t, 8, "") // 8 + 3 = 11 > 10
	res, err := Narrow(dir, "internal/handlers/tools")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Bloated) != 1 || res.Bloated[0].Limit != 10 || !strings.Contains(res.Bloated[0].LimitSource, "default") || res.Bloated[0].Suppressed {
		t.Fatalf("warnings = %+v", res.Bloated)
	}
	// No config runs interfacebloat: warned, never suppressed (an unused
	// directive is noise nolintlint would flag).
	if deps, _ := os.ReadFile(filepath.Join(dir, res.File)); strings.Contains(string(deps), "nolint") {
		t.Errorf("a directive for a linter the project does not run:\n%s", deps)
	}
}

func TestInterfaceBloatLimit(t *testing.T) {
	for _, tc := range []struct {
		name, cfg  string
		limit      int
		enabled    bool
		fromConfig bool
	}{
		{"v2 enabled", "version: \"2\"\nlinters:\n  enable: [interfacebloat]\n  settings:\n    interfacebloat:\n      max: 12\n", 12, true, true},
		{"v2 enabled, default max", "version: \"2\"\nlinters:\n  enable: [interfacebloat]\n", 10, true, true},
		{"v2 not enabled", "version: \"2\"\nlinters:\n  enable: [errcheck]\n  settings:\n    interfacebloat:\n      max: 12\n", 12, false, true},
		{"v2 default all", "version: \"2\"\nlinters:\n  default: all\n  disable: [errcheck]\n", 10, true, true},
		{"v2 default all, disabled", "version: \"2\"\nlinters:\n  default: all\n  disable: [interfacebloat]\n", 10, false, true},
		{"v1", "linters:\n  enable: [interfacebloat]\nlinters-settings:\n  interfacebloat:\n    max: 7\n", 7, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".golangci.yml"), []byte(tc.cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			got := readLintConfig(dir)
			if !got.found || got.interfaceBloatMax != tc.limit || got.interfaceBloatOn != tc.enabled {
				t.Errorf("readLintConfig = %+v, want limit %d enabled %v", got, tc.limit, tc.enabled)
			}
		})
	}
	if got := readLintConfig(t.TempDir()); got.found || got.interfaceBloatMax != 10 || !got.interfaceBloatOn {
		t.Errorf("no config = %+v, want golangci's default 10, advisory", got)
	}
}
