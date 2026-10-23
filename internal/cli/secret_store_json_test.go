package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// canaryValue is written into the fixture store as a real secret value. No
// assertion in this file may ever relax to "redacted" or "truncated" — the
// contract `forge secret list` documents is that values are ABSENT.
const canaryValue = "SUPERSECRETVALUE-DO-NOT-LEAK"

// writeSecretListFixture stages a KCL render fixture plus a secret store and
// returns the store path. The provider path is absolute so the test does not
// depend on the process's working directory resolving to a forge project.
func writeSecretListFixture(t *testing.T, storeBody string, declaredJSON string) string {
	t.Helper()
	dir := t.TempDir()
	storePath := filepath.Join(dir, "dev.yaml")
	if storeBody != "" {
		if err := os.WriteFile(storePath, []byte(storeBody), 0o600); err != nil {
			t.Fatalf("write store: %v", err)
		}
	}

	fixture := `{"output": {
  "workloads": [` + declaredJSON + `],
  "secret_provider": {"type": "file", "path": ` + jsonString(storePath) + `}
}}`
	fixturePath := filepath.Join(dir, "render.json")
	if err := os.WriteFile(fixturePath, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", fixturePath)
	return storePath
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// apiDeclaresTwo declares one secret that the store holds and one it does not.
const apiDeclaresTwo = `
    {
      "name": "api", "kind": "service",
      "runtime": {"type": "host"},
      "spec": {"kind": "service", "env": [
        {"name": "STRIPE_SECRET_KEY", "secretRef": {"name": "app-secrets"}},
        {"name": "MISSING_TOKEN", "secretRef": {"name": "app-secrets", "key": "missing_token"}}
      ]}
    }`

func runListJSON(t *testing.T) (secretListReport, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := runSecretListJSON(context.Background(), "dev", &buf); err != nil {
		t.Fatalf("runSecretListJSON: %v", err)
	}
	var report secretListReport
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, buf.String())
	}
	return report, buf.Bytes()
}

// THE load-bearing test. A secret value sits in the store, is declared, and is
// reported present — and the value itself must not appear anywhere in the
// emitted bytes, in any encoding.
func TestSecretListJSONNeverEmitsAValue(t *testing.T) {
	writeSecretListFixture(t,
		"STRIPE_SECRET_KEY: "+canaryValue+"\nINERT_KEY: "+canaryValue+"\n",
		apiDeclaresTwo)

	report, raw := runListJSON(t)

	if bytes.Contains(raw, []byte(canaryValue)) {
		t.Fatalf("secret VALUE leaked into --json output:\n%s", raw)
	}
	// Also catch a value that was escaped or re-encoded on the way out.
	if strings.Contains(string(raw), "SUPERSECRET") {
		t.Fatalf("fragment of a secret value leaked into --json output:\n%s", raw)
	}
	// The test is only meaningful if the canary was actually in play: the
	// declared key must be reported present and the inert key must be listed.
	if !presenceOf(t, report, "STRIPE_SECRET_KEY") {
		t.Fatal("STRIPE_SECRET_KEY not reported present; the canary was never exercised")
	}
	if len(report.Inert) != 1 || report.Inert[0] != "INERT_KEY" {
		t.Fatalf("inert = %v, want [INERT_KEY]", report.Inert)
	}
}

func presenceOf(t *testing.T, r secretListReport, name string) bool {
	t.Helper()
	for _, s := range r.Secrets {
		if s.Name == name {
			return s.Present
		}
	}
	t.Fatalf("secret %q absent from report %+v", name, r.Secrets)
	return false
}

func TestSecretListJSONDeclaredPresentAndMissing(t *testing.T) {
	storePath := writeSecretListFixture(t,
		"STRIPE_SECRET_KEY: "+canaryValue+"\n",
		apiDeclaresTwo)

	report, _ := runListJSON(t)

	if report.Env != "dev" {
		t.Errorf("env = %q, want dev", report.Env)
	}
	if report.Provider != "file" {
		t.Errorf("provider = %q, want file", report.Provider)
	}
	if report.StorePath != storePath {
		t.Errorf("store_path = %q, want %q", report.StorePath, storePath)
	}
	if !report.StoreExists {
		t.Error("store_exists = false, want true")
	}
	if len(report.Secrets) != 2 {
		t.Fatalf("got %d secrets, want 2: %+v", len(report.Secrets), report.Secrets)
	}
	// Sorted by name: MISSING_TOKEN then STRIPE_SECRET_KEY.
	if report.Secrets[0].Name != "MISSING_TOKEN" || report.Secrets[0].Present {
		t.Errorf("secrets[0] = %+v, want MISSING_TOKEN present=false", report.Secrets[0])
	}
	if report.Secrets[1].Name != "STRIPE_SECRET_KEY" || !report.Secrets[1].Present {
		t.Errorf("secrets[1] = %+v, want STRIPE_SECRET_KEY present=true", report.Secrets[1])
	}

	// Attribution: which workload declares it, and through which Secret/key.
	decl := report.Secrets[1].DeclaredBy
	if len(decl) != 1 || decl[0].Workload != "api" || decl[0].Kind != "service" {
		t.Fatalf("declared_by = %+v, want one service \"api\"", decl)
	}
	if decl[0].SecretName != "app-secrets" || decl[0].SecretKey != "STRIPE_SECRET_KEY" {
		t.Errorf("declared_by[0] = %+v, want app-secrets/STRIPE_SECRET_KEY", decl[0])
	}

	// The ensure gate's dimension.
	if report.MissingCount != 1 || len(report.Missing) != 1 || report.Missing[0] != "MISSING_TOKEN" {
		t.Errorf("missing = %v (count %d), want [MISSING_TOKEN]", report.Missing, report.MissingCount)
	}
	if report.OK {
		t.Error("ok = true with a declared secret missing a value")
	}
}

func TestSecretListJSONAllPresentIsOK(t *testing.T) {
	writeSecretListFixture(t,
		"STRIPE_SECRET_KEY: "+canaryValue+"\nMISSING_TOKEN: "+canaryValue+"\n",
		apiDeclaresTwo)

	report, raw := runListJSON(t)

	if bytes.Contains(raw, []byte(canaryValue)) {
		t.Fatalf("secret VALUE leaked:\n%s", raw)
	}
	if !report.OK || report.MissingCount != 0 {
		t.Errorf("ok = %v, missing_count = %d; want true/0", report.OK, report.MissingCount)
	}
	if len(report.Inert) != 0 {
		t.Errorf("inert = %v, want empty", report.Inert)
	}
}

// "No store file at all" must be distinguishable from "store exists but is
// empty" — a UI renders those two states very differently.
func TestSecretListJSONNoStoreFile(t *testing.T) {
	writeSecretListFixture(t, "", apiDeclaresTwo)

	report, _ := runListJSON(t)

	if report.StoreExists {
		t.Error("store_exists = true with no file on disk")
	}
	if report.MissingCount != 2 {
		t.Errorf("missing_count = %d, want 2", report.MissingCount)
	}
	if report.OK {
		t.Error("ok = true with no store file and two declared secrets")
	}
	for _, s := range report.Secrets {
		if s.Present {
			t.Errorf("%s reported present with no store file", s.Name)
		}
	}
}

// An env whose KCL declares nothing: a valid, empty report rather than an error.
func TestSecretListJSONNothingDeclared(t *testing.T) {
	writeSecretListFixture(t, "STRAY: "+canaryValue+"\n", `
    {"name": "api", "kind": "service", "runtime": {"type": "host"}, "spec": {"kind": "service"}}`)

	report, raw := runListJSON(t)

	if bytes.Contains(raw, []byte(canaryValue)) {
		t.Fatalf("secret VALUE leaked:\n%s", raw)
	}
	if len(report.Secrets) != 0 {
		t.Errorf("secrets = %+v, want empty", report.Secrets)
	}
	if !report.OK {
		t.Error("ok = false with nothing declared and nothing missing")
	}
	if len(report.Inert) != 1 || report.Inert[0] != "STRAY" {
		t.Errorf("inert = %v, want [STRAY]", report.Inert)
	}
}

// Exit-code parity: --json must fail exactly when text mode fails. list
// answers for every provider, so its only failure is an env that does not
// render.
func TestSecretListJSONExitParityOnBadEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", filepath.Join(dir, "does-not-exist.json"))

	var textBuf, jsonBuf bytes.Buffer
	textErr := runSecretList(context.Background(), "dev", &textBuf)
	jsonErr := runSecretListJSON(context.Background(), "dev", &jsonBuf)

	if textErr == nil {
		t.Fatal("text mode accepted an env that does not render")
	}
	if jsonErr == nil {
		t.Fatalf("--json accepted an env text mode rejected; exit codes diverge (out: %s)", jsonBuf.String())
	}
}

// The struct contract, asserted on the TYPE rather than on any one run: no
// field anywhere in the report is a channel a secret value could travel
// through. Reflection rather than a marshal is load-bearing here — an added
// `Value string` with `omitempty` is invisible to a marshal of a zero value,
// which is precisely the "added the field and left it empty" mistake this
// guards against.
func TestSecretListReportHasNoValueCarryingField(t *testing.T) {
	// Every field the report may ever emit, vetted as incapable of holding a
	// secret value. Adding to this list is a deliberate act.
	allowed := map[string]bool{
		"env": true, "provider": true, "store_path": true, "store_exists": true,
		"secrets": true, "inert": true, "missing": true, "missing_count": true,
		"ok": true, "name": true, "present": true, "declared_by": true,
		"workload": true, "kind": true, "secret_name": true, "secret_key": true,
		// presence is an enum (set|missing|unknown), version a counter,
		// verifiable a bool — none can carry a value.
		"presence": true, "version": true, "verifiable": true,
	}
	for _, f := range jsonFieldNames(reflect.TypeOf(secretListReport{}), map[reflect.Type]bool{}) {
		if !allowed[f] {
			t.Errorf("unvetted field %q in secretListReport — if it can hold a secret value it must not exist; if it is a legitimate addition, add it to this list deliberately", f)
		}
	}
}

// jsonFieldNames walks a struct type graph and returns every JSON field name
// it can emit, following slices, maps, pointers and nested structs.
func jsonFieldNames(t reflect.Type, seen map[reflect.Type]bool) []string {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return nil
	}
	seen[t] = true

	var names []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "-" {
			continue
		}
		if tag == "" {
			tag = f.Name
		}
		names = append(names, tag)
		names = append(names, jsonFieldNames(f.Type, seen)...)
	}
	return names
}
