// forge:deps — consumer-declared interfaces for this package's Deps.
// Repository is this package's own declaration of what the preset handlers
// read and write; mock_gen.go mocks it. *db.Repo satisfies it, and the
// composition root hands it to New.
//
// It lists the methods the handlers call directly, then embeds the role of
// every callee they hand the repository to (workflowsource, workflowcatalog),
// so a callee that needs more points the compiler here.
package preset

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/workflowcatalog"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// Repository is everything the preset handlers read and write.
type Repository interface {
	// Preset rows: a user's own, a project's, and the builtins they shadow.
	CreatePreset(ctx context.Context, preset *db.Preset) (*db.Preset, error)
	UpdatePreset(ctx context.Context, preset *db.Preset) (*db.Preset, error)
	DeletePreset(ctx context.Context, id string) error
	GetPresetBySlug(ctx context.Context, userID, slug string) (*db.Preset, error)
	GetPresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) (*db.Preset, error)
	ListUserPresetsByProject(ctx context.Context, userID, projectID string) ([]*db.Preset, error)
	ListPresetsByTag(ctx context.Context, userID, tag, projectID string) ([]*db.Preset, error)

	// The project whose committed presets apply, checked against the caller.
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	GetProjectPresetsJSON(ctx context.Context, projectID string) (*string, error)

	// The settings that hold a user's default preset per scope.
	GetSetting(ctx context.Context, userID string, projectID *string, key string) (*db.Setting, error)
	CreateSetting(ctx context.Context, setting *db.Setting) error
	UpdateSetting(ctx context.Context, setting *db.Setting) error
	ListSettingsByKey(ctx context.Context, userID string, keyPattern string) ([]*db.Setting, error)

	// Which presets a user has hidden, over the shipped defaults.
	ListVisibilityOverrides(ctx context.Context, userID string, itemType int32) (map[string]bool, error)
	ListHiddenItemDefaults(ctx context.Context, itemType int32) ([]string, error)

	// The workflows a preset applies to.
	workflowsource.Store
	workflowcatalog.Reads
}
