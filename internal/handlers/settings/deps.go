// forge:deps — consumer-declared interfaces for this package's Deps.
// Repository is this package's own declaration of what the settings handlers
// read and write; mock_gen.go mocks it. *db.Repo satisfies it, and the
// composition root hands it to New.
//
// It lists the methods the handlers call directly, then embeds the role of
// the callee they hand the repository to (drivers.HasStaleReliantKey), so a
// callee that needs more points the compiler here.
package settings

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
)

// Repository is everything the settings handlers read and write.
type Repository interface {
	// Setting rows, user- and project-scoped.
	GetSetting(ctx context.Context, userID string, projectID *string, key string) (*db.Setting, error)
	ListSettings(ctx context.Context, userID string, projectID *string) ([]*db.Setting, error)
	CreateSetting(ctx context.Context, setting *db.Setting) error
	UpdateSetting(ctx context.Context, setting *db.Setting) error
	DeleteSetting(ctx context.Context, id string) error

	// Privacy choices and hidden items.
	GetPrivacySettings(ctx context.Context, userID string) (analyticsEnabled, crashReportingEnabled bool, err error)
	SetPrivacySettings(ctx context.Context, userID string, analyticsEnabled, crashReportingEnabled bool) error
	ListVisibilityOverrides(ctx context.Context, userID string, itemType int32) (map[string]bool, error)
	SetVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string, isVisible bool) error
	ListHiddenItemDefaults(ctx context.Context, itemType int32) ([]string, error)

	// Provider accounts: sign-in, re-label, limits and the usage poll a new
	// account schedules.
	ListProviderAccounts(ctx context.Context, userID string) ([]core.ProviderAccount, error)
	UpsertProviderAccount(ctx context.Context, u core.AccountUpsert) (acct core.ProviderAccount, created bool, err error)
	PatchProviderAccount(ctx context.Context, userID, accountID string, p core.AccountPatch) (core.ProviderAccount, error)
	ListAccountLimits(ctx context.Context, userID string) (map[string]core.LimitState, error)
	ScheduleUsagePoll(ctx context.Context, accountID string, next time.Time, failures int, errText string) error

	// The refetch every settings write pushes to the user's other clients.
	EmitUserRefetch(ctx context.Context, userID string, refetchType db.RefetchType, opts db.RefetchOpts) error

	drivers.AccountLister
}
