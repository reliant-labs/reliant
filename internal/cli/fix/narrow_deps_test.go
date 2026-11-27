package fix

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/derivecontract"
	"github.com/reliant-labs/forge/pkg/testkit"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, src := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// `forge fix narrow-deps <dir>` retypes a foreign wide Deps interface to the
// calls the package makes, writes its mock, and says what it did; a package
// whose interfaces are all used in full reports nothing to narrow.
func TestFix_NarrowDeps(t *testing.T) {
	testkit.Slow(t, "type-checks the fixture module with go/packages (execs go list)")
	t.Setenv("GOWORK", "off") // the fixture is its own module
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"forge.yaml": "name: app\n",
		"go.mod":     "module example.com/fx\n\ngo 1.22\n",
		"internal/store/store.go": `package store

import "context"

// Repository is the wide repository every handler used to take.
type Repository interface {
	Get(ctx context.Context, id string) (string, error)
	Put(ctx context.Context, id string) error
	A()
	B()
	C()
}

type Impl struct{}

func (*Impl) Get(context.Context, string) (string, error) { return "", nil }
func (*Impl) Put(context.Context, string) error            { return nil }
func (*Impl) A()                                           {}
func (*Impl) B()                                           {}
func (*Impl) C()                                           {}
`,
		"internal/handlers/tools/service.go": `package tools

import (
	"context"

	"example.com/fx/internal/store"
)

type Deps struct {
	Repo  store.Repository
	Other store.Repository
}

type Service struct{ deps Deps }

func New(d Deps) *Service { return &Service{deps: d} }

func (s *Service) Lookup(ctx context.Context, id string) (string, error) {
	return s.deps.Repo.Get(ctx, id)
}

func (s *Service) Raw() *store.Impl {
	impl, _ := s.deps.Other.(*store.Impl)
	return impl
}
`,
		"internal/handlers/lean/service.go": `package lean

type Deps struct{ Name string }
`,
	})
	t.Chdir(root)

	run := func(args ...string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		cmd := newCmd(&factory.Factory{Out: &out})
		cmd.SetArgs(append([]string{"narrow-deps"}, args...))
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := cmd.Execute()
		return out.String(), err
	}

	out, err := run("./internal/handlers/tools")
	if err != nil {
		t.Fatalf("narrow-deps: %v\n%s", err, out)
	}
	for _, want := range []string{
		"⏭️  Deps.Other (", // only downcast: left alone, with the reason
		"✅ Deps.Repo:",     // narrowed
		"1 method(s): Get", // exactly the one method it calls
		"declared in internal/handlers/tools/deps.go, mocked in internal/handlers/tools/mock_gen.go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	deps, err := os.ReadFile(filepath.Join(root, "internal/handlers/tools/deps.go"))
	if err != nil {
		t.Fatalf("deps.go not written: %v", err)
	}
	if !strings.Contains(string(deps), "Get(ctx context.Context, id string) (string, error)") || strings.Contains(string(deps), "Put(") {
		t.Errorf("deps.go must hold exactly the called method:\n%s", deps)
	}
	if _, err := os.Stat(filepath.Join(root, "internal/handlers/tools/mock_gen.go")); err != nil {
		t.Errorf("the narrowed interface's mock was not written: %v", err)
	}

	if out, err := run("internal/handlers/lean"); err != nil || !strings.Contains(out, "internal/handlers/lean: nothing to narrow") {
		t.Fatalf("package with no foreign interfaces: err=%v\n%s", err, out)
	}

	if _, err := run("internal/handlers/missing"); err == nil || !strings.Contains(err.Error(), "no component package with a Deps struct") {
		t.Fatalf("missing package: err=%v, want the 'no component package' error", err)
	}
}

// writeDerived reports a checksums state file it cannot read as "package
// rewritten, contract_gen.go not written", naming the next step, instead of
// leaving the user with a half-migrated package and no hint.
func TestWriteDerived_UnreadableStateFileIsReportedWithTheNextStep(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{".forge/disowned.json": "{not json"})
	var out bytes.Buffer
	err := writeDerived(&out, root, &derivecontract.Migration{Dir: "internal/billing", Type: "service"})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"the package is rewritten, but contract_gen.go was not written", "run `forge generate`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if out.Len() != 0 {
		t.Errorf("nothing should be reported as done, got %q", out.String())
	}
}
