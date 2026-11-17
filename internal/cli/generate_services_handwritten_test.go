package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// A handler directory started by hand before the service was generated — a
// helper file, no service.go — used to be taken for a scaffolded service:
// `forge generate` only stubbed the missing rpcs, so rpc_ping.go hung off a
// *Service nothing declared and the build failed ("undefined: Service";
// compose.go's ledger.New(ledger.Deps{…}) too). Each owned file is now decided
// on its own: service.go is written, the hand-written file is kept, and the
// run says which file it wrote.
func TestGenerateServiceStubsWritesAMissingServiceGoIntoAHandWrittenDir(t *testing.T) {
	projectDir := t.TempDir()
	dir := filepath.Join(projectDir, "internal", "handlers", "ledger")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	helper := "package ledger\n\n// Rate is hand-written.\nfunc Rate() int { return 1 }\n"
	if err := os.WriteFile(filepath.Join(dir, "helper.go"), []byte(helper), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := codegen.ServiceDef{
		Name: "LedgerService", Package: "services.ledger.v1",
		GoPackage: "example.com/s3/gen/services/ledger/v1", PkgName: "ledgerv1",
		ProtoFile: "proto/services/ledger/v1/ledger.proto", ModulePath: "example.com/s3",
		Methods: []codegen.Method{{Name: "Ping", InputType: "PingRequest", OutputType: "PingResponse"}},
	}

	out := captureStdout(t, func() {
		if err := generateServiceStubs(&config.ProjectConfig{ModulePath: "example.com/s3"}, []codegen.ServiceDef{svc}, projectDir, nil, nil); err != nil {
			t.Fatalf("generateServiceStubs: %v", err)
		}
	})

	got, err := os.ReadFile(filepath.Join(dir, "service.go"))
	if err != nil {
		t.Fatalf("service.go was not written into the hand-written dir: %v\n%s", err, out)
	}
	for _, want := range []string{"package ledger", "type Deps struct", "type Service struct", "func New(deps Deps)"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("service.go lacks %q:\n%s", want, got)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "helper.go")); string(b) != helper {
		t.Errorf("the hand-written helper.go changed:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "rpc_ping.go")); err != nil {
		t.Errorf("the rpc stub was not written: %v", err)
	}
	if !strings.Contains(out, "Wrote internal/handlers/ledger/service.go") {
		t.Errorf("the run does not say it wrote service.go:\n%s", out)
	}
}
