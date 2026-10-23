package closedset

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testSets stands in for the registry so a test pins the behaviour, not
// forge's current scope list.
func testSets(sets map[string][]string) Resolver {
	return func(name string) ([]string, error) {
		if v, ok := sets[name]; ok {
			return v, nil
		}
		return nil, fmt.Errorf("forge:closed-set %s names no set forge exports", name)
	}
}

var scopes = map[string][]string{"accesstoken.AllScopes": {"deploy:read", "deploy:write", "identity:read"}}

func writeMigrations(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i+1 < len(files); i += 2 {
		if err := os.WriteFile(filepath.Join(dir, files[i]), []byte(files[i+1]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func analyze(t *testing.T, dir string, r Resolver) []Finding {
	t.Helper()
	fs, err := Analyze(dir, r)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

const baseline = `-- forge:backward-compatible — baseline
CREATE TABLE public.access_tokens (
    id text PRIMARY KEY,
    scopes text[] NOT NULL,
    -- a comment naming CONSTRAINT decoy CHECK (x IN ('no')) is prose
    CONSTRAINT access_tokens_scopes CHECK (((cardinality(scopes) > 0) AND (scopes <@ ARRAY['deploy:read'::text, 'deploy:write'::text])))
);
`

const declare = `-- forge:backward-compatible — a comment
COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS
    'forge:closed-set accesstoken.AllScopes';
`

func TestAnalyze_InSyncPasses(t *testing.T) {
	sets := map[string][]string{"accesstoken.AllScopes": {"deploy:write", "deploy:read"}}
	dir := writeMigrations(t, "0001_base.up.sql", baseline, "0002_declare.up.sql", declare)
	if fs := analyze(t, dir, testSets(sets)); len(fs) != 0 {
		t.Fatalf("in-sync CHECK reported: %+v", fs)
	}
}

// The forge #1060 shape: the set gained a value, the CHECK did not.
func TestAnalyze_SetGainedAValue(t *testing.T) {
	dir := writeMigrations(t, "0001_base.up.sql", baseline, "0002_declare.up.sql", declare)
	fs := analyze(t, dir, testSets(scopes))
	if len(fs) != 1 {
		t.Fatalf("want one finding, got %+v", fs)
	}
	f := fs[0]
	if !slices.Equal(f.Missing, []string{"identity:read"}) || len(f.Extra) != 0 {
		t.Fatalf("missing=%q extra=%q", f.Missing, f.Extra)
	}
	if filepath.Base(f.File) != "0001_base.up.sql" || f.Line != 6 {
		t.Errorf("located at %s:%d, want the defining line 0001_base.up.sql:6", f.File, f.Line)
	}
	if !strings.Contains(f.Problem, "lacks 'identity:read'") {
		t.Errorf("problem does not name the value: %s", f.Problem)
	}
	if f.Fix == nil || !f.Fix.BackwardCompatible {
		t.Fatalf("a widening fix is backward-compatible: %+v", f.Fix)
	}
	for _, want := range []string{
		"-- forge:backward-compatible — widens access_tokens_scopes",
		"ALTER TABLE public.access_tokens\n    DROP CONSTRAINT access_tokens_scopes,\n    ADD CONSTRAINT access_tokens_scopes CHECK (((cardinality(scopes) > 0) AND (scopes <@ ARRAY['deploy:read'::text, 'deploy:write'::text, 'identity:read'::text])));",
		"COMMENT ON CONSTRAINT access_tokens_scopes ON public.access_tokens IS\n    'forge:closed-set accesstoken.AllScopes';",
	} {
		if !strings.Contains(f.Fix.Migration, want) {
			t.Errorf("fix migration lacks %q:\n%s", want, f.Fix.Migration)
		}
	}

	// The printed fix is the whole repair: as the next migration, the
	// analysis comes back clean.
	if err := os.WriteFile(filepath.Join(dir, "0003_fix.up.sql"), []byte(f.Fix.Migration), 0o644); err != nil {
		t.Fatal(err)
	}
	if fs := analyze(t, dir, testSets(scopes)); len(fs) != 0 {
		t.Fatalf("after the printed fix: %+v", fs)
	}
}

func TestAnalyze_CheckAdmitsAValueTheSetLacks(t *testing.T) {
	sets := map[string][]string{"accesstoken.AllScopes": {"deploy:read"}}
	dir := writeMigrations(t, "0001_base.up.sql", baseline, "0002_declare.up.sql", declare)
	fs := analyze(t, dir, testSets(sets))
	if len(fs) != 1 || !slices.Equal(fs[0].Extra, []string{"deploy:write"}) {
		t.Fatalf("want deploy:write extra, got %+v", fs)
	}
	if fs[0].Fix.BackwardCompatible || !strings.Contains(fs[0].Fix.Migration, "-- forge:breaks-rollback narrows") {
		t.Fatalf("a narrowing fix breaks rollback:\n%s", fs[0].Fix.Migration)
	}
}

func TestAnalyze_UnknownSetFails(t *testing.T) {
	dir := writeMigrations(t, "0001_base.up.sql", baseline, "0002_declare.up.sql",
		strings.Replace(declare, "accesstoken.AllScopes", "accesstoken.AllScops", 1))
	fs := analyze(t, dir, NewResolver(dir))
	if len(fs) != 1 || !strings.Contains(fs[0].Problem, "accesstoken.AllScops names no set forge exports") ||
		!strings.Contains(fs[0].Problem, "accesstoken.AllScopes") {
		t.Fatalf("want an unknown-set finding listing the registry, got %+v", fs)
	}
	if fs[0].Fix != nil {
		t.Errorf("no fix for an unknown set: %+v", fs[0].Fix)
	}
}

func TestAnalyze_NamesNoSet(t *testing.T) {
	dir := writeMigrations(t, "0001_base.up.sql", baseline, "0002_declare.up.sql",
		strings.Replace(declare, " accesstoken.AllScopes", "", 1))
	fs := analyze(t, dir, testSets(scopes))
	if len(fs) != 1 || !strings.Contains(fs[0].Problem, "names no set") {
		t.Fatalf("got %+v", fs)
	}
}

// control-plane's shape: a schema-qualified table, the CHECK replaced in
// two ALTER statements, and the declaration — which the DROP took with it —
// never re-issued.
func TestAnalyze_ReplacedCheckLosesItsDeclaration(t *testing.T) {
	replace := `-- forge:backward-compatible — widen
ALTER TABLE controlplane.access_tokens
    DROP CONSTRAINT IF EXISTS access_tokens_scopes;

ALTER TABLE controlplane.access_tokens
    ADD CONSTRAINT access_tokens_scopes
    CHECK (
        cardinality(scopes) > 0
        AND scopes <@ ARRAY[
            'deploy:read',
            'deploy:write'
        ]::TEXT[]
    );
`
	dir := writeMigrations(t, "0001_base.up.sql", baseline, "0002_declare.up.sql", declare, "0003_replace.up.sql", replace)
	fs := analyze(t, dir, testSets(scopes))
	if len(fs) != 1 {
		t.Fatalf("want one finding, got %+v", fs)
	}
	f := fs[0]
	if !strings.Contains(f.Problem, "the declaration is gone") || !strings.Contains(f.Problem, "lacks 'identity:read'") {
		t.Fatalf("want the lost declaration AND the drift reported: %s", f.Problem)
	}
	if filepath.Base(f.File) != "0003_replace.up.sql" {
		t.Errorf("located at %s, want the re-creating migration", f.File)
	}
	want := "AND scopes <@ ARRAY[\n            'deploy:read',\n            'deploy:write',\n            'identity:read'\n        ]::TEXT[]"
	if !strings.Contains(f.Fix.SQL, want) || !strings.Contains(f.Fix.SQL, "COMMENT ON CONSTRAINT access_tokens_scopes ON controlplane.access_tokens") {
		t.Fatalf("fix keeps the layout and re-declares:\n%s", f.Fix.SQL)
	}

	// In sync but undeclared: the fix is the COMMENT alone.
	sets := map[string][]string{"accesstoken.AllScopes": {"deploy:read", "deploy:write"}}
	fs = analyze(t, dir, testSets(sets))
	if len(fs) != 1 || fs[0].Fix == nil || strings.Contains(fs[0].Fix.SQL, "ALTER TABLE") ||
		!strings.Contains(fs[0].Fix.SQL, "COMMENT ON CONSTRAINT") {
		t.Fatalf("want a comment-only fix, got %+v", fs)
	}
	// A COMMENT after the ADD restores the declaration.
	_ = os.WriteFile(filepath.Join(dir, "0004_redeclare.up.sql"), []byte(fs[0].Fix.Migration), 0o644)
	if fs := analyze(t, dir, testSets(sets)); len(fs) != 0 {
		t.Fatalf("after re-declaring: %+v", fs)
	}
}

// One ALTER TABLE that drops and re-adds, then re-comments — reliant's
// shape — is fully modelled within a statement.
func TestAnalyze_DropAddInOneStatement(t *testing.T) {
	replace := `-- forge:backward-compatible — widen
ALTER TABLE access_tokens
    DROP CONSTRAINT access_tokens_scopes,
    ADD CONSTRAINT access_tokens_scopes CHECK (
        cardinality(scopes) > 0
        AND scopes <@ ARRAY['deploy:read', 'deploy:write', 'identity:read']::text[]
    );
COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS 'forge:closed-set accesstoken.AllScopes';
`
	dir := writeMigrations(t, "0001_base.up.sql", baseline, "0002_declare.up.sql", declare, "0003_replace.up.sql", replace)
	if fs := analyze(t, dir, testSets(scopes)); len(fs) != 0 {
		t.Fatalf("got %+v", fs)
	}
}

func TestAnalyze_ScalarAnyAndPlacement(t *testing.T) {
	m := `-- forge:backward-compatible — x
CREATE TABLE grants (
    id text PRIMARY KEY,
    owner_id text,
    resource_kind text,
    CONSTRAINT grants_kind CHECK (((resource_kind IS NULL) OR (resource_kind = ANY (ARRAY['daemon'::text, 'port'::text])))),
    CONSTRAINT grants_owner_fkey FOREIGN KEY (owner_id) REFERENCES owners(id)
);
COMMENT ON CONSTRAINT grants_kind ON grants IS 'forge:closed-set accesstoken.AllResourceKinds';
COMMENT ON CONSTRAINT grants_owner_fkey ON grants IS 'forge:closed-set accesstoken.AllResourceKinds';
`
	dir := writeMigrations(t, "0001.up.sql", m)
	fs := analyze(t, dir, testSets(map[string][]string{"accesstoken.AllResourceKinds": {"daemon", "port", "connector"}}))
	if len(fs) != 2 {
		t.Fatalf("want two findings, got %+v", fs)
	}
	if !slices.Equal(fs[0].Missing, []string{"connector"}) {
		t.Errorf("= ANY form: missing=%q", fs[0].Missing)
	}
	if !strings.Contains(fs[1].Problem, "is a FOREIGN constraint") {
		t.Errorf("FK placement: %s", fs[1].Problem)
	}
}

func TestAnalyze_DroppedConstraintAndOrphanComment(t *testing.T) {
	drop := "ALTER TABLE access_tokens DROP CONSTRAINT access_tokens_scopes;\n"
	dir := writeMigrations(t, "0001_base.up.sql", baseline, "0002_declare.up.sql", declare, "0003_drop.up.sql", drop)
	if fs := analyze(t, dir, testSets(scopes)); len(fs) != 0 {
		t.Fatalf("a dropped constraint takes its declaration with it: %+v", fs)
	}
	dir = writeMigrations(t, "0001_declare.up.sql", declare)
	if fs := analyze(t, dir, testSets(scopes)); len(fs) != 1 || !strings.Contains(fs[0].Problem, "no earlier migration defines") {
		t.Fatalf("got %+v", fs)
	}
}

// The registry's names are the Go identifiers they stand for, and each
// resolves through the default resolver without loading any package.
func TestRegistry(t *testing.T) {
	r := NewResolver(t.TempDir())
	for _, n := range Names() {
		if IsQualified(n) {
			t.Errorf("%s: a registry name has no import path", n)
		}
		if v, err := r(n); err != nil || len(v) == 0 {
			t.Errorf("%s: %v %v", n, v, err)
		}
	}
	if v, _ := r("accesstoken.AllScopes"); !slices.Contains(v, "identity:manage") {
		t.Errorf("accesstoken.AllScopes = %q", v)
	}
}
