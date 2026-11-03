//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/goexec"
)

// forgetScaffoldRecordE2E deletes a scaffold-once file AND its ledger entry,
// which is how an author asks forge to re-birth a file against the schema as
// it now stands. Deleting the file alone is deliberately not enough: the
// ledger records that forge already scaffolded it once, so a bare deletion
// stays deleted (that is the scaffold-once contract).
func forgetScaffoldRecordE2E(t *testing.T, projectDir, fullPath, relPath string) {
	t.Helper()
	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove %s: %v", relPath, err)
	}
	entry := filepath.Join(projectDir, filepath.FromSlash(checksums.ScaffoldEntryPath(relPath)))
	if _, err := os.Stat(entry); err != nil {
		t.Fatalf("scaffold ledger has no record of %s — the re-birth this sets up would be vacuous: %v", relPath, err)
	}
	if err := os.Remove(entry); err != nil {
		t.Fatalf("remove ledger entry: %v", err)
	}
}

// runCmdAllowFail runs a command and RETURNS its failure instead of aborting
// the test. Needed wherever the failure IS the behaviour under test.
func runCmdAllowFail(t *testing.T, dir string, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(),
		"GOFLAGS=",
		"GONOSUMCHECK=*",
		"GOPROXY=https://proxy.golang.org,direct",
		"GONOSUMDB=*",
	))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The defect this pins, stated as the run that found it:
//
//	FAIL: TestCRUD_Order_Lifecycle
//	  create #1: invalid_argument: create order: a field value violates a
//	  constraint (constraint orders_customer_id_check)
//
// An author wrote migrations with the CHECKs forge's own charter tells them to
// write — an email regex, a two-character country code, a bounded name — and
// the generated lifecycle test inserted values those CHECKs reject. The test is
// scaffold-once, so the author's only escape was deleting it, and it
// regenerated.
//
// The migration below carries the SAME constraints, copied from the run that
// failed: the email regex, the two-character country code, the bounded name,
// plus the numeric IN-list that probing showed the derivation also could not
// invert. It is a hand-written TIGHTENING of the born table — which is exactly
// what forge's charter tells authors to write ("add relationships, indexes,
// and constraints with hand-written migrations").
//
// The proof is not an assertion about the generated text — it is `go test` on
// the generated project. If the fixtures violate the schema, create #1 fails
// exactly as it did in the wild.
const checkConstraintMigration = `
ALTER TABLE orders ADD CONSTRAINT orders_customer_email_check
    CHECK (customer_email ~ '^[^@\s]+@[^@\s]+\.[^@\s]+$');
ALTER TABLE orders ADD CONSTRAINT orders_shipping_country_check
    CHECK (char_length(shipping_country) = 2);
ALTER TABLE orders ADD CONSTRAINT orders_shipping_name_check
    CHECK (char_length(shipping_name) BETWEEN 1 AND 160);
ALTER TABLE orders ADD CONSTRAINT orders_priority_check
    CHECK (priority IN (10, 20, 30));
`

// TestE2ECRUDFixtureSatisfiesCheckConstraints scaffolds a project carrying the
// shopdemo CHECKs and runs its generated CRUD lifecycle test.
//
// The constraints are added BEFORE the lifecycle test is scaffolded, because
// that is the order that matters: fixtures are derived on the first-scaffold
// path, from the schema as it stands then. A schema that already carries tight
// CHECKs is the case the run hit.
func TestE2ECRUDFixtureSatisfiesCheckConstraints(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "shopchk", "--mod", "example.com/shopchk", "--service", "storefront")
	projectDir := filepath.Join(dir, "shopchk")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "storefront", "v1", "storefront.proto")
	proto := readFileE2E(t, protoPath)
	proto += "\n// forge:entity\n" +
		"message Order {\n" +
		"  string customer_id = 1;\n" +
		"  string customer_email = 2;\n" +
		"  string shipping_name = 3;\n" +
		"  string shipping_country = 4;\n" +
		"  int64 priority = 5;\n" +
		"}\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("append Order entity: %v", err)
	}

	// Phase 1 births the table and injects the CRUD quintet; phase 2 would
	// normally scaffold the lifecycle test in the same run. Birth first, then
	// TIGHTEN the born migration by hand — which is exactly what the charter
	// tells authors to do ("add relationships, indexes, and constraints with
	// hand-written migrations") and what the failing run's author had done.
	runCmd(t, projectDir, forgeBin, "scaffold")

	if err := os.WriteFile(nextMigrationPathE2E(t, projectDir, "checks"),
		[]byte(checkConstraintMigration), 0o644); err != nil {
		t.Fatalf("write checks migration: %v", err)
	}

	// The lifecycle test is scaffold-once and was born against the loose
	// schema. Clearing its ledger record is how an author asks for a fresh
	// birth against the schema as it now stands.
	crudTestPath := filepath.Join(projectDir, "internal", "handlers", "storefront", "handlers_crud_test.go")
	forgetScaffoldRecordE2E(t, projectDir, crudTestPath,
		filepath.Join("internal", "handlers", "storefront", "handlers_crud_test.go"))
	runCmd(t, projectDir, forgeBin, "generate")

	crudTest := readFileE2E(t, crudTestPath)
	if !strings.Contains(crudTest, "TestCRUD_Order_Lifecycle") {
		t.Fatalf("lifecycle test was not scaffolded against the constrained schema:\n%s", crudTest)
	}

	// The fixtures live in the regenerated factory, and must NOT be the
	// type-blind placeholders the run shipped. This is a property of the
	// DERIVATION (a placeholder is the emitter's own stamp for "I invented
	// this"), not a hardcoded expected value.
	factories := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "storefront", "factories_gen_test.go"))
	for _, dead := range []string{`"sample_customer_email_1"`, `"sample_shipping_country_1"`} {
		if strings.Contains(factories, dead) {
			t.Errorf("fixture %s violates its own CHECK and was emitted anyway", dead)
		}
	}

	// THE PROOF: the generated project's own CRUD lifecycle test passes
	// against the constrained schema. create #1 is the assertion.
	runCmd(t, projectDir, "go", "test", "-count=1", "./internal/handlers/storefront/")
}

// TestE2ECRUDFixtureGuardFailsLoudlyOnUninvertibleCheck pins the other half of
// the contract: when the derivation CANNOT satisfy a constraint, forge must
// say so loudly, naming the column and the constraint — never emit a fixture
// its own schema rejects and leave it to surface as a mysterious test failure.
//
// The fixture lives in the REGENERATED factory, so refusing to generate would
// hold every other change hostage to a test fixture on every run. Instead the
// generate prints the violation as a warning, and the factory's body fails
// the lifecycle test with the same message — the request is never built.
//
// The constraint is a lookahead: postgres's POSIX engine accepts it, Go's RE2
// cannot compile it, so there is nothing to invert. Before the guard, this
// silently produced `sample_passcode_1` and a create #1 failure much later.
func TestE2ECRUDFixtureGuardFailsLoudlyOnUninvertibleCheck(t *testing.T) {
	t.Parallel()
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "guardapp", "--mod", "example.com/guardapp", "--service", "account")
	projectDir := filepath.Join(dir, "guardapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "account", "v1", "account.proto")
	proto := readFileE2E(t, protoPath)
	proto += "\n// forge:entity\n" +
		"message Credential {\n" +
		"  string passcode = 1;\n" +
		"}\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("append Credential entity: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "scaffold")

	// Tighten the born table with a CHECK forge's derivation cannot invert.
	up := "ALTER TABLE credentials ADD CONSTRAINT credentials_passcode_check " +
		`CHECK (passcode ~ '^(?=.*[A-Z])[a-zA-Z]{8,}$');` + "\n"
	if err := os.WriteFile(nextMigrationPathE2E(t, projectDir, "passcode"), []byte(up), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	// Generation succeeds and WARNS, locating the problem.
	out, err := runCmdAllowFail(t, projectDir, forgeBin, "generate")
	if err != nil {
		t.Fatalf("generate failed over a test fixture it cannot derive; it must warn instead:\n%s", out)
	}
	for _, want := range []string{"NewCreateCredentialRequest", "credentials_passcode_check", "vocab.yaml"} {
		if !strings.Contains(out, want) {
			t.Errorf("generate output omits %q — the author cannot act on it:\n%s", want, out)
		}
	}

	// The lifecycle test fails, with the same located message — and the
	// rejected value was never emitted.
	factories := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "account", "factories_gen_test.go"))
	if strings.Contains(factories, "Passcode:") {
		t.Errorf("a fixture value the schema rejects was emitted anyway:\n%s", factories)
	}
	testOut, err := runCmdAllowFail(t, projectDir, "go", "test", "-count=1", "./internal/handlers/account/")
	if err == nil {
		t.Fatalf("the lifecycle test passed against a constraint no fixture satisfies:\n%s", testOut)
	}
	for _, want := range []string{"credentials.passcode", "credentials_passcode_check"} {
		if !strings.Contains(testOut, want) {
			t.Errorf("test failure omits %q:\n%s", want, testOut)
		}
	}
}
