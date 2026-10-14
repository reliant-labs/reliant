// Copyright (c) 2025 Reliant Labs
package db

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/migratekit/compat"

	forgedb "github.com/reliant-labs/reliant/db"
)

// TestBaselineAdoptsExactlyTheGooseHistory pins the fact the goose → forge
// migrations cutover rests on: the baseline in db/migrations declares, version
// for version, the goose history it replaces
// (research/forge-migration/04-p5a-cutover-runbook.md).
//
// forge's migrator adopts a goose-migrated database — records the baseline as
// applied without running it — only when goose applied EXACTLY the listed set,
// and refuses it otherwise. A goose migration merged after the baseline was
// cut is applied by the last goose release but missing from the list, so every
// database we keep, prod included, would be refused at the cutover. So goose
// is frozen from the moment the baseline exists: a schema change is a forge
// migration in db/migrations, and the only way this list changes is re-cutting
// the baseline (runbook §2).
func TestBaselineAdoptsExactlyTheGooseHistory(t *testing.T) {
	ms, err := compat.Scan(forgedb.Source(), forgedb.SourceDir)
	if err != nil {
		t.Fatalf("scan db/migrations: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("db/migrations holds no migration; the baseline is missing")
	}
	baseline := ms[0]
	if baseline.Adopts == nil || baseline.Adopts.Tool != compat.ToolGoose {
		t.Fatalf("the first migration, %s, must declare the goose history it replaces (%s goose …)", baseline.Name, compat.AdoptsDirective)
	}

	goose := gooseFileVersions(t)
	declared := baseline.Adopts.Versions
	var missing, extra []int64
	for _, v := range goose {
		if !slices.Contains(declared, v) {
			missing = append(missing, v)
		}
	}
	for _, v := range declared {
		if !slices.Contains(goose, v) {
			extra = append(extra, v)
		}
	}
	if len(missing) > 0 {
		t.Errorf("goose migration(s) %v are not in %s's adoption list. goose is frozen: once migrations run through forge's "+
			"migrator, write the change as a forge migration (`forge db migration new <name>`) instead; before that, it ships only "+
			"by re-cutting the baseline from the new goose head (runbook §2)",
			missing, baseline.Name)
	}
	if len(extra) > 0 {
		t.Errorf("%s adopts goose version(s) %v that internal/db/migrations/postgres does not hold; the list must be generated from the goose directory, never typed",
			baseline.Name, extra)
	}
}

// frozenGooseDigest is gooseHistoryDigest of internal/db/migrations/postgres
// when the baseline was cut from it.
const frozenGooseDigest = "6fe5b463fe670ff7d4e4e8d732789ae5720a0f1ed5a3965809f6e14e31e13137"

// TestGooseHistoryIsFrozen pins the CONTENT of the goose history the baseline
// was cut from, not only its version list.
//
// The baseline is a dump of the schema those exact files build. A database
// goose migrated is adopted on the promise that its schema is that dump, and a
// database still behind goose's head is first brought to it by running these
// same files. Editing one after the cut — a typo fix in a DDL, a changed
// default — would make both promises false without changing a single version:
// adopted and caught-up databases would silently differ from fresh ones. So
// the files are frozen byte for byte. A change to the schema is a new forge
// migration in db/migrations.
func TestGooseHistoryIsFrozen(t *testing.T) {
	if got := gooseHistoryDigest(t); got != frozenGooseDigest {
		t.Fatalf("internal/db/migrations/postgres changed after the baseline was cut from it (digest %s, frozen %s). "+
			"goose's history is frozen: the baseline and every database adopted or caught up from goose rely on these exact files. "+
			"Revert the change and write a forge migration instead (`forge db migration new <name>`)", got, frozenGooseDigest)
	}
}

// gooseFileVersions returns the version of every embedded goose migration,
// ascending.
func gooseFileVersions(t *testing.T) []int64 {
	t.Helper()
	entries, err := fs.ReadDir(FS, migrationsDir)
	if err != nil {
		t.Fatalf("read goose migrations: %v", err)
	}
	var versions []int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, _, _ := strings.Cut(e.Name(), "_")
		v, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			t.Fatalf("goose migration %s: version %q is not a number", e.Name(), prefix)
		}
		versions = append(versions, v)
	}
	slices.Sort(versions)
	return versions
}

// gooseHistoryDigest is a sha256 over every embedded goose file, in name
// order: name NUL hex(sha256(content)) NUL, per file. The same digest from a
// shell, run in internal/db/migrations/postgres:
//
//	for f in $(ls *.sql | sort); do printf '%s\0%s\0' "$f" "$(sha256sum "$f" | cut -d' ' -f1)"; done | sha256sum
func gooseHistoryDigest(t *testing.T) string {
	t.Helper()
	entries, err := fs.ReadDir(FS, migrationsDir)
	if err != nil {
		t.Fatalf("read goose migrations: %v", err)
	}
	h := sha256.New()
	for _, e := range entries { // fs.ReadDir returns entries sorted by name
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, err := fs.ReadFile(FS, path.Join(migrationsDir, e.Name()))
		if err != nil {
			t.Fatalf("read goose migration %s: %v", e.Name(), err)
		}
		sum := sha256.Sum256(raw)
		h.Write([]byte(e.Name()))
		h.Write([]byte{0})
		h.Write([]byte(hex.EncodeToString(sum[:])))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
