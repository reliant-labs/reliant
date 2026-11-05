package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPreCodegenContractCheck_PassesOnCanonicalContract verifies that a
// canonical Service/Deps/New(Deps) Service contract.go does not abort the
// pipeline.
func TestPreCodegenContractCheck_PassesOnCanonicalContract(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "email")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	canonical := `package email

type Service interface { Send(to string) error }
type Deps struct{}

func New(_ Deps) Service { return nil }
`
	if err := os.WriteFile(filepath.Join(pkgDir, "contract.go"), []byte(canonical), 0o644); err != nil {
		t.Fatalf("write contract.go: %v", err)
	}
	if err := preCodegenContractCheck(tmp, nil); err != nil {
		t.Fatalf("preCodegenContractCheck on canonical contract returned err: %v", err)
	}
}

// TestPreCodegenContractCheck_AbortsOnNonCanonicalContract verifies the
// pipeline aborts BEFORE codegen runs when an internal-package contract.go
// uses non-canonical names. The error message must reference the canonical
// shape so users can grep for the convention doc.
func TestPreCodegenContractCheck_AbortsOnNonCanonicalContract(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "email")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	noncanonical := `package email

type Sender interface { Send(to string) error }
type Config struct{}

func NewSender(_ Config) Sender { return nil }
`
	if err := os.WriteFile(filepath.Join(pkgDir, "contract.go"), []byte(noncanonical), 0o644); err != nil {
		t.Fatalf("write contract.go: %v", err)
	}
	err := preCodegenContractCheck(tmp, nil)
	if err == nil {
		t.Fatal("preCodegenContractCheck must abort on non-canonical contract")
	}
	const sentinel = "internal-package contracts must declare 'type Service interface', 'type Deps struct', and 'func New(Deps) Service'"
	if !strings.Contains(err.Error(), sentinel) {
		t.Errorf("error must carry the canonical sentinel for greppability; got: %s", err.Error())
	}
}

// TestPreCodegenContractCheck_NoInternalDir verifies the check is a no-op
// when there's no internal/ directory (CLI/library projects without
// behavioural sub-packages).
func TestPreCodegenContractCheck_NoInternalDir(t *testing.T) {
	tmp := t.TempDir()
	if err := preCodegenContractCheck(tmp, nil); err != nil {
		t.Fatalf("expected no error for project without internal/, got: %v", err)
	}
}
