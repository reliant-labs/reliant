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

// CodexAuthTokens stores persisted Codex OAuth credentials for a user.
type CodexAuthTokens struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	AccountID    string
}

// CopilotAuthTokens stores persisted GitHub Copilot OAuth credentials for a user.
type CopilotAuthTokens struct {
	UserID             string
	GitHubAccessToken  string
	GitHubRefreshToken string
	Tier               string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ClaudeAuthTokens stores persisted Claude OAuth credentials for a user.
type ClaudeAuthTokens struct {
	AccessToken      string
	RefreshToken     string
	ExpiresAt        time.Time
	AccountUUID      string
	AccountEmail     string
	OrganizationUUID string
	OrganizationName string
	Scope            string
}

// SettingStore is the shared contract for settings persistence across drivers.
type SettingStore interface {
	CreateSetting(ctx context.Context, setting *Setting) error
	GetSetting(ctx context.Context, userID string, projectID *string, key string) (*Setting, error)
	ListSettings(ctx context.Context, userID string, projectID *string) ([]*Setting, error)
	ListSettingsByKey(ctx context.Context, userID string, keyPattern string) ([]*Setting, error)
	UpdateSetting(ctx context.Context, setting *Setting) error
	DeleteSetting(ctx context.Context, id string) error

	GetProviderAPIKey(ctx context.Context, userID string, provider string) (string, error)
	SetProviderAPIKey(ctx context.Context, userID string, provider, apiKey string) error
	DeleteProviderAPIKey(ctx context.Context, userID string, provider string) error
	GetProviderAPIKeys(ctx context.Context, userID string) (map[string]string, error)

	GetCodexAuthTokens(ctx context.Context, userID string) (*CodexAuthTokens, error)
	SetCodexAuthTokens(ctx context.Context, userID string, tokens CodexAuthTokens) error
	// CompareAndSwapCodexAuthTokens persists tokens only if the currently
	// stored refresh token still equals expectedRefreshToken, i.e. no
	// concurrent rotation has been persisted since the caller read it.
	// Returns true when the row was updated. Codex OAuth refresh tokens are
	// single-use (rotated on every exchange), so a plain last-writer-wins
	// upsert would let a stale rotation clobber the live token lineage.
	CompareAndSwapCodexAuthTokens(ctx context.Context, userID string, expectedRefreshToken string, tokens CodexAuthTokens) (bool, error)
	DeleteCodexAuthTokens(ctx context.Context, userID string) error

	GetCopilotAuthTokens(ctx context.Context, userID string) (*CopilotAuthTokens, error)
	SetCopilotAuthTokens(ctx context.Context, userID string, tokens CopilotAuthTokens) error
	DeleteCopilotAuthTokens(ctx context.Context, userID string) error

	GetClaudeAuthTokens(ctx context.Context, userID string) (*ClaudeAuthTokens, error)
	SetClaudeAuthTokens(ctx context.Context, userID string, tokens ClaudeAuthTokens) error
	// CompareAndSwapClaudeAuthTokens persists tokens only if the currently
	// stored refresh token still equals expectedRefreshToken, i.e. no
	// concurrent rotation has been persisted since the caller read it.
	// Returns true when the row was updated. Claude OAuth refresh tokens are
	// single-use (rotated on every exchange), so a plain last-writer-wins
	// upsert would let a stale rotation clobber the live token lineage.
	CompareAndSwapClaudeAuthTokens(ctx context.Context, userID string, expectedRefreshToken string, tokens ClaudeAuthTokens) (bool, error)
	DeleteClaudeAuthTokens(ctx context.Context, userID string) error

	GetVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string) (*bool, error)
	ListVisibilityOverrides(ctx context.Context, userID string, itemType int32) (map[string]bool, error)
	SetVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string, isVisible bool) error
	DeleteVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string) error
	GetItemDefault(ctx context.Context, itemType int32, slug string) (*ItemDefault, error)
	ListHiddenItemDefaults(ctx context.Context, itemType int32) ([]string, error)
	GetDefaultPresetAssignments(ctx context.Context, workflowName string) (map[string]string, error)
}
