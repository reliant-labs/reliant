package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

const importTestHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// importTestControlPlane serves the org and env lookups and ImportUsers,
// answering each row with the outcome its id names ("conflict-…" → conflict).
func importTestControlPlane(t *testing.T, importReply func(w http.ResponseWriter, body map[string]any)) (*[]map[string]any, string) {
	t.Helper()
	var imports []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/" + cloud.ProcGetOrganization:
			_, _ = fmt.Fprintf(w, `{"%s":{"orgId":"authenticated-org"}}`, orgResponseKey)
		case "/controlplane.v1.DeployService/ListEnvironments":
			_, _ = w.Write([]byte(`{"environments":[{"id":"exact-env","name":"prod","project":"shop"}]}`))
		case "/" + procIdentityImport:
			imports = append(imports, body)
			importReply(w, body)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	identityTestTarget(t, srv.URL)
	t.Setenv("IDENTITY_TEST_TOKEN", "opaque-human-session")
	prev := identityImportTarget
	identityImportTarget = func(ctx context.Context, env string, f *cloudIdentityFlags) (identityImporter, error) {
		client, req, err := f.target(ctx, env) // the hosted path, minus the KCL render
		if err != nil {
			return nil, err
		}
		return &cloudIdentityImporter{client: client, req: req, env: env}, nil
	}
	t.Cleanup(func() { identityImportTarget = prev })
	return &imports, srv.URL
}

func outcomeByID(w http.ResponseWriter, body map[string]any) {
	users, _ := body["users"].([]any)
	var results []map[string]string
	for _, u := range users {
		id := u.(map[string]any)["id"].(string)
		outcome, reason := "created", ""
		switch {
		case strings.HasPrefix(id, "conflict"):
			outcome, reason = "conflict", "email_taken_by_other_id"
		case strings.HasPrefix(id, "same"):
			outcome = "unchanged"
		}
		results = append(results, map[string]string{"id": id, "outcome": outcome, "reason": reason})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
}

func writeImportFile(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "users.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func importRow(id, email string) string {
	return fmt.Sprintf(`{"v":1,"id":%q,"email":%q,"email_verified":true,"password_hash":%q,"links":[{"provider":"github","provider_user_id":"583231"}],"metadata":{"plan":"pro"},"roles":["member"]}`,
		id, email, importTestHash)
}

func TestCloudIdentityImport_BatchesTheRowsAndReportsWithoutHashes(t *testing.T) {
	imports, _ := importTestControlPlane(t, outcomeByID)
	file := writeImportFile(t, importRow("u1", "a@x.example"), importRow("same-u2", "b@x.example"), `{"v":1,"id":"broken","password":"hunter2"}`, importRow("conflict-u3", "c@x.example"))
	report := filepath.Join(t.TempDir(), "report.jsonl")

	out, importErr := executeIdentity("import", "prod", "--file", file, "--batch", "2", "--update-existing", "--report", report)
	var ec exitCodeError
	if !errors.As(importErr, &ec) || ec.ExitCode() != exitConflict {
		t.Fatalf("err = %v, want exit %d", importErr, exitConflict)
	}
	if len(*imports) != 2 {
		t.Fatalf("%d ImportUsers calls, want 2 batches", len(*imports))
	}
	first := (*imports)[0]
	if first["orgId"] != "authenticated-org" || first["environmentId"] != "exact-env" || first["dryRun"] != false ||
		first["updateExisting"] != true || first["updatePasswords"] != false {
		t.Errorf("request = %v", first)
	}
	if _, ok := first["identity"]; ok {
		t.Error("the request names an identity; the default identity is the only one until --identity exists")
	}
	users := first["users"].([]any)
	u1 := users[0].(map[string]any)
	if len(users) != 2 || u1["id"] != "u1" || u1["passwordHash"] != importTestHash || u1["emailVerified"] != true ||
		u1["links"].([]any)[0].(map[string]any)["providerUserId"] != "583231" || u1["roles"].([]any)[0] != "member" {
		t.Errorf("first batch = %v", users)
	}
	if second := (*imports)[1]["users"].([]any); len(second) != 1 || second[0].(map[string]any)["id"] != "conflict-u3" {
		t.Errorf("second batch = %v (the unreadable row must not be sent)", second)
	}

	for _, want := range []string{"Imported 4 row(s)", "created 1, unchanged 1, conflict 1, invalid 1",
		"line 3  broken  invalid  unreadable: there is no plaintext", "line 4  conflict-u3  conflict  email_taken_by_other_id"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(raw)), "\n"); len(lines) != 4 || !strings.Contains(lines[0], `"outcome":"created"`) {
		t.Errorf("report = %s", raw)
	}
	for _, leaked := range []string{importTestHash, "N9qo8uLOickgx2ZMRZoMy", "hunter2"} {
		if strings.Contains(out, leaked) || strings.Contains(string(raw), leaked) || strings.Contains(importErr.Error(), leaked) {
			t.Fatalf("a credential reached the output or the report: %q", leaked)
		}
	}
	// Windows has no Unix permission bits (a file reads back 0666 or
	// 0444), so the mode is only meaningful elsewhere.
	if fi, _ := os.Stat(report); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("report mode = %v", fi.Mode().Perm())
	}
}

func TestCloudIdentityImport_AllGoodExitsZeroAndJSON(t *testing.T) {
	imports, _ := importTestControlPlane(t, outcomeByID)
	file := writeImportFile(t, importRow("u1", "a@x.example"), importRow("same-u2", "b@x.example"))
	out, err := executeIdentity("import", "prod", "--file", file, "--dry-run", "--update-passwords", "--json")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if (*imports)[0]["dryRun"] != true || (*imports)[0]["updatePasswords"] != true {
		t.Errorf("request = %v", (*imports)[0])
	}
	var doc identityImportDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !doc.OK || doc.ExitCode != 0 || !doc.DryRun || doc.Totals["created"] != 1 || doc.Totals["unchanged"] != 1 || len(doc.Results) != 2 || doc.Attempted != 2 {
		t.Errorf("doc = %+v", doc)
	}
}

func TestCloudIdentityImport_AnOlderControlPlaneIsToldApart(t *testing.T) {
	importTestControlPlane(t, func(w http.ResponseWriter, _ map[string]any) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"unimplemented","message":"controlplane.v1.IdentityService/ImportUsers is not implemented"}`))
	})
	file := writeImportFile(t, importRow("u1", "a@x.example"))
	report := filepath.Join(t.TempDir(), "report.jsonl")
	_, err := executeIdentity("import", "prod", "--file", file, "--report", report)
	if err == nil || !strings.Contains(err.Error(), "does not support identity import yet") || exitCodeForError(err) != exitUndetermined {
		t.Fatalf("err = %v (exit %d)", err, exitCodeForError(err))
	}
	if raw, _ := os.ReadFile(report); len(strings.TrimSpace(string(raw))) != 0 {
		t.Errorf("rows nobody attempted were reported: %s", raw)
	}
}

func TestCloudIdentityImport_FlagsAndUnreadableFiles(t *testing.T) {
	imports, _ := importTestControlPlane(t, outcomeByID)
	if _, err := executeIdentity("import", "prod"); err == nil || !strings.Contains(err.Error(), "--file is required") {
		t.Errorf("no --file: %v", err)
	}
	file := writeImportFile(t, importRow("u1", "a@x.example"))
	if _, err := executeIdentity("import", "prod", "--file", file, "--batch", "501"); err == nil || !strings.Contains(err.Error(), "--batch must be 1..500") {
		t.Errorf("--batch 501: %v", err)
	}
	csv := filepath.Join(t.TempDir(), "users.csv")
	_ = os.WriteFile(csv, []byte("id,email,password\nu1,a@x.example,hunter2\n"), 0o600)
	if _, err := executeIdentity("import", "prod", "--file", csv); err == nil || !strings.Contains(err.Error(), `no plaintext "password" column`) {
		t.Errorf("plaintext column: %v", err)
	}
	// Every row unreadable: nothing to send, and the identity is never asked.
	bad := writeImportFile(t, `{"v":3,"id":"u1","email":"a@x.example"}`)
	if _, err := executeIdentity("import", "prod", "--file", bad); exitCodeForError(err) != exitConflict {
		t.Errorf("all-invalid file: %v", err)
	}
	if len(*imports) != 0 {
		t.Errorf("%d ImportUsers calls for files that never parsed", len(*imports))
	}
}
