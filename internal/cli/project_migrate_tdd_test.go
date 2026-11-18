package cli

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Golden tests for the test-migrate-tdd codemod. Inputs live under
// testdata/test_migrate_tdd/<case>/input.go.txt; the test asserts the
// transformed output matches a few invariants (parses, contains the
// expected tdd.RunRPCCases sites, no longer contains the hand-rolled
// `tests := []struct` shape) rather than a byte-for-byte golden so the
// emitter can evolve without churning fixtures on every comment change.
//
// no_match cases assert the file is skipped — codemod returns
// SkippedNoMatch and leaves the file content unchanged.

func TestMigrateTDDFile_SvcBasic(t *testing.T) {
	src := loadFixture(t, "svc_basic/input.go.txt")
	dest := writeTempFile(t, src)

	res, err := migrateTDDFile(dest, false)
	if err != nil {
		t.Fatalf("migrateTDDFile: %v", err)
	}
	if res.Status != migrateStatusTransformed {
		t.Fatalf("expected Transformed, got %v (reason=%q)", res.Status, res.Reason)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(res.Rows))
	}

	out, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	// Output must parse.
	if _, err := parser.ParseFile(token.NewFileSet(), "out.go", got, 0); err != nil {
		t.Fatalf("output failed to parse: %v\n---\n%s", err, got)
	}

	// Hand-rolled shape must be gone.
	if strings.Contains(got, "tests := []struct") {
		t.Errorf("output still contains hand-rolled `tests := []struct` slice")
	}
	if strings.Contains(got, "func TestHandlers(") {
		t.Errorf("output still contains the original TestHandlers function")
	}

	// New shape must be present.
	if !strings.Contains(got, "tdd.RunRPCCases") {
		t.Errorf("output missing tdd.RunRPCCases call")
	}
	if !strings.Contains(got, "func TestGetCurrentUser_Generated(") {
		t.Errorf("output missing TestGetCurrentUser_Generated function")
	}
	if !strings.Contains(got, "func TestUpdateProfile_Generated(") {
		t.Errorf("output missing TestUpdateProfile_Generated function")
	}

	// Sibling tests must survive verbatim.
	if !strings.Contains(got, "func TestValidateDepsRejectsNilLogger(") {
		t.Errorf("output dropped sibling TestValidateDepsRejectsNilLogger")
	}

	// forge/pkg/tdd import must be added.
	if !strings.Contains(got, `"github.com/reliant-labs/forge/pkg/tdd"`) {
		t.Errorf("output missing forge/pkg/tdd import")
	}
}

func TestMigrateTDDFile_ClientIntegration(t *testing.T) {
	src := loadFixture(t, "client_integration/input.go.txt")
	dest := writeTempFile(t, src)

	res, err := migrateTDDFile(dest, false)
	if err != nil {
		t.Fatalf("migrateTDDFile: %v", err)
	}
	if res.Status != migrateStatusTransformed {
		t.Fatalf("expected Transformed, got %v (reason=%q)", res.Status, res.Reason)
	}

	out, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	// Output must parse.
	if _, err := parser.ParseFile(token.NewFileSet(), "out.go", got, 0); err != nil {
		t.Fatalf("output failed to parse: %v\n---\n%s", err, got)
	}

	// Build tag must be preserved.
	if !strings.Contains(got, "//go:build integration") {
		t.Errorf("output dropped //go:build integration tag")
	}
	if !strings.Contains(got, "// +build integration") {
		t.Errorf("output dropped // +build integration tag")
	}

	// Two-value receiver pattern must be preserved.
	if !strings.Contains(got, "_, client := app.NewTestAdminServerServer(t)") {
		t.Errorf("output missing two-value receiver pattern; got:\n%s", got)
	}

	// client.Method must be passed as the handler.
	if !strings.Contains(got, "}, client.Create)") {
		t.Errorf("output missing client.Create handler arg")
	}
	if !strings.Contains(got, "}, client.List)") {
		t.Errorf("output missing client.List handler arg")
	}
}

func TestMigrateTDDFile_NoMatch(t *testing.T) {
	src := loadFixture(t, "no_match/input.go.txt")
	dest := writeTempFile(t, src)

	res, err := migrateTDDFile(dest, false)
	if err != nil {
		t.Fatalf("migrateTDDFile: %v", err)
	}
	if res.Status != migrateStatusSkippedNoMatch {
		t.Fatalf("expected SkippedNoMatch, got %v", res.Status)
	}

	// File content must be byte-identical to input.
	out, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, src) {
		t.Errorf("file modified despite SkippedNoMatch:\n--- want ---\n%s\n--- got ---\n%s", src, out)
	}
}

// TestMigrateTDDFile_DryRun verifies that --dry-run does not write to disk.
func TestMigrateTDDFile_DryRun(t *testing.T) {
	src := loadFixture(t, "svc_basic/input.go.txt")
	dest := writeTempFile(t, src)

	res, err := migrateTDDFile(dest, true)
	if err != nil {
		t.Fatalf("migrateTDDFile: %v", err)
	}
	if res.Status != migrateStatusTransformed {
		t.Fatalf("expected Transformed (dry-run still reports), got %v", res.Status)
	}

	out, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, src) {
		t.Errorf("dry-run wrote changes to disk")
	}
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", "test_migrate_tdd", name))
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func writeTempFile(t *testing.T, src []byte) string {
	t.Helper()
	dir := t.TempDir()
	dest := filepath.Join(dir, "handlers_test.go")
	if err := os.WriteFile(dest, src, 0o644); err != nil {
		t.Fatal(err)
	}
	return dest
}
