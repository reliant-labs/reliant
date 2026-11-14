package sqllex

import (
	"slices"
	"strings"
)

// NonTransactional returns how postgres names a statement it refuses inside a
// transaction block — the callers of PreventInTransactionBlock — or "" for
// any other statement. Only statements a migration could plausibly hold are
// listed; COMMIT PREPARED and the like are not.
//
// Each case matches the statement's leading keywords, not text anywhere in
// it, so a comment or a string that mentions CONCURRENTLY never matches.
func NonTransactional(w []string) string {
	at := func(i int, word string) bool { return len(w) > i && w[i] == word }
	has := func(word string) bool { return slices.Contains(w, word) }
	switch {
	case at(0, "create") && (at(1, "index") && at(2, "concurrently") || at(1, "unique") && at(2, "index") && at(3, "concurrently")):
		return "CREATE INDEX CONCURRENTLY"
	case at(0, "drop") && at(1, "index") && at(2, "concurrently"):
		return "DROP INDEX CONCURRENTLY"
	case at(0, "reindex") && has("concurrently") && !optionOff(w, "concurrently"):
		return "REINDEX … CONCURRENTLY"
	case at(0, "reindex"):
		switch kind := reindexKind(w); kind {
		case "schema", "database", "system":
			return "REINDEX " + strings.ToUpper(kind)
		}
	case at(0, "alter") && at(1, "table") && has("detach") && has("concurrently"):
		return "ALTER TABLE … DETACH PARTITION … CONCURRENTLY"
	case at(0, "vacuum"):
		return "VACUUM"
	case at(0, "cluster") && !slices.ContainsFunc(w[1:], func(s string) bool { return s != "verbose" }):
		// CLUSTER with no table reclusters every table, one transaction each.
		return "CLUSTER without a table"
	case at(0, "create") && at(1, "database"):
		return "CREATE DATABASE"
	case at(0, "drop") && at(1, "database"):
		return "DROP DATABASE"
	case at(0, "alter") && at(1, "database") && hasSeq(w, "set", "tablespace"):
		return "ALTER DATABASE … SET TABLESPACE"
	case at(0, "create") && at(1, "tablespace"):
		return "CREATE TABLESPACE"
	case at(0, "drop") && at(1, "tablespace"):
		return "DROP TABLESPACE"
	case at(0, "alter") && at(1, "system"):
		return "ALTER SYSTEM"
	case at(0, "create") && at(1, "subscription") && !optionOff(w, "connect") && !optionOff(w, "create_slot"):
		return "CREATE SUBSCRIPTION (it creates a replication slot)"
	case at(0, "drop") && at(1, "subscription"):
		return "DROP SUBSCRIPTION (of a subscription with a replication slot)"
	case at(0, "alter") && at(1, "subscription") && !optionOff(w, "refresh") &&
		(has("refresh") || has("publication") && (has("set") || has("add") || has("drop"))):
		return "ALTER SUBSCRIPTION … with refresh"
	case at(0, "discard") && at(1, "all"):
		return "DISCARD ALL"
	}
	return ""
}

// reindexKind returns the object kind a REINDEX names — index, table, schema,
// database or system — past any parenthesised options.
func reindexKind(w []string) string {
	for _, word := range w[1:] {
		switch word {
		case "index", "table", "schema", "database", "system":
			return word
		}
	}
	return ""
}

// optionOff reports whether a statement turns option off: `option = false`,
// lexed as the two words.
func optionOff(w []string, option string) bool {
	i := slices.Index(w, option)
	return i >= 0 && i+1 < len(w) && (w[i+1] == "false" || w[i+1] == "off")
}

// hasSeq reports whether w holds a then b, adjacent.
func hasSeq(w []string, a, b string) bool {
	for i := 0; i+1 < len(w); i++ {
		if w[i] == a && w[i+1] == b {
			return true
		}
	}
	return false
}
