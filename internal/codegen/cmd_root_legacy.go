package codegen

import (
	"path/filepath"

	"github.com/reliant-labs/forge/internal/checksums"
)

// cmd/<bin>/cmd/root_gen.go is RETIRED. It held ServiceName, the migration
// source and generatedCommands; a project gets those from its own
// scaffold-once root.go (ServiceName as a const, `db migrate` self-registered
// from db.go) and from db.Source().
//
// forge no longer emits a compatibility copy for a root.go that still calls
// generatedCommands(): such a project ports root.go, serve.go and db.go to the
// current shape (the cmd-tree templates) before it upgrades.

// legacyRootGenRelPath is the retired file's project-relative path.
func legacyRootGenRelPath(bin string) string {
	return filepath.Join("cmd", bin, "cmd", "root_gen.go")
}

// SyncLegacyRootGen reclaims a leftover root_gen.go. The current root.go
// declares ServiceName itself, so a stale copy would be a redeclaration in the
// same package. Only forge's own pristine render is removed (RetireGenerated);
// a hand-edited or disowned one is the user's.
func SyncLegacyRootGen(projectDir, _, bin string, cs *checksums.FileChecksums) error {
	RetireGenerated(projectDir, legacyRootGenRelPath(bin), cs)
	return nil
}
