package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// The born contract_test.go must survive the polish its own sibling
// recommends.
//
// service.go.tmpl tells the author, in the doc comment on New: "Scaffolds
// ship with a no-error path; polish the body when you grow dep validation or
// eager initialization." Take that advice — add one required dep and the
// nil-check that rejects it — and the contract_test.go forge scaffolded
// alongside it goes red on `New(Deps{})`, a call the author never wrote and
// an empty Deps the scaffold never asked them to fill. Two shipped templates,
// one telling you to do the thing the other cannot survive.
//
// This is the DOGFOOD §4 rule in its literal form: anything scaffold-once
// must survive the constraints forge's own output recommends, not just the
// ones applied at birth.
//
// The guard compiles and RUNS the rendered scaffold against a package whose
// New validates its deps. Grepping the template for a line order would pass
// on a template that no longer compiles; only running it proves the shape.
//
// The second subtest is the other half of the contract: fixing this by making
// the born test unable to fail would be worse than the bug. With a case in
// the table and a New that genuinely refuses to construct, the test must
// still go red.

// depsValidatingFixture is a package that took service.go.tmpl's advice:
// one required dep, and a New that refuses to construct without it.
var depsValidatingFixture = map[string]string{
	"contract.go": `package widgets

import "context"

// Service is the widgets package boundary.
type Service interface {
	Count(ctx context.Context) (int, error)
}

// Store is this package's persistence boundary.
type Store interface {
	Count(ctx context.Context) (int, error)
}
`,
	"service.go": `package widgets

import (
	"context"
	"errors"
)

// Deps holds dependencies for the widgets package.
type Deps struct {
	Store Store
}

type service struct {
	deps Deps
}

// New grew the dep validation service.go.tmpl's own comment recommends.
func New(deps Deps) (Service, error) {
	if deps.Store == nil {
		return nil, errors.New("widgets: Deps.Store is required")
	}
	return &service{deps: deps}, nil
}

func (s *service) Count(ctx context.Context) (int, error) {
	return s.deps.Store.Count(ctx)
}
`,
}

// caseTableAnchor is the empty ContractCase table the scaffold ships. It is
// anchored on the tdd type name rather than on surrounding prose, so a
// reworded comment does not silently stop the injection below from finding
// anything — a miss is a hard failure, not a green run against an unmodified
// file.
const caseTableAnchor = "cases := []tdd.ContractCase{"

func TestBornContractTestSurvivesDepValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test on a scaffolded module; runs in task test")
	}
	t.Parallel()

	rendered, err := InternalPkgTemplates().Render("contract_test.go.tmpl", map[string]string{
		"Name":       "widgets",
		"ImportPath": "widgets",
		"Module":     "example.com/proj",
	})
	if err != nil {
		t.Fatalf("render contract_test.go.tmpl: %v", err)
	}
	born := string(rendered)
	if !strings.Contains(born, caseTableAnchor) {
		t.Fatalf("rendered contract_test.go no longer contains %q — retarget this guard at wherever the case table moved:\n%s",
			caseTableAnchor, born)
	}

	t.Run("empty table skips without constructing", func(t *testing.T) {
		t.Parallel()
		out, err := runBornContractTest(t, born)
		if err != nil {
			t.Fatalf("the born contract_test.go must pass (by skipping) in a package whose New validates its deps.\n"+
				"service.go.tmpl tells the author to grow exactly that validation; a scaffold that cannot survive its\n"+
				"sibling's advice is two templates contradicting each other.\ngo test: %v\n%s", err, out)
		}
		if !strings.Contains(out, "SKIP") {
			t.Errorf("expected the empty case table to SKIP; got:\n%s", out)
		}
	})

	t.Run("populated table still fails when New refuses", func(t *testing.T) {
		t.Parallel()
		// One real row, using only identifiers the scaffold's own imports
		// already cover, so the table is non-empty and the skip cannot fire.
		withCase := strings.Replace(born, caseTableAnchor,
			caseTableAnchor+"\n\t\t{Name: \"constructs\", Call: func() (any, error) { return svc, nil }},", 1)

		out, err := runBornContractTest(t, withCase)
		if err == nil {
			t.Fatalf("a populated case table against a New that refuses to construct MUST fail — "+
				"a born test that cannot go red reports green forever.\n%s", out)
		}
		if !strings.Contains(out, "Deps.Store is required") {
			t.Errorf("expected the failure to name the dep-validation error New returned; got:\n%s", out)
		}
	})
}

// runBornContractTest writes a throwaway module containing the
// dep-validating fixture plus contractTest, and runs `go test` on it.
// Returns the combined output and the command error.
func runBornContractTest(t *testing.T, contractTest string) (string, error) {
	t.Helper()

	// The forge module ROOT: pkg/* are packages inside it, so a replace
	// targeting pkg/ would name a directory with no go.mod.
	forgeRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("locate the forge module root: %v", err)
	}

	root := t.TempDir()
	pkgDir := filepath.Join(root, "internal", "widgets")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range depsValidatingFixture {
		if err := os.WriteFile(filepath.Join(pkgDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "contract_test.go"), []byte(contractTest), 0o644); err != nil {
		t.Fatalf("write contract_test.go: %v", err)
	}

	// The go directive is read off forge's own go.mod: a main module below
	// its dependency's language version does not build, and hardcoding a
	// version here would rot the day forge bumps.
	goMod := "module example.com/proj\n\n" +
		"go " + goDirectiveOf(t, filepath.Join(forgeRoot, "go.mod")) + "\n\n" +
		"require github.com/reliant-labs/forge v0.0.0\n\n" +
		"replace github.com/reliant-labs/forge => " + forgeRoot + "\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	cmd := exec.Command("go", "test", "-v", "./internal/widgets/")
	cmd.Dir = root
	// -mod=mod lets the throwaway module materialise its own go.sum;
	// GOWORK=off keeps any ambient workspace out of a module that resolves
	// forge through an explicit replace.
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

var goDirectiveRe = regexp.MustCompile(`(?m)^go\s+(\S+)\s*$`)

func goDirectiveOf(t *testing.T, goModPath string) string {
	t.Helper()
	b, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatalf("read %s: %v", goModPath, err)
	}
	m := goDirectiveRe.FindSubmatch(b)
	if m == nil {
		t.Fatalf("no go directive in %s", goModPath)
	}
	return string(m[1])
}
