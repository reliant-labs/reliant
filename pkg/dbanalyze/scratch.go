package dbanalyze

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path"
	"testing/fstest"

	"github.com/reliant-labs/forge/pkg/migratekit"
	"github.com/reliant-labs/forge/pkg/migratekit/compat"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

// PrepareScratch returns a scratch database — on pgtest's embedded Postgres,
// or the server FORGE_TEST_POSTGRES_URL names — with setup applied by forge's
// migrator, and a cleanup that drops it.
//
// The migrated state is a pgtest template keyed by setup's content, so a
// second run over the same base clones it instead of migrating again.
func PrepareScratch(ctx context.Context, setup []MigrationFile) (dsn string, cleanup func(), err error) {
	h := sha256.New()
	fsys := fstest.MapFS{}
	for _, f := range setup {
		name := path.Base(f.File)
		fmt.Fprintf(h, "%s\x00%d\x00%s\x00", name, len(f.SQL), f.SQL)
		fsys["migrations/"+name] = &fstest.MapFile{Data: []byte(f.SQL)}
	}
	// A file the migrator would not read as a migration would be skipped
	// silently, and the base would be short of it.
	if len(setup) > 0 {
		ms, err := compat.Scan(fsys, "migrations")
		if err != nil {
			return "", nil, err
		}
		if len(ms) != len(setup) {
			return "", nil, fmt.Errorf("%d of the %d base migrations are not named <version>_<name>.up.sql", len(setup)-len(ms), len(setup))
		}
	}
	key := "dbanalyze_" + hex.EncodeToString(h.Sum(nil))[:16]
	db, dsn, cleanup, err := pgtest.NewFromTemplate(key, func(db *sql.DB, _ string) error {
		if len(setup) == 0 {
			return nil
		}
		if _, err := migratekit.Apply(ctx, db, fsys, "migrations"); err != nil {
			return fmt.Errorf("apply the base migrations: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	// The caller connects by DSN, through its own Session.
	_ = db.Close()
	return dsn, cleanup, nil
}
