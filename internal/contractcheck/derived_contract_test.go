package contractcheck

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

const markedServiceGo = `package mailer

import "context"

type Deps struct{}

//forge:contract
type service struct{ deps Deps }

func New(deps Deps) (Service, error) { return &service{deps: deps}, nil }

func (s *service) Send(ctx context.Context, to string) error { return nil }
`

// TestInternalContracts_MarkedStructIsTheContract: a package whose Service is
// derived from a `//forge:contract` struct has the canonical shape — before
// forge generate has written contract_gen.go, too, since the shape rule
// reads only hand-written files.
func TestInternalContracts_MarkedStructIsTheContract(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "mailer")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "service.go"), markedServiceGo))

	fs, err := Inspect(context.Background(), tmp, Options{Rules: []Rule{RuleInternalPackageContractNames, RuleInternalPackageMissingContract}})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("a marked-struct package drew findings:\n%s", AsResult(fs).FormatText())
	}
}

// TestInternalContracts_ContractDeclaredTwice: a hand-written Service beside a
// marked struct is refused, anchored on the marked type.
func TestInternalContracts_ContractDeclaredTwice(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "mailer")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "service.go"), markedServiceGo))
	must(t, writeFile(filepath.Join(pkgDir, "contract.go"), "package mailer\n\nimport \"context\"\n\ntype Service interface {\n\tSend(ctx context.Context, to string) error\n}\n"))

	fs, err := Inspect(context.Background(), tmp, Options{Rules: []Rule{RuleInternalPackageContractNames}})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleInternalPackageContractNames))
	if len(got) != 1 || !strings.Contains(got[0].Message, "declares its contract twice") || !strings.HasSuffix(got[0].File, "service.go") {
		t.Fatalf("want one declared-twice finding on service.go, got:\n%s", AsResult(fs).FormatText())
	}
}
