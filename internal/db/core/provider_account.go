// Copyright (c) 2025 Reliant Labs
package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
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
	ErrProviderAccountNotFound = svcerr.NotFound("provider account")
	// ErrInvalidAccountOrder: a reorder that is not a permutation of the user's accounts for the provider.
	ErrInvalidAccountOrder = svcerr.InvalidArgument("account order must list each of the provider's accounts exactly once")
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
	// ReplaceIdentityOf names a placeholder identity (PlaceholderIdentityKey) this write supersedes:
	// the row holding it is rewritten to IdentityKey in place (design §3.3). A non-placeholder value
	// is ignored: a real identity is never rewritten. If another row already owns IdentityKey the
	// two are the same upstream account and merge INTO that row (the placeholder is the one deleted):
	// it keeps its id, label, pause and limit state, takes the smaller priority and the newer tokens,
	// and thread assignments are repointed to it.
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
	// IdentityKey, ExternalAccountID and OrganizationID are what the refresh response says the
	// account is. The compare-and-swap ignores them; the refresh coordinator uses them to
	// rewrite a placeholder identity (design §3.3).
	IdentityKey, ExternalAccountID, OrganizationID string
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
	// LastTrafficAt is when a response served by the account last carried rate-limit headers.
	// Zero when none has. Unlike ObservedAt, a usage-API poll never moves it, which is what
	// lets the poller tell an account in use from one it merely polled.
	LastTrafficAt time.Time
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

// ManagedIdentityKey is the identity of the managed Reliant key; at most one per user.
const ManagedIdentityKey = "managed"

// placeholderIdentityPrefix is stored in rows, so it stays "legacy:" for the placeholders already
// written; only the Go names say what it is.
const placeholderIdentityPrefix = "legacy:"

// PlaceholderIdentityKey is the identity of a credential whose upstream identity is not yet
// known; the first refresh response rewrites it (AccountUpsert.ReplaceIdentityOf). A
// placeholder is not an identity: it never outranks a real one.
func PlaceholderIdentityKey(rowID string) string { return placeholderIdentityPrefix + rowID }

// IsPlaceholderIdentityKey reports whether k is a placeholder from PlaceholderIdentityKey.
func IsPlaceholderIdentityKey(k string) bool { return strings.HasPrefix(k, placeholderIdentityPrefix) }

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
		return NormalizeCodexPlan(r)
	}
	return ""
}

// NormalizeCodexPlan maps a ChatGPT plan string (id_token chatgpt_plan_type or wham/usage
// plan_type) to the stored vocabulary. "" is unknown; an unrecognised non-empty plan passes
// through lowercased so a new tier is shown rather than lost.
func NormalizeCodexPlan(raw string) string {
	p := strings.ToLower(strings.TrimSpace(raw))
	switch p {
	case "":
		return ""
	case "free", "guest", "free_workspace":
		return "free"
	case "go":
		return "go"
	case "plus":
		return "plus"
	case "pro":
		return "pro"
	case "prolite":
		return "pro_lite"
	case "promax":
		return "pro_max"
	case "team":
		return "team"
	case "business", "self_serve_business_prolite", "self_serve_business_usage_based":
		return "business"
	case "education", "edu", "edu_plus", "edu_pro", "k12", "quorum":
		return "edu"
	}
	if strings.HasPrefix(p, "enterprise") || strings.HasPrefix(p, "ent") && len(p) > 3 && p[3] >= '0' && p[3] <= '9' {
		return "enterprise"
	}
	return p
}
