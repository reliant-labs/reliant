//go:build e2e

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestE2EScaffoldSecretFieldPreservedOnFullReplace is the end-to-end
// data-loss gate for the `// forge:secret` field marker, proven at RUNTIME
// against real postgres through the executed generated stack (handlers →
// pkg/crud (Spec.SecretColumns) → internal/db → database).
//
// The defect: `// forge:secret` strips a field from READ responses but a
// maskless (full-replace) Update — built from a round-tripped entity whose
// secret came back "" over the wire — overwrote the stored credential with
// "". This gate pins the correct semantics:
//
//   - Create WRITES the secret (the client authors it on birth);
//   - a client Get reads the secret back as "" (the read path strips it);
//   - a maskless full-replace Update carrying secret="" PRESERVES the stored
//     value — it is never clobbered (the fix);
//   - an EXPLICIT masked Update that NAMES the secret DOES change it
//     (deliberate rotation intent).
//
// The row is read DIRECTLY (bypassing the wire strip) to observe the true
// stored value. Pre-fix, the maskless-update step wiped it to "".
func TestE2EScaffoldSecretFieldPreservedOnFullReplace(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "vaultapp", "--mod", "example.com/vaultapp", "--service", "vaults")
	projectDir := filepath.Join(dir, "vaultapp")
	addCorpusForgePkgReplace(t, projectDir)

	// Author a Credential entity: a normal string field FIRST (so it, not the
	// secret, is the born lifecycle test's mutation target), then a
	// `// forge:secret` field, then the timestamp convention fields.
	protoPath := filepath.Join(projectDir, "proto", "services", "vaults", "v1", "vaults.proto")
	proto := readFileE2E(t, protoPath)
	proto += "\n// forge:entity\n" +
		"message Credential {\n" +
		"  string id = 1;\n" +
		"  string label = 2;\n" +
		"  // forge:secret\n" +
		"  string token = 3;\n" +
		"  google.protobuf.Timestamp created_at = 4;\n" +
		"  google.protobuf.Timestamp updated_at = 5;\n" +
		"}\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("append Credential entity to vaults proto: %v", err)
	}

	// Birth the marked entity (migration + CRUD quintet) and run the real
	// generate pipeline.
	runCmd(t, projectDir, forgeBin, "scaffold")

	// The secret column is schema truth — present in the born migration.
	migDir := filepath.Join(projectDir, "db", "migrations")
	migEntries, _ := os.ReadDir(migDir)
	var upMig string
	for _, e := range migEntries {
		if filepath.Ext(e.Name()) == ".sql" && regexp.MustCompile(`_create_credentials\.up\.sql$`).MatchString(e.Name()) {
			upMig = readFileE2E(t, filepath.Join(migDir, e.Name()))
		}
	}
	if upMig == "" || !regexp.MustCompile(`\btoken\b`).MatchString(upMig) {
		t.Fatalf("secret column must stay in the born migration (schema truth); migrations=%v mig=\n%s", migEntries, upMig)
	}

	// ── repo layer: the secret column is held back on a full replace ──
	// The protection rides on the column's own Bun tag (,skipupdate) rather
	// than a parallel list of column names in a repo Spec: a name list was
	// checked against nothing, so a typo silently disabled the guard.
	credORM := readFileE2E(t, filepath.Join(projectDir, "internal", "db", "credential_orm_gen.go"))
	if !regexp.MustCompile(`bun:"token[^"]*,skipupdate`).MatchString(credORM) {
		t.Errorf("credential_orm.go must tag the token column ,skipupdate; got:\n%s", credORM)
	}

	// ── runtime: drive the generated stack against real postgres ──
	writeSecretPreservationTest(t, projectDir)
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
	runCmd(t, projectDir, "go", "test", "./internal/handlers/vaults/")
}

// writeSecretPreservationTest injects a test into the generated app that
// reuses the born harness helpers (crudTestDB, crudTestCtx — same package)
// and drives the real generated handlers, reading the stored row DIRECTLY to
// prove the secret is preserved on a maskless full-replace Update and
// changeable only via a mask that names it.
func writeSecretPreservationTest(t *testing.T, projectDir string) {
	t.Helper()
	born := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "vaults", "handlers_crud_test.go"))
	// The test factory lives in the HANDLER package (helpers_gen_test.go),
	// reached through that package's own import alias — the pkg/app god
	// package that used to host app.NewTest<X> was retired with the old DI
	// unit. Capture the alias too so the injected test imports what the
	// born one does.
	m := regexp.MustCompile(`(\w+)\.NewTest(\w+)\(`).FindStringSubmatch(born)
	if m == nil {
		t.Fatalf("born handlers_crud_test.go carries no <pkg>.NewTest<X> helper call:\n%s", born)
	}
	pkgAlias := m[1]
	helper := m[2]
	pbImport := regexp.MustCompile(`pb "([^"]+)"`).FindStringSubmatch(born)
	if pbImport == nil {
		t.Fatalf("born handlers_crud_test.go carries no pb import:\n%s", born)
	}
	svcImport := regexp.MustCompile(pkgAlias + ` "([^"]+)"`).FindStringSubmatch(born)
	if svcImport == nil {
		t.Fatalf("born handlers_crud_test.go carries no %s handler-package import:\n%s", pkgAlias, born)
	}

	test := fmt.Sprintf(`package vaults_test

// Written by forge's secret-field e2e
// (TestE2EScaffoldSecretFieldPreservedOnFullReplace): proves a
// `+"`"+`// forge:secret`+"`"+` column is PRESERVED on a maskless full-replace Update and
// changeable only via a mask that names it — through the executed generated
// stack (handlers -> pkg/crud (Spec.SecretColumns "token") -> internal/db ->
// real postgres). The stored row is read DIRECTLY to bypass the wire strip.

import (
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	pb "%s"
	%s "%s"
)

func TestSecretPreservedOnMasklessUpdate(t *testing.T) {
	db := crudTestDB(t)
	svc := %s.NewTest%s(t, %s.WithDB(db))
	ctx := crudTestCtx()

	// readToken reads the stored secret DIRECTLY — the repo/handler layers
	// never strip at the row; only the wire toProto does. This is the true
	// stored value.
	readToken := func(id string) string {
		var tok string
		if err := db.QueryRow(ctx, "SELECT token FROM credentials WHERE id = $1", id).Scan(&tok); err != nil {
			t.Fatalf("read token row for %%s: %%v", id, err)
		}
		return tok
	}

	// Create writes the secret (the client authors it on birth).
	created, err := svc.CreateCredential(ctx, connect.NewRequest(&pb.CreateCredentialRequest{
		Label: "prod",
		Token: "topsecret",
	}))
	if err != nil {
		t.Fatalf("create credential: %%v", err)
	}
	id := created.Msg.GetCredential().GetId()
	if id == "" {
		t.Fatal("create returned empty id")
	}
	if got := readToken(id); got != "topsecret" {
		t.Fatalf("Create did not store the secret: row token = %%q, want topsecret", got)
	}

	// The read path STRIPS the secret: a client Get reads it back as "".
	got, err := svc.GetCredential(ctx, connect.NewRequest(&pb.GetCredentialRequest{Id: id}))
	if err != nil {
		t.Fatalf("get credential: %%v", err)
	}
	if v := got.Msg.GetCredential().GetToken(); v != "" {
		t.Fatalf("read path must strip the secret from Get responses, got token = %%q", v)
	}

	// Client round-trip: it read the entity (token came back ""), changed
	// another field, and issued a MASKLESS full-replace Update carrying
	// Token="". Pre-fix this clobbered the stored credential with "".
	roundTripped := got.Msg.GetCredential()
	roundTripped.Label = "prod-renamed"
	// roundTripped.Token is already "" (stripped on read).
	if _, err := svc.UpdateCredential(ctx, connect.NewRequest(&pb.UpdateCredentialRequest{
		Credential: roundTripped,
	})); err != nil {
		t.Fatalf("maskless update credential: %%v", err)
	}
	if got := readToken(id); got != "topsecret" {
		t.Fatalf("SECRET WIPED: maskless full-replace Update overwrote the stored secret with %%q; want it PRESERVED as topsecret", got)
	}
	// The non-secret field still updated on the full-replace path.
	afterFull, err := svc.GetCredential(ctx, connect.NewRequest(&pb.GetCredentialRequest{Id: id}))
	if err != nil {
		t.Fatalf("get after maskless update: %%v", err)
	}
	if v := afterFull.Msg.GetCredential().GetLabel(); v != "prod-renamed" {
		t.Fatalf("maskless Update did not persist the non-secret field: label = %%q, want prod-renamed", v)
	}

	// A masked Update that NAMES the secret is deliberate intent -> it writes.
	rotate := afterFull.Msg.GetCredential()
	rotate.Token = "rotated-secret"
	if _, err := svc.UpdateCredential(ctx, connect.NewRequest(&pb.UpdateCredentialRequest{
		Credential: rotate,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"token"}},
	})); err != nil {
		t.Fatalf("masked update naming the secret: %%v", err)
	}
	if got := readToken(id); got != "rotated-secret" {
		t.Fatalf("masked Update naming the secret did not write it: row token = %%q, want rotated-secret", got)
	}
}
`, pbImport[1], pkgAlias, svcImport[1], pkgAlias, helper, pkgAlias)

	path := filepath.Join(projectDir, "internal", "handlers", "vaults", "secret_preservation_e2e_test.go")
	if err := os.WriteFile(path, []byte(test), 0o644); err != nil {
		t.Fatalf("write secret preservation test: %v", err)
	}
}
