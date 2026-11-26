package orm

import (
	"errors"
	"strings"
	"testing"
)

// otherDialect is a postgres dialect under another name, to reach the paths
// that refuse a dialect forge does not implement.
type otherDialect struct{ PostgresDialect }

func (*otherDialect) Name() string { return "other" }

func TestValidateIdentifier(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		wantErr string
	}{
		{"plain", "users", ""},
		{"underscore start", "_tmp_1", ""},
		{"mixed case digits", "Table2", ""},
		{"empty", "", "identifier cannot be empty"},
		{"leading digit", "1abc", `invalid identifier "1abc"`},
		{"space", "a b", `invalid identifier "a b"`},
		{"injection", `users"; DROP TABLE x; --`, "invalid identifier"},
		{"dash", "a-b", "invalid identifier"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIdentifier(tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateIdentifier(%q) = %v", tc.in, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateIdentifier(%q) = %v, want %q", tc.in, err, tc.wantErr)
			}
		})
	}
}

func TestRegisterDialect_RefusesBadRegistrations(t *testing.T) {
	for _, tc := range []struct {
		name  string
		d     Dialect
		panic string
	}{
		{"nil", nil, "dialect is nil"},
		{"unnamed", &namedDialect{}, "name is empty"},
		{"duplicate", &PostgresDialect{}, "called twice for dialect postgres"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				msg, _ := r.(string)
				if !strings.Contains(msg, tc.panic) {
					t.Fatalf("panic = %v, want one containing %q", r, tc.panic)
				}
			}()
			RegisterDialect(tc.d)
		})
	}
}

type namedDialect struct{ PostgresDialect }

func (*namedDialect) Name() string { return "" }

func TestGetDialect_AndListDialects(t *testing.T) {
	d, err := GetDialect("postgres")
	if err != nil || d.Name() != "postgres" {
		t.Fatalf("GetDialect(postgres) = %v, %v", d, err)
	}
	if _, err := GetDialect("mysql"); !errors.Is(err, ErrInvalidDialect) || !strings.Contains(err.Error(), "mysql") {
		t.Fatalf("GetDialect(mysql) err = %v, want ErrInvalidDialect naming it", err)
	}
	found := false
	for _, n := range ListDialects() {
		found = found || n == "postgres"
	}
	if !found {
		t.Fatalf("ListDialects() = %v, missing postgres", ListDialects())
	}
}

func TestPostgresDialect_Basics(t *testing.T) {
	d := &PostgresDialect{}
	if d.DriverName() != "postgres" || !d.SupportsReturning() {
		t.Errorf("DriverName=%q SupportsReturning=%v", d.DriverName(), d.SupportsReturning())
	}
	if got := d.Placeholder(0); got != "$1" {
		t.Errorf("Placeholder(0) = %q, want $1", got)
	}
	if got := d.Placeholder(4); got != "$5" {
		t.Errorf("Placeholder(4) = %q, want $5", got)
	}
	if got := d.QuoteIdentifier(`we"ird`); got != `"we""ird"` {
		t.Errorf("QuoteIdentifier = %q, want the quote doubled", got)
	}
	if got := d.MapFieldType(TypeTimestampTZ); got != "TIMESTAMPTZ" {
		t.Errorf("MapFieldType = %q", got)
	}
}

func TestPostgresDialect_OnConflictClause(t *testing.T) {
	d := &PostgresDialect{}
	for _, tc := range []struct {
		name string
		cols []string
		want string
	}{
		{"updates every given column from EXCLUDED", []string{"a", "b"},
			"ON CONFLICT (id) DO UPDATE SET a = EXCLUDED.a, b = EXCLUDED.b"},
		{"one column", []string{"a"}, "ON CONFLICT (id) DO UPDATE SET a = EXCLUDED.a"},
		{"nothing to update does nothing", nil, "ON CONFLICT (id) DO NOTHING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.OnConflictClause("id", tc.cols); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPostgresDialect_IntrospectionQueriesCarryTheTable(t *testing.T) {
	d := &PostgresDialect{}
	if q := d.TableExistsQuery("users"); !strings.Contains(q, "table_name = 'users'") || !strings.Contains(q, "information_schema.tables") {
		t.Errorf("TableExistsQuery = %s", q)
	}
	if q := d.ListTablesQuery(); !strings.Contains(q, "table_schema = 'public'") || !strings.Contains(q, "BASE TABLE") {
		t.Errorf("ListTablesQuery = %s", q)
	}
	q := d.IntrospectColumnsQuery("users")
	if n := strings.Count(q, "'users'"); n != 3 {
		t.Errorf("IntrospectColumnsQuery names the table %d times, want 3 (is_primary_key, is_unique, columns):\n%s", n, q)
	}
	if q := d.IntrospectIndexesQuery("users"); !strings.Contains(q, "i.tablename = 'users'") || !strings.Contains(q, "NOT idx.indisprimary") {
		t.Errorf("IntrospectIndexesQuery = %s", q)
	}
}

// The table name is interpolated into SQL, so every query builder refuses
// anything that is not a plain identifier by panicking before it can reach the
// database.
func TestPostgresDialect_QueriesRefuseUnsafeTableNames(t *testing.T) {
	d := &PostgresDialect{}
	for name, build := range map[string]func(string) string{
		"TableExistsQuery":       d.TableExistsQuery,
		"IntrospectColumnsQuery": d.IntrospectColumnsQuery,
		"IntrospectIndexesQuery": d.IntrospectIndexesQuery,
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("no panic for a table name carrying SQL")
				}
			}()
			build("users'; DROP TABLE users; --")
		})
	}
}

func TestPostgresDialect_ParseColumnType(t *testing.T) {
	d := &PostgresDialect{}
	for in, want := range map[string]FieldType{
		"text": TypeText, "character varying": TypeVarchar, "varchar": TypeVarchar,
		"integer": TypeInteger, "int4": TypeInteger, "bigint": TypeBigInt, "int8": TypeBigInt,
		"boolean": TypeBoolean, "bool": TypeBoolean,
		"timestamp with time zone": TypeTimestampTZ, "timestamptz": TypeTimestampTZ,
		"jsonb": TypeJSONB, "bytea": TypeBytea, "serial": TypeSerial, "bigserial": TypeBigSerial,
	} {
		got, err := d.ParseColumnType(in)
		if err != nil || got != want {
			t.Errorf("ParseColumnType(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := d.ParseColumnType(""); err == nil || !strings.Contains(err.Error(), "cannot be empty") {
		t.Errorf("empty type: err = %v", err)
	}
	if _, err := d.ParseColumnType("hstore"); err == nil || !strings.Contains(err.Error(), "unknown PostgreSQL column type: hstore") {
		t.Errorf("unknown type: err = %v", err)
	}
}
