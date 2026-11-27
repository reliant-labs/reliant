package sqllex

import "testing"

// NonTransactional reads the statement's leading keywords the way postgres's
// PreventInTransactionBlock callers do. Each row is one statement a migration
// could hold and the name postgres would give the refusal ("" = allowed in a
// transaction block).
func TestNonTransactionalClassifiesStatements(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want string
	}{
		{"create index concurrently", "CREATE INDEX CONCURRENTLY i ON t (a)", "CREATE INDEX CONCURRENTLY"},
		{"create unique index concurrently", "CREATE UNIQUE INDEX CONCURRENTLY i ON t (a)", "CREATE INDEX CONCURRENTLY"},
		{"plain create index is transactional", "CREATE INDEX i ON t (a)", ""},
		{"drop index concurrently", "DROP INDEX CONCURRENTLY i", "DROP INDEX CONCURRENTLY"},
		{"plain drop index is transactional", "DROP INDEX i", ""},
		{"reindex index concurrently", "REINDEX INDEX CONCURRENTLY i", "REINDEX … CONCURRENTLY"},
		{"reindex option list concurrently", "REINDEX (CONCURRENTLY) TABLE t", "REINDEX … CONCURRENTLY"},
		{"reindex concurrently turned off falls through to its table kind", "REINDEX (CONCURRENTLY false) TABLE t", ""},
		{"reindex concurrently off on a schema is still refused as a schema", "REINDEX (CONCURRENTLY off) SCHEMA s", "REINDEX SCHEMA"},
		{"reindex table is transactional", "REINDEX TABLE t", ""},
		{"reindex index is transactional", "REINDEX INDEX i", ""},
		{"reindex schema", "REINDEX SCHEMA s", "REINDEX SCHEMA"},
		{"reindex database", "REINDEX DATABASE d", "REINDEX DATABASE"},
		{"reindex system", "REINDEX SYSTEM d", "REINDEX SYSTEM"},
		{"reindex with no object kind", "REINDEX", ""},
		{"detach partition concurrently", "ALTER TABLE t DETACH PARTITION p CONCURRENTLY", "ALTER TABLE … DETACH PARTITION … CONCURRENTLY"},
		{"detach partition without concurrently", "ALTER TABLE t DETACH PARTITION p", ""},
		{"vacuum", "VACUUM t", "VACUUM"},
		{"vacuum with options", "VACUUM (ANALYZE) t", "VACUUM"},
		{"cluster with no table", "CLUSTER", "CLUSTER without a table"},
		{"cluster verbose with no table", "CLUSTER VERBOSE", "CLUSTER without a table"},
		{"cluster of a table is transactional", "CLUSTER t USING i", ""},
		{"cluster verbose of a table is transactional", "CLUSTER VERBOSE t", ""},
		{"create database", "CREATE DATABASE d", "CREATE DATABASE"},
		{"drop database", "DROP DATABASE d", "DROP DATABASE"},
		{"alter database set tablespace", "ALTER DATABASE d SET TABLESPACE ts", "ALTER DATABASE … SET TABLESPACE"},
		{"alter database set a parameter is transactional", "ALTER DATABASE d SET work_mem = 1", ""},
		{"create tablespace", "CREATE TABLESPACE ts LOCATION '/x'", "CREATE TABLESPACE"},
		{"drop tablespace", "DROP TABLESPACE ts", "DROP TABLESPACE"},
		{"alter system", "ALTER SYSTEM SET work_mem = 1", "ALTER SYSTEM"},
		{"create subscription", "CREATE SUBSCRIPTION s CONNECTION 'c' PUBLICATION p", "CREATE SUBSCRIPTION (it creates a replication slot)"},
		{"create subscription connect=false makes no slot", "CREATE SUBSCRIPTION s CONNECTION 'c' PUBLICATION p WITH (connect = false)", ""},
		{"create subscription create_slot=off makes no slot", "CREATE SUBSCRIPTION s CONNECTION 'c' PUBLICATION p WITH (create_slot = off)", ""},
		{"drop subscription", "DROP SUBSCRIPTION s", "DROP SUBSCRIPTION (of a subscription with a replication slot)"},
		{"alter subscription refresh", "ALTER SUBSCRIPTION s REFRESH PUBLICATION", "ALTER SUBSCRIPTION … with refresh"},
		{"alter subscription set publication", "ALTER SUBSCRIPTION s SET PUBLICATION p", "ALTER SUBSCRIPTION … with refresh"},
		{"alter subscription add publication", "ALTER SUBSCRIPTION s ADD PUBLICATION p", "ALTER SUBSCRIPTION … with refresh"},
		{"alter subscription drop publication", "ALTER SUBSCRIPTION s DROP PUBLICATION p", "ALTER SUBSCRIPTION … with refresh"},
		{"alter subscription refresh=false", "ALTER SUBSCRIPTION s SET PUBLICATION p WITH (refresh = false)", ""},
		{"alter subscription rename is transactional", "ALTER SUBSCRIPTION s RENAME TO t", ""},
		{"discard all", "DISCARD ALL", "DISCARD ALL"},
		{"discard plans is transactional", "DISCARD PLANS", ""},
		{"ordinary ddl", "CREATE TABLE t (id int)", ""},
		{"empty statement", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var words []string
			if stmts := Split(tc.sql); len(stmts) > 0 {
				words = stmts[0].Words
			}
			if got := NonTransactional(words); got != tc.want {
				t.Errorf("NonTransactional(%q) = %q, want %q", tc.sql, got, tc.want)
			}
		})
	}
}

// A comment or string that merely mentions CONCURRENTLY never makes a
// statement non-transactional: only its leading keywords count.
func TestNonTransactionalIgnoresMentionsInCommentsAndStrings(t *testing.T) {
	for _, sql := range []string{
		"-- CREATE INDEX CONCURRENTLY i ON t (a)\nSELECT 1",
		"SELECT 'VACUUM'",
		"/* DROP DATABASE d */ SELECT 1",
	} {
		if got := NonTransactional(Split(sql)[0].Words); got != "" {
			t.Errorf("NonTransactional(%q) = %q, want it allowed in a transaction", sql, got)
		}
	}
}

func TestOptionOff(t *testing.T) {
	cases := []struct {
		name   string
		words  []string
		option string
		want   bool
	}{
		{"false", []string{"with", "connect", "false"}, "connect", true},
		{"off", []string{"with", "connect", "off"}, "connect", true},
		{"true", []string{"with", "connect", "true"}, "connect", false},
		{"absent option", []string{"with", "other", "false"}, "connect", false},
		{"option is the last word", []string{"with", "connect"}, "connect", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := optionOff(tc.words, tc.option); got != tc.want {
				t.Errorf("optionOff(%v, %q) = %v, want %v", tc.words, tc.option, got, tc.want)
			}
		})
	}
}

func TestHasSeq(t *testing.T) {
	w := []string{"alter", "database", "d", "set", "tablespace", "ts"}
	if !hasSeq(w, "set", "tablespace") {
		t.Error("hasSeq missed an adjacent pair")
	}
	if hasSeq(w, "alter", "set") {
		t.Error("hasSeq matched a non-adjacent pair")
	}
	if hasSeq(w, "tablespace", "ts2") || hasSeq([]string{"set"}, "set", "tablespace") {
		t.Error("hasSeq matched a pair that is not there")
	}
}
