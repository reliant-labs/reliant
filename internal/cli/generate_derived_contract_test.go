package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/goexec"
)

// derivedContractPackage is a contract package in the marked-struct form:
// no contract.go, the Service derived from `//forge:contract` on the
// implementation, a dependency interface in a `// forge:deps` deps.go, and a
// method referencing a package-local type and an aliased import.
var derivedContractPackage = map[string]string{
	"service.go": `package email

import (
	"context"
	stdtime "time"
)

// Deps is what the email service needs.
type Deps struct {
	Store Store
}

// service sends mail.
//
//forge:contract
type service struct{ deps Deps }

// New builds the email service.
//
// forge:constructor
func New(deps Deps) (Service, error) { return &service{deps: deps}, nil }

// Send delivers one message.
func (s *service) Send(ctx context.Context, msg Message) error {
	return s.deps.Store.Save(ctx, msg.To)
}

// Schedule reports when the next batch goes out.
func (s service) Schedule(_ context.Context, every stdtime.Duration) (stdtime.Time, error) {
	return stdtime.Time{}.Add(every), nil
}

func (s *service) unexported() {}

// Message is one outgoing email.
type Message struct{ To string }
`,
	"deps.go": "// forge:deps — consumer-declared interfaces for this package's Deps.\npackage email\n\nimport \"context\"\n\n// Store records what was sent.\ntype Store interface {\n\tSave(ctx context.Context, to string) error\n}\n",
}

func writeDerivedContractProject(t *testing.T, root string) string {
	t.Helper()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/proj\n\ngo 1.24\n")
	pkg := filepath.Join(root, "internal", "email")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, src := range derivedContractPackage {
		mustWrite(t, filepath.Join(pkg, name), src)
	}
	return pkg
}

// TestGenerate_DerivesTheContractFromAMarkedStruct: `//forge:contract` on the
// implementation makes the contracts step write contract_gen.go (the Service
// interface), then mock_gen.go (Service AND the deps.go interface) and
// middleware_gen.go from it, exactly as from a hand-written contract.go — and
// the package is a component the instrument decision covers. Removing the
// marker removes contract_gen.go. In full mode the result compiles.
func TestGenerate_DerivesTheContractFromAMarkedStruct(t *testing.T) {
	// Resolved before the test changes the working directory.
	forgeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	root := "."
	pkg := writeDerivedContractProject(t, root)
	cfg := &config.ProjectConfig{Name: "proj"}

	if !codegen.ShouldInstrumentComponent(pkg, "Service", codegen.DetectServiceInterfaceName(pkg)) {
		t.Fatal("a marked-struct package is not instrumented")
	}
	if !codegen.IsWiredComponentDir(pkg) {
		t.Fatal("a marked-struct package is not a component")
	}

	cs := &checksums.FileChecksums{}
	if err := generateInternalPackageContracts(root, cfg, cs); err != nil {
		t.Fatalf("generateInternalPackageContracts: %v", err)
	}

	derived := readFile(t, filepath.Join(pkg, "contract_gen.go"))
	for _, want := range []string{
		"type Service interface {",
		"\t// Send delivers one message.\n\tSend(ctx context.Context, msg Message) error",
		"Schedule(_ context.Context, every stdtime.Duration) (stdtime.Time, error)",
		`stdtime "time"`,
		"var _ Service = (*service)(nil)",
	} {
		if !strings.Contains(derived, want) {
			t.Errorf("contract_gen.go lacks %q:\n%s", want, derived)
		}
	}
	if strings.Contains(derived, "unexported") {
		t.Errorf("contract_gen.go holds an unexported method:\n%s", derived)
	}
	mock := readFile(t, filepath.Join(pkg, "mock_gen.go"))
	for _, want := range []string{"type MockService struct", "type MockStore struct", "edit service.go"} {
		if !strings.Contains(mock, want) {
			t.Errorf("mock_gen.go lacks %q:\n%s", want, mock)
		}
	}
	mw := readFile(t, filepath.Join(pkg, "middleware_gen.go"))
	if !strings.Contains(mw, "Send(") || !strings.Contains(mw, "Schedule(") {
		t.Errorf("middleware_gen.go does not wrap the derived methods:\n%s", mw)
	}
	// The derived methods route through the op form: error-only Send via
	// RunOp, the value-and-error Schedule via AroundOp.
	if !strings.Contains(mw, "observe.RunOp(") || !strings.Contains(mw, "observe.AroundOp(") {
		t.Errorf("middleware_gen.go does not use the observe.RunOp/AroundOp op form:\n%s", mw)
	}

	if !testing.Short() {
		buildDerivedContractPackage(t, forgeRoot, root)
	}

	// Unmarking hands the package back to the hand-written form: the
	// derived file goes, and the next generate has no contract to read.
	svc := filepath.Join(pkg, "service.go")
	mustWrite(t, svc, strings.Replace(readFile(t, svc), "//forge:contract\n", "", 1))
	if err := generateInternalPackageContracts(root, cfg, cs); err != nil {
		t.Fatalf("generate after unmarking: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pkg, "contract_gen.go")); !os.IsNotExist(err) {
		t.Fatalf("contract_gen.go survived its marker (err=%v)", err)
	}
}

// TestGenerate_RefusesAContractDeclaredTwice: a hand-written Service beside a
// marked struct is a generate error naming both.
func TestGenerate_RefusesAContractDeclaredTwice(t *testing.T) {
	t.Chdir(t.TempDir())
	pkg := writeDerivedContractProject(t, ".")
	mustWrite(t, filepath.Join(pkg, "contract.go"), "package email\n\nimport \"context\"\n\ntype Service interface {\n\tSend(ctx context.Context, msg Message) error\n}\n")
	err := generateInternalPackageContracts(".", &config.ProjectConfig{Name: "proj"}, nil)
	if err == nil || !strings.Contains(err.Error(), "declares its contract twice") {
		t.Fatalf("err = %v, want the declared-twice refusal", err)
	}
}

// buildDerivedContractPackage compiles the generated package against this
// forge checkout: contract_gen.go, mock_gen.go and middleware_gen.go must
// all type-check beside the hand-written files.
func buildDerivedContractPackage(t *testing.T, forgeRoot, root string) {
	t.Helper()
	sum, err := os.ReadFile(filepath.Join(forgeRoot, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	goVersion := "1.24"
	if data, rerr := os.ReadFile(filepath.Join(forgeRoot, "go.mod")); rerr == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
				goVersion = strings.TrimSpace(v)
			}
		}
	}
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/proj\n\ngo "+goVersion+"\n\nrequire github.com/reliant-labs/forge v0.0.0\n\n"+
		"replace github.com/reliant-labs/forge => "+filepath.ToSlash(forgeRoot)+"\n")
	mustWrite(t, filepath.Join(root, "go.sum"), string(sum))
	vet := exec.Command("go", "vet", "./internal/...")
	vet.Dir = root
	vet.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
	if out, verr := vet.CombinedOutput(); verr != nil {
		t.Fatalf("the derived contract package does not compile: %v\n%s", verr, out)
	}
}
