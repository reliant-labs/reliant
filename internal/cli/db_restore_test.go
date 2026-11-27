package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// The scaffolded shape (internal/templates/deploy/kcl/cloud/main.k.tmpl): a
// top-level _db, listed by name in the Bundle's databases.
const dbRestoreScaffold = `import forge

# The database.
_db = forge.ManagedDatabase {
    name = "shop"
    runtime = forge.OnHosted {}
}

bundle = forge.Bundle {
    project = "shop"
    databases = [_db]
}
`

func TestRestoreInKCLDeclaresANewDatabaseBesideTheSource(t *testing.T) {
	got, source, err := restoreInKCL(dbRestoreScaffold, "", "", "2026-11-20T15:02:00Z")
	if err != nil {
		t.Fatal(err)
	}
	want := `import forge

# The database.
_db = forge.ManagedDatabase {
    name = "shop"
    runtime = forge.OnHosted {}
}

# shop as it was at 2026-11-20T15:02:00Z (forge db restore). The app still reads shop:
# check the rows, then pass this database's name where the env passes shop's.
_shop_restored = forge.ManagedDatabase {
    name = "shop-restored"
    runtime = forge.OnHosted {}
    spec = {
        restore = {
            sourceDatabase = "shop"
            pointInTime = "2026-11-20T15:02:00Z"
        }
    }
}

bundle = forge.Bundle {
    project = "shop"
    databases = [_db, _shop_restored]
}
`
	if source != "shop" || got != want {
		t.Errorf("source %q, got:\n%s\nwant:\n%s", source, got, want)
	}
}

func TestRestoreInKCLInlineDatabaseAndMultilineList(t *testing.T) {
	src := `import forge as f

bundle = f.Bundle {
    databases = [
        f.ManagedDatabase {name = "a", runtime = f.OnCluster {target = _t}, namespace = "data"}
        f.ManagedDatabase {name = "b"}
    ]
}
`
	got, _, err := restoreInKCL(src, "a", "a-inc12", "2026-11-20T15:02:00Z")
	if err != nil {
		t.Fatal(err)
	}
	want := `import forge as f

bundle = f.Bundle {
    databases = [
        f.ManagedDatabase {name = "a", runtime = f.OnCluster {target = _t}, namespace = "data"}
        f.ManagedDatabase {name = "b"}
        f.ManagedDatabase {name = "a-inc12", runtime = f.OnCluster {target = _t}, namespace = "data", spec = {restore = {sourceDatabase = "a", pointInTime = "2026-11-20T15:02:00Z"}}}
    ]
}
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRestoreInKCLRefusesWhatItCannotPlace(t *testing.T) {
	for name, tc := range map[string]struct{ src, source string }{
		"two databases, none named": {src: `import forge
_a = forge.ManagedDatabase {name = "a"}
_b = forge.ManagedDatabase {name = "b"}
bundle = forge.Bundle {databases = [_a, _b]}
`},
		"computed name": {src: `import forge
_n = "shop"
_db = forge.ManagedDatabase {name = _n}
bundle = forge.Bundle {databases = [_db]}
`},
		"no such source": {src: dbRestoreScaffold, source: "orders"},
		"declared elsewhere": {src: `import ..base.env as envbase
output = envbase.render(envbase.bundle("prod"))
`},
		"databases list computed": {src: `import forge
_db = forge.ManagedDatabase {name = "shop"}
bundle = forge.Bundle {databases = [_db] + _more}
`},
		"source not listed": {src: `import forge
_db = forge.ManagedDatabase {name = "shop"}
bundle = forge.Bundle {databases = _dbs}
`},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := restoreInKCL(tc.src, tc.source, "", "2026-11-20T15:02:00Z")
			var anchor *errDBRestoreNoAnchor
			if !errors.As(err, &anchor) {
				t.Fatalf("want a refusal with the lines to add by hand, got %v", err)
			}
			if !strings.Contains(err.Error(), "Nothing was written") || !strings.Contains(err.Error(), "restore = {") {
				t.Errorf("the refusal does not print the declaration: %v", err)
			}
		})
	}
}

func TestRestoreInKCLNeverReusesAName(t *testing.T) {
	src := strings.Replace(dbRestoreScaffold, "databases = [_db]", "databases = [_db, _old]", 1) +
		"_old = forge.ManagedDatabase {name = \"shop-restored\"}\n"
	if _, _, err := restoreInKCL(src, "shop", "", "2026-11-20T15:02:00Z"); err == nil || !strings.Contains(err.Error(), "--as") {
		t.Fatalf("a taken default name must ask for --as, got %v", err)
	}
	if _, _, err := restoreInKCL(dbRestoreScaffold, "", "Shop_Restored", "2026-11-20T15:02:00Z"); err == nil || !strings.Contains(err.Error(), "RFC-1123") {
		t.Fatalf("an invalid --as must be refused, got %v", err)
	}
}

func TestDBRestorePointInTime(t *testing.T) {
	now := time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC)
	if got, err := dbRestorePointInTime("2026-11-20T10:02:00-05:00", now); err != nil || got != "2026-11-20T15:02:00Z" {
		t.Errorf("an offset time is spelled in UTC: got %q, %v", got, err)
	}
	for _, bad := range []string{"", "2026-11-20 15:02", "2026-11-20T17:00:00Z"} {
		if _, err := dbRestorePointInTime(bad, now); err == nil {
			t.Errorf("--to %q was accepted", bad)
		}
	}
}

func TestRunDBRestoreWritesRendersAndRestoresOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deploy", "kcl", "prod", "main.k")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func() {
		if err := os.WriteFile(path, []byte(dbRestoreScaffold), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	now := func() time.Time { return time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC) }
	opts := dbRestoreOptions{to: "2026-11-20T15:02:00Z", now: now}
	orig := dbRestoreRender
	t.Cleanup(func() { dbRestoreRender = orig })

	// dry run: prints the edit, writes nothing, renders nothing.
	write()
	dbRestoreRender = func(context.Context, string, string) ([]DatabaseEntity, error) {
		t.Fatal("a dry run rendered")
		return nil, nil
	}
	var out bytes.Buffer
	dry := opts
	dry.dryRun = true
	if err := runDBRestore(context.Background(), &out, dir, "prod", dry); err != nil {
		t.Fatal(err)
	}
	if read() != dbRestoreScaffold || !strings.Contains(out.String(), `pointInTime = "2026-11-20T15:02:00Z"`) ||
		!strings.Contains(out.String(), "+     databases = [_db, _shop_restored]") {
		t.Errorf("dry run wrote, or printed no edit:\n%s", out.String())
	}

	// The render carries the restore: the edit is kept.
	dbRestoreRender = func(context.Context, string, string) ([]DatabaseEntity, error) {
		return []DatabaseEntity{
			{Name: "shop"},
			{Name: "shop-restored", Spec: deployv1alpha1.ManagedDatabaseSpec{Restore: &deployv1alpha1.DatabaseRestore{SourceDatabase: "shop", PointInTime: "2026-11-20T15:02:00Z"}}},
		}, nil
	}
	out.Reset()
	if err := runDBRestore(context.Background(), &out, dir, "prod", opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(), `name = "shop-restored"`) || !strings.Contains(out.String(), "declared database shop-restored in deploy/kcl/prod/main.k") ||
		!strings.Contains(out.String(), "--database shop-restored") {
		t.Errorf("the edit was not kept, or the next steps are missing:\n%s", out.String())
	}

	// The render does not carry it (the Bundle is built elsewhere): the file
	// is put back.
	write()
	dbRestoreRender = func(context.Context, string, string) ([]DatabaseEntity, error) {
		return []DatabaseEntity{{Name: "shop"}}, nil
	}
	err := runDBRestore(context.Background(), &out, dir, "prod", opts)
	if err == nil || !strings.Contains(err.Error(), "is unchanged") || read() != dbRestoreScaffold {
		t.Fatalf("a render without the restore must restore the file, got %v", err)
	}
}

// The render must carry BOTH databases: a render with the restored copy but
// no source (a Bundle that lists the copy and not the database it restores
// from) is refused, as is one with the source and no copy.
func TestRenderedRestoreNeedsTheSourceAndTheCopy(t *testing.T) {
	restored := DatabaseEntity{Name: "shop-restored", Spec: deployv1alpha1.ManagedDatabaseSpec{
		Restore: &deployv1alpha1.DatabaseRestore{SourceDatabase: "shop", PointInTime: "2026-11-20T15:02:00Z"}}}
	if err := renderedRestore([]DatabaseEntity{restored}, "shop", "shop-restored", "2026-11-20T15:02:00Z"); err == nil ||
		!strings.Contains(err.Error(), `no database "shop" to restore from`) {
		t.Errorf("a render with only the restored copy passed: %v", err)
	}
	if err := renderedRestore([]DatabaseEntity{{Name: "shop"}}, "shop", "shop-restored", "2026-11-20T15:02:00Z"); err == nil {
		t.Error("a render with only the source passed")
	}
	if err := renderedRestore([]DatabaseEntity{{Name: "shop"}, restored}, "shop", "shop-restored", "2026-11-20T15:02:00Z"); err != nil {
		t.Errorf("a render with both failed: %v", err)
	}
}
