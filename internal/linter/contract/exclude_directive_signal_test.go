package contract

import (
	"go/token"
	"go/types"
	"testing"
)

// pgx's I/O is its connection, pool, transaction and result types and the
// functions that connect or query; its error checks and config parsing are
// pure. Red before: every function under github.com/jackc/pgx was a database
// call, so reliant's dbconn (pgconn.SafeToRetry, an errors.As) was refused.
func TestIOSignalFor_PgxNamesOnlyItsIO(t *testing.T) {
	pgx := types.NewPackage("github.com/jackc/pgx/v5", "pgx")
	pgconn := types.NewPackage("github.com/jackc/pgx/v5/pgconn", "pgconn")
	pgxpool := types.NewPackage("github.com/jackc/pgx/v5/pgxpool", "pgxpool")
	pgtype := types.NewPackage("github.com/jackc/pgx/v5/pgtype", "pgtype")

	fn := func(pkg *types.Package, name string) *types.Func {
		return types.NewFunc(token.NoPos, pkg, name, types.NewSignatureType(nil, nil, nil, nil, nil, false))
	}
	method := func(pkg *types.Package, recv, name string) *types.Func {
		named := types.NewNamed(types.NewTypeName(token.NoPos, pkg, recv, nil), types.NewStruct(nil, nil), nil)
		r := types.NewVar(token.NoPos, pkg, "r", types.NewPointer(named))
		return types.NewFunc(token.NoPos, pkg, name, types.NewSignatureType(r, nil, nil, nil, nil, false))
	}

	for name, f := range map[string]*types.Func{
		"pgx.Connect":           fn(pgx, "Connect"),
		"pgx.CollectRows":       fn(pgx, "CollectRows"),
		"(*pgx.Conn).Query":     method(pgx, "Conn", "Query"),
		"pgxpool.New":           fn(pgxpool, "New"),
		"(*pgxpool.Pool).Exec":  method(pgxpool, "Pool", "Exec"),
		"(*pgconn.PgConn).Exec": method(pgconn, "PgConn", "Exec"),
	} {
		if label, ok := ioSignalFor(f); !ok || label != "a database call (pgx)" {
			t.Errorf("%s: want a database call (pgx), got %q, %v", name, label, ok)
		}
	}
	for name, f := range map[string]*types.Func{
		"pgconn.SafeToRetry":         fn(pgconn, "SafeToRetry"),
		"pgconn.Timeout":             fn(pgconn, "Timeout"),
		"pgx.ParseConfig":            fn(pgx, "ParseConfig"),
		"pgxpool.ParseConfig":        fn(pgxpool, "ParseConfig"),
		"(*pgconn.PgError).Error":    method(pgconn, "PgError", "Error"),
		"(pgx.Identifier).Sanitize":  method(pgx, "Identifier", "Sanitize"),
		"(*pgtype.Map).RegisterType": method(pgtype, "Map", "RegisterType"),
	} {
		if label, ok := ioSignalFor(f); ok {
			t.Errorf("%s: a pure pgx call was classed as I/O (%q)", name, label)
		}
	}
}
