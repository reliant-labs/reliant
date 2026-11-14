package dbanalyze

// Rule IDs. Each is graded like a `forge lint` rule: `lint.rules` sets its
// severity, and it warns first through forge's lint rollout table.
const (
	// RuleTableRewrite: a pre-existing table or its indexes were rewritten.
	RuleTableRewrite = "db-analyze-table-rewrite"
	// RuleAccessExclusiveLock: a pre-existing table held ACCESS EXCLUSIVE
	// (blocks reads and writes) across a statement that also scanned or
	// rewrote it.
	RuleAccessExclusiveLock = "db-analyze-access-exclusive-lock"
	// RuleValidationScan: a full scan of a pre-existing table under a lock
	// that blocks writes.
	RuleValidationScan = "db-analyze-validation-scan"
	// RuleBlockingIndexBuild: an index built on a pre-existing table without
	// CONCURRENTLY.
	RuleBlockingIndexBuild = "db-analyze-blocking-index-build"
	// RuleStatementTimeout: a statement exceeded statement_timeout.
	RuleStatementTimeout = "db-analyze-statement-timeout"
)

// RuleIDs is every rule the migration pass reports.
func RuleIDs() []string {
	return []string{RuleTableRewrite, RuleAccessExclusiveLock, RuleValidationScan, RuleBlockingIndexBuild, RuleStatementTimeout}
}

// Fix texts, one per rule (validation scans have one per kind of check).
const (
	fixTableRewrite = "avoid the rewrite: add a column with no default or a constant one, change a type only " +
		"to a binary-compatible one (varchar(n) to a wider varchar or text), or add the new column beside the old, " +
		"backfill it in batches and switch over in a later migration (expand/contract)"
	fixAccessExclusive = "keep the long part out from under ACCESS EXCLUSIVE: add a constraint NOT VALID and VALIDATE " +
		"it in a later migration (validation takes SHARE UPDATE EXCLUSIVE), and avoid table rewrites (see " +
		RuleTableRewrite + ")"
	fixValidateCheck = "add the constraint NOT VALID (no scan, brief lock), then ALTER TABLE … VALIDATE CONSTRAINT " +
		"in a later migration: validation takes SHARE UPDATE EXCLUSIVE, which does not block reads or writes. " +
		"The two must not share a migration file, which runs as one transaction and would hold the first lock " +
		"through the scan"
	fixValidateFK = "add the foreign key NOT VALID, then ALTER TABLE … VALIDATE CONSTRAINT in a later migration: " +
		"validation does not block writes to either table"
	fixValidateNotNull = "add CHECK (<column> IS NOT NULL) NOT VALID, VALIDATE CONSTRAINT it in a later migration, " +
		"then SET NOT NULL in a third: postgres skips the scan when a valid CHECK already proves it, and the CHECK " +
		"can then be dropped"
	fixValidateGeneric = "move the scan out from under the write-blocking lock: a migration file runs as one " +
		"transaction, so a lock an earlier statement of the file took is held through this one; split them"
	fixIndexBuild = "build the index with CREATE INDEX CONCURRENTLY, as the only statement of its migration file " +
		"(it cannot run inside a transaction); for a UNIQUE or PRIMARY KEY constraint, build the unique index " +
		"concurrently first and then ADD CONSTRAINT … USING INDEX"
	fixVacuumFull = "VACUUM FULL rewrites the table under ACCESS EXCLUSIVE, blocking every read and write of it " +
		"until it finishes: a migration should run plain VACUUM, which makes space reusable without a rewrite; " +
		"reclaim disk with an online tool (pg_repack) outside migrations"
	fixCluster = "CLUSTER rewrites the table under ACCESS EXCLUSIVE, blocking every read and write of it until it " +
		"finishes: leave it out of migrations, and reorder online (pg_repack --order-by) if the order matters"
	fixReindex = "use REINDEX … CONCURRENTLY, alone in its migration file: a plain REINDEX holds SHARE on the " +
		"table, blocking its writes, and ACCESS EXCLUSIVE on each index, blocking the reads that use it, for the " +
		"whole rebuild"
	fixStatementTimeout = "the statement did not finish within statement_timeout on this target: it would hold " +
		"its locks at least that long in production. Batch the work, or split it so the long part runs " +
		"without a blocking lock"
)
