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

// SettingStore is the shared contract for the api_keys automation tokens and
// the visibility overrides across drivers. Settings, item defaults and
// default preset assignments are in internal/store.
type SettingStore interface {
	// Automation tokens: the daemon:resume credential minted for one (user, daemon), kept in api_keys.
	GetAutomationToken(ctx context.Context, userID, daemonID string) (string, error)
	SetAutomationToken(ctx context.Context, userID, daemonID, token string) error
	DeleteAutomationToken(ctx context.Context, userID, daemonID string) error
}
