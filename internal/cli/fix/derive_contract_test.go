package fix

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// `forge fix derive-contract <dir>`: --dry-run reports and writes nothing; a
// run rewrites the package and writes contract_gen.go at once; a package
// that cannot derive its contract is refused with the reason.
func TestFix_DeriveContract(t *testing.T) {
	testkit.Slow(t, "type-checks the package with go/packages (execs go list)")
	t.Setenv("GOWORK", "off") // the fixture is its own module
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "billing")
	files := map[string]string{
		"forge.yaml": "name: app\n",
		"go.mod":     "module example.com/app\n\ngo 1.22\n",
		"internal/billing/contract.go": `package billing

import "context"

// Service charges customers.
type Service interface {
	// Charge bills a customer.
	Charge(ctx context.Context, id string) error
}

// Store persists charges.
type Store interface {
	Save(ctx context.Context, id string) error
}
`,
		"internal/billing/service.go": `package billing

import "context"

type Deps struct{ Store Store }

type service struct{ deps Deps }

func (s *service) Charge(ctx context.Context, id string) error { return s.deps.Store.Save(ctx, id) }
`,
		"internal/twice/contract.go": "package twice\n\ntype Service interface {\n\tRun() error\n}\n",
		"internal/twice/a.go":        "package twice\n\ntype a struct{}\n\nfunc (a) Run() error { return nil }\n\ntype b struct{}\n\nfunc (b) Run() error { return nil }\n",
	}
	for rel, src := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	run := func(args ...string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		cmd := newCmd(&factory.Factory{Out: &out})
		cmd.SetArgs(append([]string{"derive-contract"}, args...))
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := cmd.Execute()
		return out.String(), err
	}

	out, err := run("internal/billing", "--dry-run")
	if err != nil || !strings.Contains(out, "would rewrite: internal/billing/service.go: //forge:contract on service") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "contract.go")); err != nil {
		t.Fatalf("--dry-run changed the package: %v", err)
	}

	out, err = run("./internal/billing")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for _, want := range []string{"internal/billing/deps.go: created with Store", "internal/billing/contract_gen.go: derived from service", "Next: run `forge generate`"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	gen, err := os.ReadFile(filepath.Join(dir, "contract_gen.go"))
	if err != nil {
		t.Fatalf("contract_gen.go not written: %v", err)
	}
	if !strings.Contains(string(gen), "\t// Charge bills a customer.\n\tCharge(ctx context.Context, id string) error") {
		t.Errorf("contract_gen.go:\n%s", gen)
	}
	if _, err := os.Stat(filepath.Join(dir, "contract.go")); !os.IsNotExist(err) {
		t.Errorf("contract.go should be removed")
	}

	out, err = run("internal/twice")
	if err == nil || !strings.Contains(out, "2 types implement Service") {
		t.Errorf("a two-implementation package must be refused: %v\n%s", err, out)
	}
	if _, serr := os.Stat(filepath.Join(root, "internal", "twice", "contract.go")); serr != nil {
		t.Errorf("a refused package was rewritten: %v", serr)
	}
	if _, err := run("internal/nope"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("a missing directory must fail: %v", err)
	}
}
