package shadowdb

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/migrationhook"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// migrationSet is the set the shadow applies for migDir: forge's replay of
// its *.up.sql files, or — when the project's forge.yaml names another tool
// for that directory (`database.migrations.tool`) — that tool, run against
// the shadow database's DSN through the migration hook.
//
// Only the project's OWN migrations directory goes through the hook. A reader
// handed some other directory (a fixture, a sibling checkout's) gets the
// replay it always did.
func migrationSet(projectDir, migDir string) (*schemadef.MigrationSet, error) {
	h, ok := migrationhook.ResolveForDir(projectDir, migDir)
	if !ok || !sameDir(h.Dir, migDir) {
		return schemadef.ReadMigrationSet(migDir)
	}
	digest, n, err := migrationhook.Digest(h)
	if err != nil {
		return nil, fmt.Errorf("read %s for the shadow schema: %w", h.RelDir, err)
	}
	return schemadef.NewCommandMigrationSet(digest, n, func(dsn string) error {
		// The tool's output is kept, not streamed: a generate prints one
		// line per step, and the shadow apply runs under several of them.
		// It is shown when the apply fails, which is when it matters.
		var out bytes.Buffer
		err := migrationhook.Apply(context.Background(), h, migrationhook.Invocation{
			DSN: dsn, Phase: migrationhook.PhaseShadow, Stdout: &out, Stderr: &out,
		})
		if err != nil {
			if tail := lastLines(out.String(), 20); tail != "" {
				return fmt.Errorf("%w\n%s", err, tail)
			}
			return err
		}
		return nil
	}), nil
}

// sameDir reports whether a and b name one directory.
func sameDir(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return aa == bb
}

// lastLines is the last n non-empty lines of s, indented under the error.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	var b strings.Builder
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		b.WriteString("    " + l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
