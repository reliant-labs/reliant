// Copyright (c) 2025 Reliant Labs
package db

import (
	"bytes"
	"io/fs"
	"path"
	"regexp"
	"testing"

	"github.com/reliant-labs/forge/pkg/migratekit/compat"

	forgedb "github.com/reliant-labs/reliant/db"
)

// breaksRollbackRe is the line a forge migration carries to say, on purpose,
// that the previous release cannot run on the schema it leaves:
//
//	-- reliant:breaks-rollback <why, and what replaces the rollback>
//
// A reason is required: it is the record of a decision, not a checkbox.
var breaksRollbackRe = regexp.MustCompile(`(?im)^\s*--\s*reliant:breaks-rollback\s+\S`)

// TestForgeMigrationsDeclareCompatibility makes every forge migration after
// the baseline state whether the PREVIOUS release keeps working on the schema
// it leaves — `-- forge:backward-compatible` (the default, and what
// `make migration` writes) or `-- reliant:breaks-rollback <why>`.
//
// Rolling back is redeploying the previous release onto the schema the new one
// left. Until a release after the cutover has shipped, the previous release is
// the last GOOSE release: goose finds its whole history applied and does
// nothing, and its code runs on whatever forge migrations were applied since.
// After that, it is an older forge release, whose migrator refuses to start on
// a schema ahead of it unless every newer version was declared
// backward-compatible (migratekit.SchemaAheadError). Either way, an
// undeclared migration is a rollback nobody decided to give up. Expand in one
// release, contract in a later one, and say which half this is.
func TestForgeMigrationsDeclareCompatibility(t *testing.T) {
	ms, err := compat.Scan(forgedb.Source(), forgedb.SourceDir)
	if err != nil {
		t.Fatalf("scan db/migrations: %v", err)
	}
	for i, m := range ms {
		if i == 0 {
			continue // the baseline builds a database; nothing older runs on it
		}
		raw, err := fs.ReadFile(forgedb.Source(), path.Join(forgedb.SourceDir, m.Name))
		if err != nil {
			t.Fatalf("read %s: %v", m.Name, err)
		}
		breaks := breaksRollbackRe.Match(raw)
		switch {
		case bytes.Contains(raw, []byte("<why")):
			t.Errorf("%s still carries `make migration`'s <why ...> placeholder: write why the previous release keeps working on "+
				"this schema (or why, deliberately, it does not)", m.Name)
		case m.BackwardCompatible && breaks:
			t.Errorf("%s declares both %q and `-- reliant:breaks-rollback`: it is one or the other", m.Name, compat.Directive)
		case !m.BackwardCompatible && !breaks:
			t.Errorf("%s does not say whether the previous release (until one ships after the cutover, the last goose release) keeps working on "+
				"the schema it leaves. Add `%s — <why>` if it does (an added column, table or index old code ignores), or "+
				"`-- reliant:breaks-rollback <why>` if it deliberately does not (a dropped or renamed column old code reads, a new NOT NULL "+
				"column without a default old code does not write) — and then the rollback runbook changes with it "+
				"(research/forge-migration/04-p5a-cutover-runbook.md)", m.Name, compat.Directive)
		}
	}
}
