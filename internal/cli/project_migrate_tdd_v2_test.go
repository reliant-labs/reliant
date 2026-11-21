package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tdd codemod lifts connect-go v1 rows (connect.NewRequest(&pb.X{}))
// into v1 tdd.RunRPCCases rows. A connect-go v2 module has neither shape, so
// the command refuses there — naming the v2 row form — and must leave every
// file untouched, even one that still carries the v1 shape it would rewrite.
func TestMigrateTDD_RefusesConnectV2Module(t *testing.T) {
	root := t.TempDir()
	writeV2Module(t, root, "require connectrpc.com/connect/v2 v2.0.2\n")
	src := loadFixture(t, "svc_basic/input.go.txt")
	testFile := filepath.Join(root, "internal", "handlers", "billing", "handlers_test.go")
	if err := os.MkdirAll(filepath.Dir(testFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testFile, src, 0o644); err != nil {
		t.Fatal(err)
	}

	// --path pointing into the module still finds its go.mod.
	err := runMigrateTDD(migrateTDDFlags{path: filepath.Join(root, "internal", "handlers")})
	if err == nil {
		t.Fatal("runMigrateTDD on a connect-go v2 module: want a refusal, got nil")
	}
	for _, want := range []string{"connect-go v2", "tdd.RunRPCCasesV2", "connectrpc.com/connect/v2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal should mention %q, got: %v", want, err)
		}
	}
	got, rerr := os.ReadFile(testFile)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != string(src) {
		t.Error("refused codemod rewrote a file")
	}
}

// A v1 module (v2 only indirect, as otelconnect pulls it) is migrated as
// before.
func TestMigrateTDD_RunsOnConnectV1Module(t *testing.T) {
	root := t.TempDir()
	writeV2Module(t, root, "require (\n\tconnectrpc.com/connect v1.21.1\n\tconnectrpc.com/connect/v2 v2.0.2 // indirect\n)\n")
	if err := refuseMigrateTDDOnConnectV2(root); err != nil {
		t.Fatalf("v1 module refused: %v", err)
	}
}

func writeV2Module(t *testing.T, root, requires string) {
	t.Helper()
	gomod := "module example.com/app\n\ngo 1.24\n\n" + requires
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
}
