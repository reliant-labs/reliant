package core

import (
	"context"
	"time"
)

// Setting represents a key-value setting.
type Setting struct {
	ID        string
	UserID    string
	ProjectID *string
	Key       string
	Value     string
	ValueType string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// VisibilityOverride represents a user's visibility preference for a workflow or preset.
type VisibilityOverride struct {
	ID        string
	UserID    string
	ItemType  int32 // HiddenItemType proto enum value
	Slug      string
	IsVisible bool
	CreatedAt time.Time
}

// ItemDefault represents system default visibility for a builtin workflow or preset.
type ItemDefault struct {
	ID        string
	ItemType  int32 // HiddenItemType proto enum value
	Slug      string
	IsHidden  bool
	Reason    *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ProviderOAuthMarker is the value the legacy api_keys rows of OAuth providers held to record that
// the provider was connected. It is never a credential; the one-time legacy copy skips it.
const ProviderOAuthMarker = "oauth"

// AutomationProviderPrefix namespaces the api_keys rows that hold a user's
// delegated automation credentials ("reliant-automation:<daemonID>"). api_keys holds nothing
// else any more: LLM credentials live in provider_accounts.
const AutomationProviderPrefix = "reliant-automation:"

// SettingStore is the shared contract for settings persistence across drivers.
type SettingStore interface {
	CreateSetting(ctx context.Context, setting *Setting) error
	GetSetting(ctx context.Context, userID string, projectID *string, key string) (*Setting, error)
	ListSettings(ctx context.Context, userID string, projectID *string) ([]*Setting, error)
	ListSettingsByKey(ctx context.Context, userID string, keyPattern string) ([]*Setting, error)
	UpdateSetting(ctx context.Context, setting *Setting) error
	DeleteSetting(ctx context.Context, id string) error

	// Automation tokens: the daemon:resume credential minted for one (user, daemon), kept in api_keys.
	GetAutomationToken(ctx context.Context, userID, daemonID string) (string, error)
	SetAutomationToken(ctx context.Context, userID, daemonID, token string) error
	DeleteAutomationToken(ctx context.Context, userID, daemonID string) error

	GetVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string) (*bool, error)
	ListVisibilityOverrides(ctx context.Context, userID string, itemType int32) (map[string]bool, error)
	SetVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string, isVisible bool) error
	DeleteVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string) error
	GetItemDefault(ctx context.Context, itemType int32, slug string) (*ItemDefault, error)
	ListHiddenItemDefaults(ctx context.Context, itemType int32) ([]string, error)
	GetDefaultPresetAssignments(ctx context.Context, workflowName string) (map[string]string, error)
}
