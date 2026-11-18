package hostinfra

import (
	"os"
	"path/filepath"
	"testing"
)

func withIDPHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	prev := idpHomeDir
	idpHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { idpHomeDir = prev })
	return home
}

// Zitadel writes the bootstrap PAT once, when it initializes its database, so
// the PAT belongs to the database: at the default idp_pat_path it lives in a
// per-machine file keyed by that database, which every checkout whose IdP
// uses it finds. A declared idp_pat_path is the author's and stays the only
// location.
func TestIDPPATLocations(t *testing.T) {
	home := withIDPHome(t)
	project := filepath.Join(t.TempDir(), "clone")
	checkout := filepath.Join(project, ".forge", "idp", "pat.txt")
	shared := filepath.Join(home, ".forge", "idp", "localhost_5434_zitadel_idp", "pat.txt")

	for _, tc := range []struct {
		name         string
		spec         Spec
		wantShared   string
		wantCheckout string
	}{
		{"unset path", Spec{IDPDatabase: "zitadel_idp", IDPDatabasePort: 5434}, shared, checkout},
		{"the schema default", Spec{IDPPATPath: ".forge/idp/pat.txt", IDPDatabase: "zitadel_idp", IDPDatabasePort: 5434}, shared, checkout},
		{"a declared path is the only one", Spec{IDPPATPath: "secrets/pat.txt", IDPDatabase: "zitadel_idp", IDPDatabasePort: 5434}, "", filepath.Join(project, "secrets", "pat.txt")},
		{"no database port: no identity to key by", Spec{IDPDatabase: "zitadel_idp"}, "", checkout},
		{"unsafe characters in the database name", Spec{IDPDatabase: "a/b c", IDPDatabasePort: 1}, filepath.Join(home, ".forge", "idp", "localhost_1_"+databaseKey("a/b c"), "pat.txt"), checkout},
	} {
		got := tc.spec.IDPPATLocations(project)
		if got.Shared != tc.wantShared || got.Checkout != tc.wantCheckout {
			t.Errorf("%s: got %+v, want shared %q checkout %q", tc.name, got, tc.wantShared, tc.wantCheckout)
		}
		want := tc.wantShared
		if want == "" {
			want = tc.wantCheckout
		}
		if got.WritePath() != want {
			t.Errorf("%s: WritePath() = %q, want %q (Zitadel writes beside the database when it can)", tc.name, got.WritePath(), want)
		}
	}
}

func TestIDPPATLocations_FindPublishMirror(t *testing.T) {
	dir := t.TempDir()
	loc := IDPPATLocations{Shared: filepath.Join(dir, "home", "pat.txt"), Checkout: filepath.Join(dir, "clone", "pat.txt")}
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	if got := loc.Find(); got != "" {
		t.Fatalf("Find() with neither file = %q, want empty", got)
	}
	if err := loc.MirrorToCheckout(); err != nil {
		t.Fatalf("mirroring a missing shared file is a no-op: %v", err)
	}

	// An older forge's database: the token is only in the checkout.
	write(loc.Checkout, "old-forge-token")
	if got := loc.Find(); got != loc.Checkout {
		t.Fatalf("Find() = %q, want the checkout fallback", got)
	}
	if err := loc.PublishShared(loc.Checkout); err != nil {
		t.Fatal(err)
	}
	if read(loc.Shared) != "old-forge-token" {
		t.Fatal("PublishShared did not copy the token")
	}
	// Its mode is asserted where POSIX modes exist: idp_pat_unix_test.go.

	// The shared file is what Zitadel writes: it wins, and the checkout copy follows it.
	write(loc.Shared, "current-token")
	if got := loc.Find(); got != loc.Shared {
		t.Fatalf("Find() = %q, want the shared file", got)
	}
	if err := loc.MirrorToCheckout(); err != nil {
		t.Fatal(err)
	}
	if read(loc.Checkout) != "current-token" {
		t.Error("MirrorToCheckout did not refresh the checkout copy")
	}

	// An empty file is not a token.
	write(loc.Shared, "")
	if got := loc.Find(); got != loc.Checkout {
		t.Errorf("Find() with an empty shared file = %q, want the checkout copy", got)
	}
}

// The shared PAT file is keyed by the database's identity, so two databases
// whose names sanitize to the same characters must not share one: the second
// bootstrap would overwrite the first database's token.
func TestIDPPATLocations_DistinctDatabasesDistinctFiles(t *testing.T) {
	withIDPHome(t)
	seen := map[string]string{}
	for _, db := range []string{"a-b/c", "a-b_c", "a-b c", "a-b\\c", "zitadel"} {
		p := Spec{IDPDatabase: db, IDPDatabasePort: 5434}.IDPPATLocations(t.TempDir()).Shared
		if other, ok := seen[p]; ok {
			t.Errorf("databases %q and %q share the PAT file %s", other, db, p)
		}
		seen[p] = db
		if filepath.Base(filepath.Dir(p)) != "localhost_5434_"+databaseKey(db) {
			t.Errorf("%q: key dir %s", db, p)
		}
	}
	if databaseKey("zitadel_idp") != "zitadel_idp" {
		t.Errorf("a safe name is its own key, got %q", databaseKey("zitadel_idp"))
	}
}

// A fresh IdP boot writes its token only to the shared file; the declared
// checkout path must still receive it, for an env with no managed identity
// whose provisioning reads .forge/idp/pat.txt. A boot that wrote nothing
// leaves the checkout copy alone (the identity step's fallback needs it).
func TestIDPPATLocations_MirrorIfRewritten(t *testing.T) {
	home := withIDPHome(t)
	project := t.TempDir()
	loc := Spec{IDPDatabase: "zitadel_idp", IDPDatabasePort: 5434}.IDPPATLocations(project)
	if loc.Shared == "" || filepath.Dir(filepath.Dir(filepath.Dir(loc.Shared))) != filepath.Join(home, ".forge") {
		t.Fatalf("unexpected shared path %q", loc.Shared)
	}

	before := loc.SharedContent()
	if before != nil {
		t.Fatalf("SharedContent() with no file = %q, want nil", before)
	}
	// Zitadel's first-instance setup writes the shared file.
	if err := os.MkdirAll(filepath.Dir(loc.Shared), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loc.Shared, []byte("fresh-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loc.MirrorIfRewritten(before); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(loc.Checkout)
	if err != nil || string(b) != "fresh-token" {
		t.Fatalf("checkout copy after a fresh boot = %q, %v; want the new token", b, err)
	}

	// A later boot against the initialized database writes nothing: an
	// older checkout token is not overwritten.
	if err := os.WriteFile(loc.Checkout, []byte("older-forge-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loc.MirrorIfRewritten(loc.SharedContent()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(loc.Checkout); string(b) != "older-forge-token" {
		t.Errorf("an unchanged shared file overwrote the checkout copy: %q", b)
	}
}
