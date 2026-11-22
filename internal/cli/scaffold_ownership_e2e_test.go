//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2ECRUDOwnershipEnforced is the gate for declarative CRUD ownership
// (`COMMENT ON TABLE t IS 'forge:owner=…'`). It proves, in a real generated
// project against real Postgres, that:
//
//   - generate FAILS CLOSED when a table declares an owner and the app provides
//     no crud.OwnerResolver, and the error carries the crud.OwnerKinds remedy;
//   - with a provider, the generated ops route through crud.Owned*, and a caller
//     cannot read, list, update, delete or create rows outside their ownership,
//     through a join path (notes → folders.owner_id) or user-OR-org alternatives;
//   - a request with no caller is Unauthenticated.
func TestE2ECRUDOwnershipEnforced(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "ownapp", "--mod", "example.com/ownapp", "--service", "notes")
	projectDir := filepath.Join(dir, "ownapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "notes", "v1", "notes.proto")
	proto := readFileE2E(t, protoPath)
	proto += `
// forge:entity
message Folder {
  string id = 1;
  string owner_id = 2;
  string name = 3;
}

// forge:entity
message Note {
  string id = 1;
  string folder_id = 2;
  string body = 3;
}

// forge:entity
message Account {
  string id = 1;
  optional string user_id = 2;
  optional string org_id = 3;
  string name = 4;
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author notes proto: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "scaffold")

	// The born notes migration already carries notes_folder_id_fkey
	// (folder_id → folders), which the join path's PermissionDenied-on-FK
	// handling keys on; this migration only declares ownership.
	if up := readBornMigrationE2E(t, projectDir, "notes"); !strings.Contains(up, "notes_folder_id_fkey FOREIGN KEY (folder_id) REFERENCES folders (id)") {
		t.Fatalf("notes birth migration must name its folder FK notes_folder_id_fkey:\n%s", up)
	}
	writeFileE2E(t, nextMigrationPathE2E(t, projectDir, "declare_ownership"), `-- forge:backward-compatible — comments only; no schema change
COMMENT ON TABLE folders IS 'forge:owner=owner_id';
COMMENT ON TABLE notes IS 'forge:owner=folder_id>owner_id';
COMMENT ON TABLE accounts IS 'forge:owner=user:user_id,org:org_id';
`)

	// No provider yet: generate must fail closed, never wire a nil or a stub.
	out, err := runCmdAllowFail(t, projectDir, forgeBin, "generate")
	if err == nil {
		t.Fatalf("generate must FAIL when an owner-scoped table has no crud.OwnerResolver provider; it succeeded:\n%s", out)
	}
	for _, want := range []string{"Owners", "crud.OwnerResolver", "crud.OwnerKinds", "crud.ErrNoCaller"} {
		if !strings.Contains(out, want) {
			t.Errorf("the missing-provider error must mention %q:\n%s", want, out)
		}
	}

	// Provide it: a provider in internal/app/providers.go, resolved by type.
	providersPath := filepath.Join(projectDir, "internal", "app", "providers.go")
	providers := readFileE2E(t, providersPath)
	const fmwImport = "\tfmw \"github.com/reliant-labs/forge/pkg/middleware\"\n"
	if !strings.Contains(providers, fmwImport) {
		t.Fatalf("scaffolded providers.go no longer imports forge's middleware as fmw; update this test:\n%s", providers)
	}
	providers = strings.Replace(providers, fmwImport, "\t\"github.com/reliant-labs/forge/pkg/crud\"\n"+fmwImport, 1)
	providers += ownersProviderSrc
	if err := os.WriteFile(providersPath, []byte(providers), 0o644); err != nil {
		t.Fatalf("write providers.go: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "generate")

	notesDir := filepath.Join(projectDir, "internal", "handlers", "notes")
	ops := readFileE2E(t, filepath.Join(notesDir, "handlers_crud_ops_gen.go"))
	for _, want := range []string{"crud.OwnedGet(", "crud.OwnedList(", "crud.OwnedUpdate(", "crud.OwnedDelete(", "crud.OwnedCreate("} {
		if !strings.Contains(ops, want) {
			t.Errorf("handlers_crud_ops_gen.go must route through %s:\n%s", want, ops)
		}
	}
	if shim := readFileE2E(t, filepath.Join(notesDir, "handlers_crud.go")); strings.Contains(shim, "FORGE_SCAFFOLD") {
		t.Errorf("handlers_crud.go must carry no FORGE_SCAFFOLD placeholder:\n%s", shim)
	}

	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./internal/handlers/...")

	// Drive the GENERATED handlers against real Postgres.
	writeFileE2E(t, filepath.Join(notesDir, "ownership_test.go"), ownershipHandlerTestSrc)
	runCmd(t, projectDir, "go", "test", "./internal/handlers/notes/", "-run", "Ownership", "-count=1")
}

const ownersProviderSrc = `
// OwnersDeps is empty: the resolver reads the caller from the request context.
type OwnersDeps struct{}

// NewOwners resolves the caller's owner values for forge:owner= scoped CRUD.
func NewOwners(OwnersDeps) (crud.OwnerResolver, error) {
	caller := func(ctx context.Context) (*middleware.Claims, error) {
		claims, err := middleware.GetUser(ctx)
		if err != nil {
			return nil, crud.ErrNoCaller
		}
		return claims, nil
	}
	return crud.OwnerKinds{
		"user": func(ctx context.Context) ([]string, error) {
			c, err := caller(ctx)
			if err != nil {
				return nil, err
			}
			return []string{c.UserID}, nil
		},
		"org": func(ctx context.Context) ([]string, error) {
			c, err := caller(ctx)
			if err != nil {
				return nil, err
			}
			if c.OrgID == "" {
				return nil, nil
			}
			return []string{c.OrgID}, nil
		},
	}, nil
}
`

const ownershipHandlerTestSrc = `package notes_test

import (
	"context"
	"sort"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/crud"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "example.com/ownapp/gen/services/notes/v1"
	notes "example.com/ownapp/internal/handlers/notes"
	"example.com/ownapp/pkg/middleware"
)

// ownersFromCtx is the app's crud.OwnerResolver, keyed on the request's claims.
func ownersFromCtx() crud.OwnerResolver {
	caller := func(ctx context.Context) (*middleware.Claims, error) {
		c, err := middleware.GetUser(ctx)
		if err != nil {
			return nil, crud.ErrNoCaller
		}
		return c, nil
	}
	return crud.OwnerKinds{
		"user": func(ctx context.Context) ([]string, error) {
			c, err := caller(ctx)
			if err != nil {
				return nil, err
			}
			return []string{c.UserID}, nil
		},
		"org": func(ctx context.Context) ([]string, error) {
			c, err := caller(ctx)
			if err != nil {
				return nil, err
			}
			if c.OrgID == "" {
				return nil, nil
			}
			return []string{c.OrgID}, nil
		},
	}
}

func callerCtx(user, org string) context.Context {
	return middleware.ContextWithClaims(context.Background(), &middleware.Claims{UserID: user, OrgID: org})
}

func wantCode(t *testing.T, label string, err error, want connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected %v, got nil", label, want)
	}
	if got := svcerr.Code(err); got != want {
		t.Fatalf("%s: code = %v, want %v (err: %v)", label, got, want, err)
	}
}

func TestOwnership_NotesThroughFolderJoin(t *testing.T) {
	db := crudTestDB(t)
	svc := notes.NewTestNotes(t, notes.WithDB(db), notes.WithNotesDeps(notes.Deps{Owners: ownersFromCtx()}))
	alice, bob := callerCtx("alice", ""), callerCtx("bob", "")

	mkFolder := func(ctx context.Context, owner string) string {
		t.Helper()
		r, err := svc.CreateFolder(ctx, &pb.CreateFolderRequest{OwnerId: owner, Name: owner + "-folder"})
		if err != nil {
			t.Fatalf("create folder for %s: %v", owner, err)
		}
		return r.GetFolder().GetId()
	}
	aliceFolder, bobFolder := mkFolder(alice, "alice"), mkFolder(bob, "bob")

	mkNote := func(ctx context.Context, folder, body string) string {
		t.Helper()
		r, err := svc.CreateNote(ctx, &pb.CreateNoteRequest{FolderId: folder, Body: body})
		if err != nil {
			t.Fatalf("create note %q: %v", body, err)
		}
		return r.GetNote().GetId()
	}
	aliceNote, bobNote := mkNote(alice, aliceFolder, "alice-secret"), mkNote(bob, bobFolder, "bob-secret")

	// Own rows are visible through the join.
	if _, err := svc.GetNote(alice, &pb.GetNoteRequest{Id: aliceNote}); err != nil {
		t.Fatalf("alice get own note: %v", err)
	}

	// Cross-tenant Get is NotFound, exactly like an unknown id.
	_, err := svc.GetNote(alice, &pb.GetNoteRequest{Id: bobNote})
	wantCode(t, "alice get bob's note", err, connect.CodeNotFound)
	_, err = svc.GetNote(alice, &pb.GetNoteRequest{Id: "no-such-note"})
	wantCode(t, "alice get unknown note", err, connect.CodeNotFound)
	_, err = svc.GetFolder(alice, &pb.GetFolderRequest{Id: bobFolder})
	wantCode(t, "alice get bob's folder", err, connect.CodeNotFound)

	// List shows only the caller's notes, and the total follows.
	listed, err := svc.ListNotes(alice, &pb.ListNotesRequest{PageSize: 50})
	if err != nil {
		t.Fatalf("alice list: %v", err)
	}
	if got := listed.GetNotes(); len(got) != 1 || got[0].GetId() != aliceNote {
		t.Fatalf("alice list = %v, want only %s", got, aliceNote)
	}
	if total := listed.GetTotalCount(); total != 1 {
		t.Errorf("alice list total_count = %d, want 1 (the count must be scoped too)", total)
	}

	// Cross-tenant Update is NotFound and leaves the row unchanged.
	_, err = svc.UpdateNote(alice, &pb.UpdateNoteRequest{Note: &pb.Note{Id: bobNote, FolderId: bobFolder, Body: "pwned"}})
	wantCode(t, "alice update bob's note", err, connect.CodeNotFound)
	got, err := svc.GetNote(bob, &pb.GetNoteRequest{Id: bobNote})
	if err != nil {
		t.Fatalf("bob re-read own note: %v", err)
	}
	if got.GetNote().GetBody() != "bob-secret" {
		t.Errorf("bob's note body = %q after alice's refused update, want bob-secret", got.GetNote().GetBody())
	}

	// Moving her own note under bob's folder would take it out of her
	// ownership: refused, and rolled back.
	_, err = svc.UpdateNote(alice, &pb.UpdateNoteRequest{Note: &pb.Note{Id: aliceNote, FolderId: bobFolder, Body: "alice-secret"}})
	wantCode(t, "alice moves her note into bob's folder", err, connect.CodePermissionDenied)
	got, err = svc.GetNote(alice, &pb.GetNoteRequest{Id: aliceNote})
	if err != nil {
		t.Fatalf("alice re-read own note: %v", err)
	}
	if got.GetNote().GetFolderId() != aliceFolder {
		t.Errorf("alice's note folder_id = %q after refused move, want %q (rollback)", got.GetNote().GetFolderId(), aliceFolder)
	}

	// An in-scope update still works.
	if _, err := svc.UpdateNote(alice, &pb.UpdateNoteRequest{Note: &pb.Note{Id: aliceNote, FolderId: aliceFolder, Body: "alice-edited"}}); err != nil {
		t.Fatalf("alice update own note: %v", err)
	}

	// Cross-tenant Delete is NotFound and the row survives.
	_, err = svc.DeleteNote(alice, &pb.DeleteNoteRequest{Id: bobNote})
	wantCode(t, "alice delete bob's note", err, connect.CodeNotFound)
	if _, err := svc.GetNote(bob, &pb.GetNoteRequest{Id: bobNote}); err != nil {
		t.Fatalf("bob's note must survive alice's refused delete: %v", err)
	}

	// Creating a note under another user's folder is PermissionDenied; so is a
	// folder that does not exist (no parent-existence oracle).
	_, err = svc.CreateNote(alice, &pb.CreateNoteRequest{FolderId: bobFolder, Body: "plant"})
	wantCode(t, "alice creates a note in bob's folder", err, connect.CodePermissionDenied)
	_, err = svc.CreateNote(alice, &pb.CreateNoteRequest{FolderId: "no-such-folder", Body: "plant"})
	wantCode(t, "alice creates a note in a missing folder", err, connect.CodePermissionDenied)
	// Nor may she create a folder owned by bob.
	_, err = svc.CreateFolder(alice, &pb.CreateFolderRequest{OwnerId: "bob", Name: "plant"})
	wantCode(t, "alice creates a folder owned by bob", err, connect.CodePermissionDenied)

	// bob still sees exactly his own note, unchanged by all of the above.
	bobList, err := svc.ListNotes(bob, &pb.ListNotesRequest{PageSize: 50})
	if err != nil {
		t.Fatalf("bob list: %v", err)
	}
	if got := bobList.GetNotes(); len(got) != 1 || got[0].GetId() != bobNote || got[0].GetBody() != "bob-secret" {
		t.Fatalf("bob list = %v, want only the untouched %s", got, bobNote)
	}

	// Alice's own delete works.
	if _, err := svc.DeleteNote(alice, &pb.DeleteNoteRequest{Id: aliceNote}); err != nil {
		t.Fatalf("alice delete own note: %v", err)
	}
}

func TestOwnership_AccountsUserOrOrg(t *testing.T) {
	db := crudTestDB(t)
	svc := notes.NewTestNotes(t, notes.WithDB(db), notes.WithNotesDeps(notes.Deps{Owners: ownersFromCtx()}))
	str := func(s string) *string { return &s }

	// Each account is created by a caller who owns it.
	mk := func(ctx context.Context, name string, user, org *string) string {
		t.Helper()
		r, err := svc.CreateAccount(ctx, &pb.CreateAccountRequest{Name: name, UserId: user, OrgId: org})
		if err != nil {
			t.Fatalf("create account %s: %v", name, err)
		}
		return r.GetAccount().GetId()
	}
	aliceInOrg1 := callerCtx("alice", "org1")
	a1 := mk(aliceInOrg1, "a1-alice", str("alice"), nil)
	a2 := mk(aliceInOrg1, "a2-org1", nil, str("org1"))
	a3 := mk(callerCtx("bob", "org2"), "a3-bob-org2", str("bob"), str("org2"))
	a4 := mk(callerCtx("carol", ""), "a4-carol", str("carol"), nil)

	ids := func(ctx context.Context) []string {
		t.Helper()
		r, err := svc.ListAccounts(ctx, &pb.ListAccountsRequest{PageSize: 50})
		if err != nil {
			t.Fatalf("list accounts: %v", err)
		}
		var out []string
		for _, a := range r.GetAccounts() {
			out = append(out, a.GetId())
		}
		sort.Strings(out)
		return out
	}
	same := func(label string, got, want []string) {
		t.Helper()
		sort.Strings(want)
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", label, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v, want %v", label, got, want)
			}
		}
	}

	// alice (user alice, member of org1) sees her own row OR her org's row.
	same("alice in org1", ids(aliceInOrg1), []string{a1, a2})
	// The same user without the org membership loses the org-owned row.
	same("alice without org", ids(callerCtx("alice", "")), []string{a1})
	// bob matches a3 by user and by org.
	same("bob in org2", ids(callerCtx("bob", "org2")), []string{a3})
	// a colleague in org1 reaches the org's row only.
	same("dave in org1", ids(callerCtx("dave", "org1")), []string{a2})
	// a stranger sees nothing.
	same("erin", ids(callerCtx("erin", "org9")), nil)

	// Get follows the same predicate.
	if _, err := svc.GetAccount(callerCtx("dave", "org1"), &pb.GetAccountRequest{Id: a2}); err != nil {
		t.Fatalf("dave get org1 account: %v", err)
	}
	_, err := svc.GetAccount(callerCtx("dave", "org1"), &pb.GetAccountRequest{Id: a1})
	wantCode(t, "dave get alice's account", err, connect.CodeNotFound)
	_, err = svc.GetAccount(callerCtx("dave", "org1"), &pb.GetAccountRequest{Id: a4})
	wantCode(t, "dave get carol's account", err, connect.CodeNotFound)

	// An org colleague may update the org's row, a stranger may not.
	if _, err := svc.UpdateAccount(callerCtx("dave", "org1"), &pb.UpdateAccountRequest{Account: &pb.Account{Id: a2, OrgId: str("org1"), Name: "renamed"}}); err != nil {
		t.Fatalf("dave update org1 account: %v", err)
	}
	_, err = svc.UpdateAccount(callerCtx("erin", "org9"), &pb.UpdateAccountRequest{Account: &pb.Account{Id: a2, OrgId: str("org1"), Name: "hijacked"}})
	wantCode(t, "erin update org1's account", err, connect.CodeNotFound)
	got, err := svc.GetAccount(aliceInOrg1, &pb.GetAccountRequest{Id: a2})
	if err != nil {
		t.Fatalf("alice get a2: %v", err)
	}
	if got.GetAccount().GetName() != "renamed" {
		t.Errorf("a2 name = %q, want renamed", got.GetAccount().GetName())
	}

	// Handing an account to an org the caller is not in is refused.
	_, err = svc.UpdateAccount(callerCtx("dave", "org1"), &pb.UpdateAccountRequest{Account: &pb.Account{Id: a2, OrgId: str("org7"), Name: "renamed"}})
	wantCode(t, "dave moves org1's account to org7", err, connect.CodePermissionDenied)

	// Delete is scoped too.
	_, err = svc.DeleteAccount(callerCtx("erin", "org9"), &pb.DeleteAccountRequest{Id: a3})
	wantCode(t, "erin delete bob's account", err, connect.CodeNotFound)
	if _, err := svc.DeleteAccount(callerCtx("dave", "org1"), &pb.DeleteAccountRequest{Id: a2}); err != nil {
		t.Fatalf("dave delete org1 account: %v", err)
	}
}

func TestOwnership_MissingCallerIsUnauthenticated(t *testing.T) {
	db := crudTestDB(t)
	svc := notes.NewTestNotes(t, notes.WithDB(db), notes.WithNotesDeps(notes.Deps{Owners: ownersFromCtx()}))
	anon := context.Background()

	_, err := svc.GetNote(anon, &pb.GetNoteRequest{Id: "x"})
	wantCode(t, "anonymous get", err, connect.CodeUnauthenticated)
	_, err = svc.ListNotes(anon, &pb.ListNotesRequest{PageSize: 10})
	wantCode(t, "anonymous list", err, connect.CodeUnauthenticated)
	_, err = svc.CreateFolder(anon, &pb.CreateFolderRequest{OwnerId: "alice", Name: "n"})
	wantCode(t, "anonymous create", err, connect.CodeUnauthenticated)
	_, err = svc.DeleteAccount(anon, &pb.DeleteAccountRequest{Id: "x"})
	wantCode(t, "anonymous delete", err, connect.CodeUnauthenticated)
}
`
