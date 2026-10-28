package store

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
)

// LockActivationDependencies serializes existing mutable dependency edits with
// approval verification and the row mutation. Predicates are also read under
// SERIALIZABLE: insertion/shadowing conflicts cannot produce a mixed snapshot.
// Lock ordering is fixed. Only user/project-scoped rows, never table locks.
func LockActivationDependencies(ctx context.Context, db orm.Context, user, project string) error {
	for _, query := range []string{
		`SELECT id FROM projects WHERE id=?1 AND user_id=?0 FOR SHARE`,
		`SELECT id FROM project_configs WHERE project_id=?1 FOR SHARE`,
		`SELECT id FROM workflow_drafts WHERE user_id=?0 ORDER BY id FOR SHARE`,
		`SELECT id FROM presets WHERE user_id=?0 AND (project_id IS NULL OR project_id=?1) ORDER BY id FOR SHARE`,
		`SELECT id FROM connections WHERE user_id=?0 ORDER BY id FOR SHARE`,
		`SELECT id FROM settings WHERE user_id=?0 AND key LIKE 'tool.bindings.%' ORDER BY id FOR SHARE`,
		`SELECT id FROM daemons WHERE user_id=?0 ORDER BY id FOR SHARE`,
		`SELECT project_id FROM project_daemons WHERE project_id=?1 ORDER BY daemon_id FOR SHARE`,
	} {
		var ids []string
		if err := db.Bun().NewRaw(query, user, project).Scan(ctx, &ids); err != nil {
			return err
		}
	}
	return nil
}
