package orm

import "testing"

// TestContextDialect pins the raw-SQL escape-hatch seam: orm.Context exposes
// Dialect() on both *Client and *Tx, so a hand-written handler builds postgres
// SQL via db.Dialect().Placeholder(i) / .QuoteIdentifier(name) instead of
// hardcoding $N / "ident" or reinventing a package-level dialect (the gap
// kalshi hit — fr-3c3f470f2c).
func TestContextDialect(t *testing.T) {
	pg := &PostgresDialect{}

	// *Tx carries the dialect from its parent Client (BeginTx threads it).
	var tx Context = &Tx{dialect: pg}
	if tx.Dialect() != pg {
		t.Fatal("Tx.Dialect() must return the dialect threaded at BeginTx")
	}

	// The seam works through the interface, inside or outside a transaction.
	if got := tx.Dialect().Placeholder(0); got != "$1" {
		t.Errorf("Placeholder(0) = %q, want $1", got)
	}
	if got := tx.Dialect().Placeholder(2); got != "$3" {
		t.Errorf("Placeholder(2) = %q, want $3", got)
	}
	if got := tx.Dialect().QuoteIdentifier(`weird"col`); got != `"weird""col"` {
		t.Errorf("QuoteIdentifier escaping = %q", got)
	}
}
