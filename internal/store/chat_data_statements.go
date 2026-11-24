// Copyright (c) 2026 Reliant Labs
package store

// ProjectDeleteStatements is every statement DeleteUserProject runs against a
// project's chats: the chat batches and the thinning slices. The guard test
// beside account purge (internal/accountpurge) reads it to fail when a table
// keyed by chat is missing from project delete; nothing else should.
func ProjectDeleteStatements() []string {
	out := []string{projectDeleteWorktreesSQL}
	for _, steps := range [][]struct{ name, sql string }{chatPurgeSteps, messageThinSteps, flatThinSteps} {
		for _, s := range steps {
			out = append(out, s.sql)
		}
	}
	return out
}

// LedgerRetentionStatements is every delete the ledger retention pass
// (internal/db/ledger_retention.go) runs, for the same guard.
func LedgerRetentionStatements() []string {
	return []string{pruneRunStepsSQL, pruneRunObservationsSQL}
}
