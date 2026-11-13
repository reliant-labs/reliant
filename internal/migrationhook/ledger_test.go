package migrationhook

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"2", "10", -1},
		{"010", "10", 0},
		{"20240101120000", "20240101120000", 0},
		{"1.2.10", "1.2.9", 1},
		{"1.2", "1.2.1", -1},
		{"3_1", "3_0", 1},
		{"99999999999999999999999", "100000000000000000000000", -1},
	} {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestLedgerBehind(t *testing.T) {
	want := Ledger{Applied: []string{"1", "2", "3"}}
	for _, tc := range []struct {
		name string
		have Ledger
		v    string
	}{
		{"everything applied", Ledger{Applied: []string{"1", "2", "3"}}, ""},
		{"one pending", Ledger{Applied: []string{"1", "2"}}, "3"},
		{"a gap", Ledger{Applied: []string{"1", "3"}}, "2"},
		{"nothing applied", Ledger{}, "1"},
		{"a baseline covers the rest", Ledger{Applied: []string{"3"}, Through: "2"}, ""},
		{"a current version covers everything below it", Ledger{Through: "3"}, ""},
		{"a current version below the last", Ledger{Through: "2"}, "3"},
	} {
		if got := want.Behind(tc.have); got != tc.v {
			t.Errorf("%s: Behind = %q, want %q", tc.name, got, tc.v)
		}
	}
	if got := (Ledger{Through: "5"}).Behind(Ledger{Through: "4"}); got != "5" {
		t.Errorf("a current version behind the shadow's: Behind = %q, want 5", got)
	}

	// A repeatable is applied only at the shadow's content, and no
	// baseline or current version covers it.
	r := RepeatablePrefix + "R__views.sql"
	shadow := Ledger{Applied: []string{"1", r}, Sums: map[string]string{r: "101"}}
	for _, tc := range []struct {
		name string
		have Ledger
		v    string
	}{
		{"same content", Ledger{Applied: []string{"1", r}, Sums: map[string]string{r: "101"}}, ""},
		{"other content", Ledger{Applied: []string{"1", r}, Sums: map[string]string{r: "100"}}, r},
		{"never run", Ledger{Applied: []string{"1"}}, r},
		{"a baseline does not cover it", Ledger{Through: "9"}, r},
	} {
		if got := shadow.Behind(tc.have); got != tc.v {
			t.Errorf("repeatable, %s: Behind = %q, want %q", tc.name, got, tc.v)
		}
	}
}

func TestLedgerDiverged(t *testing.T) {
	r := RepeatablePrefix + "R__views.sql"
	shadow := Ledger{Applied: []string{"1", "2", r}, Sums: map[string]string{"1": "a", "2": "b", r: "c"}}
	for _, tc := range []struct {
		name string
		have Ledger
		v    string
	}{
		{"same content", Ledger{Sums: map[string]string{"1": "a", "2": "b"}}, ""},
		{"an edited applied migration", Ledger{Sums: map[string]string{"1": "a", "2": "x"}}, "2"},
		{"no recorded sum", Ledger{}, ""},
		{"a repeatable is Behind's, not Diverged's", Ledger{Sums: map[string]string{r: "x"}}, ""},
	} {
		if got := shadow.Diverged(tc.have); got != tc.v {
			t.Errorf("%s: Diverged = %q, want %q", tc.name, got, tc.v)
		}
	}
}

// ReadLedger reads each built-in tool's history table in the shape the tool
// writes it.
func TestReadLedger(t *testing.T) {
	if testing.Short() {
		t.Skip("reads ledgers in a real postgres; skipped under -short")
	}
	ctx := context.Background()
	for _, tc := range []struct {
		tool  string
		argv  []string
		stmts []string
		want  Ledger
	}{
		{
			tool: config.MigrationToolGoose,
			stmts: []string{
				`CREATE TABLE goose_db_version (id serial PRIMARY KEY, version_id bigint NOT NULL, is_applied boolean NOT NULL, tstamp timestamp)`,
				`INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, true), (1, true), (2, true), (3, true), (3, false)`,
			},
			want: Ledger{Table: LedgerTable{Name: "goose_db_version"}, Applied: []string{"1", "2"}},
		},
		{
			tool: config.MigrationToolGolangMigrate,
			stmts: []string{
				`CREATE TABLE schema_migrations (version bigint PRIMARY KEY, dirty boolean NOT NULL)`,
				`INSERT INTO schema_migrations VALUES (7, true)`,
			},
			want: Ledger{Table: LedgerTable{Name: "schema_migrations"}, Dirty: "7"},
		},
		{
			tool: config.MigrationToolDbmate,
			argv: []string{"dbmate", "--migrations-table", "app.history", "migrate"},
			stmts: []string{
				`CREATE SCHEMA app`,
				`CREATE TABLE app.history (version varchar(128) PRIMARY KEY)`,
				`INSERT INTO app.history VALUES ('20240101'), ('20240102')`,
			},
			want: Ledger{Table: LedgerTable{Schema: "app", Name: "history"}, Applied: []string{"20240101", "20240102"}},
		},
		{
			tool: config.MigrationToolFlyway,
			stmts: []string{
				`CREATE TABLE flyway_schema_history (installed_rank int PRIMARY KEY, version varchar(50), type varchar(20) NOT NULL, script varchar(1000) NOT NULL, checksum int, success boolean NOT NULL)`,
				`INSERT INTO flyway_schema_history VALUES
					(0, NULL, 'SCHEMA', '<< Flyway Schema Creation >>', NULL, true),
					(1, '1', 'BASELINE', '<< Flyway Baseline >>', NULL, true),
					(2, '2', 'SQL', 'V2__a.sql', 22, true),
					(3, NULL, 'SQL', 'R__views.sql', 100, true),
					(4, '3', 'SQL', 'V3__b.sql', 33, false),
					(5, NULL, 'SQL', 'R__views.sql', 101, true),
					(6, NULL, 'SQL', 'R__labels.sql', 200, true),
					(7, NULL, 'SQL', 'R__labels.sql', 201, false),
					(8, '4', 'SQL', 'V4__c.sql', 44, true),
					(9, '4', 'UNDO_SQL', 'U4__c.sql', 45, true)`,
			},
			want: Ledger{
				Table:   LedgerTable{Name: "flyway_schema_history"},
				Applied: []string{"2", RepeatablePrefix + "R__views.sql"},
				Sums:    map[string]string{"2": "22", RepeatablePrefix + "R__views.sql": "101"},
				Through: "1",
				Dirty:   "3",
			},
		},
		{
			// An undone version is not applied, and not failed either.
			tool: config.MigrationToolFlyway,
			stmts: []string{
				`CREATE TABLE flyway_schema_history (installed_rank int PRIMARY KEY, version varchar(50), type varchar(20) NOT NULL, script varchar(1000) NOT NULL, checksum int, success boolean NOT NULL)`,
				`INSERT INTO flyway_schema_history VALUES (1, '1', 'SQL', 'V1__a.sql', 11, true), (2, '2', 'SQL', 'V2__b.sql', 22, true), (3, '2', 'UNDO_SQL', 'U2__b.sql', 23, true)`,
			},
			want: Ledger{Table: LedgerTable{Name: "flyway_schema_history"}, Applied: []string{"1"}, Sums: map[string]string{"1": "11"}},
		},
		{
			tool: config.MigrationToolAtlas,
			stmts: []string{
				`CREATE SCHEMA atlas_schema_revisions`,
				`CREATE TABLE atlas_schema_revisions.atlas_schema_revisions (version varchar PRIMARY KEY, type bigint NOT NULL, applied bigint NOT NULL, total bigint NOT NULL, error text, hash varchar NOT NULL)`,
				`INSERT INTO atlas_schema_revisions.atlas_schema_revisions VALUES ('.atlas_cloud_identifiers', 0, 0, 0, NULL, ''), ('1', 1, 0, 0, NULL, ''), ('2', 2, 3, 3, NULL, 'h2'), ('3', 2, 1, 2, 'boom', 'h3')`,
			},
			want: Ledger{Table: LedgerTable{Schema: "atlas_schema_revisions", Name: "atlas_schema_revisions"}, Applied: []string{"2"}, Sums: map[string]string{"2": "h2"}, Through: "1", Dirty: "3"},
		},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			url, clean, err := pgtest.NewURL()
			if err != nil {
				t.Fatalf("pgtest.NewURL: %v", err)
			}
			t.Cleanup(clean)
			db, err := sql.Open(pgtest.DriverName, url)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			h := Hook{Tool: tc.tool}
			h.Apply, h.Status = Preset(tc.tool)
			if tc.argv != nil {
				h.Apply = tc.argv
			}

			if l, exists, err := h.ReadLedger(ctx, db); err != nil || exists || !l.Empty() {
				t.Fatalf("before the tool ran: %+v, exists %v, err %v; want an empty ledger", l, exists, err)
			}
			for _, s := range tc.stmts {
				if _, err := db.ExecContext(ctx, s); err != nil {
					t.Fatal(err)
				}
			}
			l, exists, err := h.ReadLedger(ctx, db)
			if err != nil || !exists {
				t.Fatalf("ReadLedger: exists %v, err %v", exists, err)
			}
			if !reflect.DeepEqual(l, tc.want) {
				t.Errorf("ReadLedger = %+v, want %+v", l, tc.want)
			}
		})
	}

	if _, _, err := (Hook{Tool: config.MigrationToolCustom}).ReadLedger(ctx, nil); err == nil {
		t.Error("ReadLedger of a custom tool: want an error, forge cannot read its history")
	}
}
