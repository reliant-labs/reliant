// Copyright (c) 2025 Reliant Labs
package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// AuthKind is how an account authenticates to its provider.
type AuthKind string

const (
	AuthKindOAuth   AuthKind = "oauth"
	AuthKindAPIKey  AuthKind = "api_key"
	AuthKindDevice  AuthKind = "device"
	AuthKindManaged AuthKind = "managed"
)

// Valid reports whether k is one of the four stored kinds.
func (k AuthKind) Valid() bool {
	switch k {
	case AuthKindOAuth, AuthKindAPIKey, AuthKindDevice, AuthKindManaged:
		return true
	}
	return false
}

var (
	// ErrProviderAccountNotFound covers a missing account and one owned by another user.
	ErrProviderAccountNotFound = errors.New("provider account not found")
	// ErrInvalidAccountOrder: a reorder that is not a permutation of the user's accounts for the provider.
	ErrInvalidAccountOrder = errors.New("account order must list each of the provider's accounts exactly once")
)

// ProviderAccount is one connected LLM credential. It carries no secrets.
type ProviderAccount struct {
	ID, UserID, Provider string
	AuthKind             AuthKind
	IdentityKey          string
	Label, Email         string
	ExternalAccountID    string
	OrganizationID       string
	OrganizationName     string
	Plan, PlanRaw        string
	KeyHint              string
	Priority             int
	PausedAt             *time.Time
	NeedsReauthAt        *time.Time
	NeedsReauthReason    string
	ExpiresAt            time.Time
	Scope                string
	CreatedAt, UpdatedAt time.Time
}

// DisplayLabel is Label, else Email, else "<provider> key …<hint>".
func (a ProviderAccount) DisplayLabel() string {
	if a.Label != "" {
		return a.Label
	}
	if a.Email != "" {
		return a.Email
	}
	name := a.Provider
	if name != "" {
		name = strings.ToUpper(name[:1]) + name[1:]
	}
	if a.KeyHint != "" {
		return name + " key …" + a.KeyHint
	}
	return name + " account"
}

// AccountStatus is the derived state of an account (design §9.1). It is never stored.
type AccountStatus string

const (
	AccountAvailable   AccountStatus = "available"
	AccountLimited     AccountStatus = "limited"
	AccountPaused      AccountStatus = "paused"
	AccountNeedsSignIn AccountStatus = "needs_sign_in"
)

// Status derives the account's state. Precedence: NeedsSignIn > Paused > Limited > Available.
// limits may be nil (never observed). Benching is lazy: limited iff limited_until is in the future.
func (a ProviderAccount) Status(limits *LimitState, now time.Time) AccountStatus {
	switch {
	case a.NeedsReauthAt != nil:
		return AccountNeedsSignIn
	case a.PausedAt != nil:
		return AccountPaused
	case limits != nil && limits.LimitedUntil != nil && limits.LimitedUntil.After(now):
		return AccountLimited
	}
	return AccountAvailable
}

// AccountSecrets are the sealed values of an account. Never log them.
type AccountSecrets struct{ Secret, Refresh, IDToken string }

// AccountWithSecrets is an account with its opened secrets.
type AccountWithSecrets struct {
	ProviderAccount
	Secrets AccountSecrets
}

// AccountUpsert connects an account, or re-connects the one with the same identity.
type AccountUpsert struct {
	UserID, Provider  string
	AuthKind          AuthKind
	IdentityKey       string
	Email             string
	ExternalAccountID string
	OrganizationID    string
	OrganizationName  string
	Plan, PlanRaw     string
	KeyHint, Scope    string
	Secrets           AccountSecrets
	ExpiresAt         time.Time
	// ReplaceIdentityOf names a `legacy:<…>` identity this write supersedes: the row holding it is
	// rewritten to IdentityKey in place (design §3.3). If another row already owns IdentityKey the
	// two merge: the row with the newer updated_at keeps its tokens and takes the smaller
	// priority, thread assignments are repointed to it, and the other row is deleted.
	ReplaceIdentityOf string
}

// AccountPatch changes the user-controlled state of an account. Nil fields are left alone.
type AccountPatch struct {
	Label       *string
	Paused      *bool
	NeedsReauth *string // sets the reason; a pointer to "" clears it
}

// AccountTokens is a refreshed credential. Empty Refresh / IDToken keep the stored value; empty
// identity metadata keeps the stored metadata.
type AccountTokens struct {
	Secret, Refresh, IDToken               string
	ExpiresAt                              time.Time
	Email, OrganizationName, Plan, PlanRaw string
}

// LimitWindow is one rate-limit window of an account, as persisted.
type LimitWindow struct {
	Name         string    `json:"name"`
	UsedFraction *float64  `json:"used_fraction,omitempty"`
	Limit        *int64    `json:"limit,omitempty"`
	Remaining    *int64    `json:"remaining,omitempty"`
	ResetsAt     time.Time `json:"resets_at,omitzero"`
	LengthS      int64     `json:"length_s,omitempty"`
}

// LimitState is the latest limit state of one account. ObservedAt is the zero time for an account
// whose usage was scheduled but never observed.
type LimitState struct {
	AccountID, UserID    string
	Status               string
	Windows              []LimitWindow
	RepresentativeWindow string
	Source               string // headers | usage_api | usage_limit_error
	ObservedAt           time.Time
	LimitedUntil         *time.Time
	LimitedWindow        string
	LimitedProbes        int
	UsageAPINextAt       *time.Time
	UsageAPIFailures     int
	UsageAPIError        string
}

// Assignment is a thread's sticky account for one provider.
type Assignment struct {
	ThreadID, Provider, AccountID string
	AssignedAt                    time.Time
	Reason                        string // initial | limited | paused | removed | needs_reauth | pinned
}

// ProviderAccountStore persists provider accounts, their limit state and thread assignments.
type ProviderAccountStore interface {
	// ListProviderAccounts returns metadata only, ordered by (provider, priority).
	ListProviderAccounts(ctx context.Context, userID string) ([]ProviderAccount, error)
	// ListProviderAccountsWithSecrets opens every secret. An account whose secret does not open
	// is skipped and logged, never fatal: one broken credential must not hide the others.
	ListProviderAccountsWithSecrets(ctx context.Context, userID string) ([]AccountWithSecrets, error)
	GetProviderAccountWithSecrets(ctx context.Context, userID, accountID string) (*AccountWithSecrets, error)
	// UpsertProviderAccount inserts at priority max+1, or updates the row with the same identity
	// (fresh secrets, needs_reauth cleared; label, priority and pause are kept).
	UpsertProviderAccount(ctx context.Context, u AccountUpsert) (acct ProviderAccount, created bool, err error)
	// CompareAndSwapAccountTokens writes next only if the stored refresh token still equals expectedRefresh.
	CompareAndSwapAccountTokens(ctx context.Context, userID, accountID, expectedRefresh string, next AccountTokens) (bool, error)
	PatchProviderAccount(ctx context.Context, userID, accountID string, p AccountPatch) (ProviderAccount, error)
	// ReorderProviderAccounts sets priorities 0..n-1 in orderedIDs order, atomically. orderedIDs
	// must be exactly the user's accounts for the provider (ErrInvalidAccountOrder otherwise).
	ReorderProviderAccounts(ctx context.Context, userID, provider string, orderedIDs []string) error
	// DeleteProviderAccount removes the account and re-densifies the provider's priorities.
	// Thread assignments and limit state cascade. ErrProviderAccountNotFound if absent.
	DeleteProviderAccount(ctx context.Context, userID, accountID string) error

	// ListAccountLimits returns limit state by account id for the user's accounts that have any.
	ListAccountLimits(ctx context.Context, userID string) (map[string]LimitState, error)
	// UpsertAccountLimitSnapshot stores the windows only if s.ObservedAt is newer than the stored
	// one (monotonic across processes). It never disturbs the bench or the poll schedule, except
	// that an "allowed" snapshot observed after the bench was set lifts it and clears the probe count.
	UpsertAccountLimitSnapshot(ctx context.Context, s LimitState) error
	// BenchAccount refuses the account until `until` (GREATEST with an existing bench). probe marks
	// an unknown-reset bench: it increments limited_probes; otherwise the count resets to 0.
	BenchAccount(ctx context.Context, userID, accountID string, until time.Time, window string, probe bool) error
	// LeaseUsagePolls returns ids whose usage_api_next_at <= now and pushes each to now+lease, so
	// concurrent leasers get disjoint sets.
	LeaseUsagePolls(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]string, error)
	// ScheduleUsagePoll sets the next poll time (zero = unschedule), failure count and last error.
	ScheduleUsagePoll(ctx context.Context, accountID string, next time.Time, failures int, errText string) error

	// GetThreadAssignment returns (nil, nil) when the thread has no account for the provider.
	GetThreadAssignment(ctx context.Context, threadID, provider string) (*Assignment, error)
	// SetThreadAssignment is a compare-and-set: it applies only if the thread's current account
	// for the provider equals expectedAccountID, where "" means "no assignment yet". applied=false
	// means another writer got there first; the caller reads the row and adopts it.
	SetThreadAssignment(ctx context.Context, a Assignment, expectedAccountID string) (applied bool, err error)
	// RecentAssignmentCounts counts threads assigned to each account since the given time.
	RecentAssignmentCounts(ctx context.Context, accountIDs []string, since time.Time) (map[string]int, error)
}

// LegacyCredential is a credential still held in a legacy token table, read during the
// expand release only.
type LegacyCredential struct {
	Source      string // legacy table
	IdentityKey string // identity derived from the legacy row
	Secrets     AccountSecrets
	ExpiresAt   time.Time
	UpdatedAt   time.Time
}

// LegacyCredentialSource reads the legacy row an account mirrors, so a failed refresh can adopt
// a newer lineage the previous release wrote (design §1.4). Deleted with the legacy tables.
type LegacyCredentialSource interface {
	// ReadLegacyCredential returns (nil, nil) when the account mirrors no legacy row or the row is gone.
	ReadLegacyCredential(ctx context.Context, userID, accountID string) (*LegacyCredential, error)
}

// ManagedIdentityKey is the identity of the managed Reliant key; at most one per user.
const ManagedIdentityKey = "managed"

// LegacyIdentityKey is the placeholder identity of a credential whose upstream identity is not
// yet known; the first refresh response rewrites it (AccountUpsert.ReplaceIdentityOf).
func LegacyIdentityKey(legacyRowID string) string { return "legacy:" + legacyRowID }

// IsLegacyIdentityKey reports whether k is a placeholder from LegacyIdentityKey.
func IsLegacyIdentityKey(k string) bool { return strings.HasPrefix(k, "legacy:") }

// ClaudeIdentityKey keys a Claude subscription account. The organization is part of the key
// because limits and prompt cache are per organization.
func ClaudeIdentityKey(accountUUID, organizationUUID string) string {
	return "acct:" + accountUUID + "/org:" + organizationUUID
}

// CodexIdentityKey keys a ChatGPT/Codex account.
func CodexIdentityKey(chatGPTAccountID, userPart string) string {
	return "acct:" + chatGPTAccountID + "/user:" + userPart
}

// GoogleIdentityKey keys an Antigravity account by its Google subject.
func GoogleIdentityKey(sub string) string { return "sub:" + sub }

// GitHubIdentityKey keys a Copilot account by its GitHub user id.
func GitHubIdentityKey(githubUserID string) string { return "gh:" + githubUserID }

// APIKeyIdentityKey keys an API-key account. The user and provider salt the digest so the
// stored fingerprint is useless across users.
func APIKeyIdentityKey(userID, provider, apiKey string) string {
	h := sha256.New()
	for _, part := range []string{userID, provider, apiKey} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return "key:" + hex.EncodeToString(h.Sum(nil))[:32]
}

// KeyHint is the last four characters of an API key, for display.
func KeyHint(apiKey string) string {
	if len(apiKey) <= 4 {
		return ""
	}
	return apiKey[len(apiKey)-4:]
}

// JWTIdentity is what a provider's sign-in JWT says about the account.
type JWTIdentity struct {
	IdentityKey       string
	Email             string
	ExternalAccountID string
	PlanRaw, Plan     string
}

// CodexIdentityFromJWT derives the ChatGPT identity from the id_token, falling back to the
// access token. ok is false when no claim set names a ChatGPT account and user.
func CodexIdentityFromJWT(idToken, accessToken string) (id JWTIdentity, ok bool) {
	for _, tok := range []string{idToken, accessToken} {
		claims := jwtClaims(tok)
		auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
		if auth == nil {
			continue
		}
		str := func(k string) string { s, _ := auth[k].(string); return s }
		acct := str("chatgpt_account_id")
		user := str("chatgpt_account_user_id")
		if user == "" {
			user = str("chatgpt_user_id")
		}
		if acct == "" || user == "" {
			continue
		}
		id.IdentityKey = CodexIdentityKey(acct, user)
		id.ExternalAccountID = acct
		id.PlanRaw = str("chatgpt_plan_type")
		id.Plan = NormalizePlan("codex", id.PlanRaw)
		id.Email, _ = claims["email"].(string)
		if id.Email == "" {
			profile, _ := claims["https://api.openai.com/profile"].(map[string]any)
			id.Email, _ = profile["email"].(string)
		}
		return id, true
	}
	return JWTIdentity{}, false
}

// GoogleIdentityFromIDToken derives the Antigravity identity from a Google id_token.
func GoogleIdentityFromIDToken(idToken string) (id JWTIdentity, ok bool) {
	claims := jwtClaims(idToken)
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return JWTIdentity{}, false
	}
	id.IdentityKey = GoogleIdentityKey(sub)
	id.ExternalAccountID = sub
	id.Email, _ = claims["email"].(string)
	return id, true
}

// jwtClaims decodes a JWT payload WITHOUT verifying it. It is only for labelling an account the
// user just signed in to; no authorization decision may rest on it.
func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

// NormalizePlan maps a provider's plan string to the normalized set (max_20x, max_5x, pro, plus,
// team, business, enterprise, free). "" means unknown.
func NormalizePlan(provider, raw string) string {
	r := strings.ToLower(strings.TrimSpace(raw))
	if r == "" {
		return ""
	}
	switch provider {
	case "claude":
		switch {
		case strings.Contains(r, "max_20x"), strings.Contains(r, "max20x"):
			return "max_20x"
		case strings.Contains(r, "max_5x"), strings.Contains(r, "max5x"):
			return "max_5x"
		case strings.Contains(r, "enterprise"):
			return "enterprise"
		case strings.Contains(r, "team"):
			return "team"
		case strings.Contains(r, "pro"):
			return "pro"
		case strings.Contains(r, "free"):
			return "free"
		}
		return ""
	case "codex":
		switch r {
		case "free", "plus", "pro", "team", "business", "enterprise":
			return r
		}
		return ""
	}
	return ""
}
