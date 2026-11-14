// Copyright (c) 2025 Reliant Labs
package core

import (
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

// Account owner kinds: provider_accounts.owner_kind.
const (
	AccountOwnerUser = "user"
	AccountOwnerOrg  = "org"
)

// OrgOwnerKeyPrefix starts the owner key an org-owned account holds in provider_accounts.user_id
// ("org:<org_id>"). The schema guarantees no person's row ever starts with it.
const OrgOwnerKeyPrefix = "org:"

// Org account audiences: provider_accounts.org_access, set exactly on an org-owned row.
const (
	OrgAccessMembers  = "members"  // every live member of the org
	OrgAccessAdmins   = "admins"   // live owners and admins
	OrgAccessSelected = "selected" // only members holding a provider_account_grants row
)

// ValidOrgAccess reports whether s is one of the three audiences.
func ValidOrgAccess(s string) bool {
	switch s {
	case OrgAccessMembers, OrgAccessAdmins, OrgAccessSelected:
		return true
	}
	return false
}

// Attribution sources: provider_accounts.attribution_source, set exactly with attributed_org_id
// on a person's row that an org pays for.
const (
	AttributionWorkspace = "workspace" // a seat matched to the org's bound workspace
	AttributionApproved  = "approved"  // a seat an org admin approved by hand
	AttributionManaged   = "managed"   // the managed Reliant key minted in that org
)

var (
	// ErrOrgAPIKeyInUse: a rotation to a key the org already holds as another of its accounts.
	ErrOrgAPIKeyInUse = svcerr.AlreadyExists("that key is already another of the organization's accounts")
	// ErrProviderWorkspaceNotFound covers a binding that does not exist and one of another org.
	ErrProviderWorkspaceNotFound = svcerr.NotFound("provider workspace")
	// ErrProviderWorkspaceBoundElsewhere: the workspace already pays for another org
	// (org_provider_workspaces_one_org). It never names that org.
	ErrProviderWorkspaceBoundElsewhere = svcerr.AlreadyExists("that workspace is already bound to another organization")
	// ErrOrgSeatNotFound covers an account that is not a seat the org pays for: a personal one,
	// another org's seat, or the managed key minted in the org (which is not released).
	ErrOrgSeatNotFound = svcerr.NotFound("seat")
)

// OrgAPIKey is the new credential of an org's API-key account being rotated: its identity
// (APIKeyIdentityKey over the org's owner key), display hint and the key itself. Never log it.
type OrgAPIKey struct {
	IdentityKey, KeyHint, Secret string
}

// OrgAccountGrant is one member's grant of an org account whose audience is OrgAccessSelected
// (provider_account_grants). It counts only while the member is live and GrantedAt is at or after
// their membership began (org-provider-accounts.md §3.3); the store records it, routing judges it.
type OrgAccountGrant struct {
	AccountID, OrgID, UserID, GrantedByUserID string
	GrantedAt                                 time.Time
}

// OrgProviderWorkspace is a provider workspace an org pays for (org_provider_workspaces): a
// Claude Team/Enterprise organization or a ChatGPT Business/Team workspace. Members' sign-ins in
// it are the org's seats.
type OrgProviderWorkspace struct {
	OrgID, Provider, ExternalOrgID, DisplayName, BoundByUserID string
	BoundAt                                                    time.Time
}

// ProviderWorkspaceID is the provider's id of the workspace a sign-in belongs to, the value a
// binding's external_org_id holds: the Claude token's organization uuid, or the Codex JWT's
// chatgpt_account_id. "" for any other account, which has no workspace. It is provider-issued
// identity the account was connected with, never a value a person typed.
func ProviderWorkspaceID(a ProviderAccount) string {
	if a.AuthKind != AuthKindOAuth {
		return ""
	}
	switch a.Provider {
	case "claude":
		return a.OrganizationID
	case "codex":
		return a.ExternalAccountID
	}
	return ""
}

// TeamPlan reports whether plan (NormalizePlan's vocabulary) is a provider workspace an org pays
// per seat for: Claude Team or Enterprise, ChatGPT Team, Business or Enterprise.
func TeamPlan(provider, plan string) bool {
	switch provider {
	case "claude":
		return plan == "team" || plan == "enterprise"
	case "codex":
		return plan == "team" || plan == "business" || plan == "enterprise"
	}
	return false
}

// AccountOwner is who a provider account belongs to: a person (Kind AccountOwnerUser, ID their
// user id) or an organization (Kind AccountOwnerOrg, ID its org id). It is core.ConnectionOwner's
// twin. Every store read and write that takes one scopes to that owner in SQL, and the vault
// tenant an account's secrets are sealed under is chosen from it, so an org's account is never
// read, written or opened as a person's (its creator's included), nor a person's as an org's.
//
// An org-owned row stores Key() ("org:<org_id>") in provider_accounts.user_id
// (control-plane docs/design/org-provider-accounts.md §3.1); nothing outside the store reads
// that column raw.
type AccountOwner struct {
	Kind string
	ID   string
}

// UserAccountOwner is the owner of userID's personal accounts.
func UserAccountOwner(userID string) AccountOwner {
	return AccountOwner{Kind: AccountOwnerUser, ID: userID}
}

// OrgAccountOwner is the owner of orgID's accounts.
func OrgAccountOwner(orgID string) AccountOwner {
	return AccountOwner{Kind: AccountOwnerOrg, ID: orgID}
}

// Valid reports whether o names a real owner: a known kind and a non-empty id, where a person's
// id never looks like an owner key. UserAccountOwner("org:<id>") is NOT valid: it is neither
// that org nor any person.
func (o AccountOwner) Valid() bool {
	switch o.Kind {
	case AccountOwnerUser:
		return o.ID != "" && !strings.HasPrefix(o.ID, OrgOwnerKeyPrefix)
	case AccountOwnerOrg:
		return o.ID != ""
	}
	return false
}

// IsOrg reports whether o is an organization.
func (o AccountOwner) IsOrg() bool { return o.Kind == AccountOwnerOrg }

// Key is the value o's rows hold in provider_accounts.user_id (and provider_account_limits.user_id):
// the person's id, or "org:<org_id>". An invalid owner's key is "", which no row holds, so a
// query by it matches nothing.
func (o AccountOwner) Key() string {
	if !o.Valid() {
		return ""
	}
	if o.Kind == AccountOwnerOrg {
		return OrgOwnerKeyPrefix + o.ID
	}
	return o.ID
}

// ProviderAccount is one connected LLM credential. It carries no secrets.
type ProviderAccount struct {
	// UserID is the stored owner key (AccountOwner.Key). Read Owner() instead: for an org-owned
	// account it is not a person.
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

	// OwnerKind is AccountOwnerUser or AccountOwnerOrg; OrgID is set exactly for an org's account.
	OwnerKind string
	OrgID     string
	// OrgAccess is an org account's audience (OrgAccess*); "" for a person's.
	OrgAccess string
	// CreatedByUserID is the admin who added an org account: attribution, never authority.
	CreatedByUserID string
	// AttributedOrgID is the org that pays for a person's account (a seat, or the managed key
	// minted in that org), with AttributionSource saying how; both "" for a plain personal one.
	AttributedOrgID   string
	AttributionSource string
}

// Owner is who a belongs to. A row predating owner_kind reads as its user's.
func (a ProviderAccount) Owner() AccountOwner {
	if a.OwnerKind == AccountOwnerOrg {
		// The schema's CHECK guarantees an org id; were it ever missing, the owner is invalid
		// (it opens nothing and matches no row) rather than falling back to user_id.
		return OrgAccountOwner(a.OrgID)
	}
	return UserAccountOwner(a.UserID)
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
	// UserID is the person whose account this is. Owner, when set, overrides it: an org-owned
	// account is written with Owner = OrgAccountOwner(org), OrgAccess and CreatedByUserID, and
	// may only be an API key.
	UserID, Provider  string
	Owner             AccountOwner
	OrgAccess         string
	CreatedByUserID   string
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
	// AttributedOrgID and AttributionSource attribute a new row to the org that pays for it.
	// Only a managed key minted in an org is attributed when it is written (identity
	// ManagedOrgIdentityKey(org), source AttributionManaged); a seat is attributed by its
	// workspace binding, never here. Both "" otherwise. An existing row keeps its attribution.
	AttributedOrgID   string
	AttributionSource string
	// ReplaceIdentityOf names a placeholder identity (PlaceholderIdentityKey) this write supersedes:
	// the row holding it is rewritten to IdentityKey in place (design §3.3). A non-placeholder value
	// is ignored: a real identity is never rewritten. If another row already owns IdentityKey the
	// two are the same upstream account and merge INTO that row (the placeholder is the one deleted):
	// it keeps its id, label, pause and limit state, takes the smaller priority and the newer tokens,
	// and thread assignments are repointed to it.
	ReplaceIdentityOf string
}

// AccountOwner is who the written account belongs to: Owner when set, else UserID's.
func (u AccountUpsert) AccountOwner() AccountOwner {
	if u.Owner != (AccountOwner{}) {
		return u.Owner
	}
	return UserAccountOwner(u.UserID)
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

// ManagedIdentityKey is the identity of the org-less managed Reliant key; at most one per user.
// It is minted in the person's default org (where it bills), and is what every release before
// P10 writes and reads.
const ManagedIdentityKey = "managed"

// managedOrgIdentityPrefix is the identity prefix of a managed Reliant key minted in one org.
const managedOrgIdentityPrefix = "managed:"

// ManagedOrgIdentityKey is the identity of the managed Reliant key minted in orgID
// (org-provider-accounts §3.2, G0's per-org key, P10): one per (person, org), attributed to
// orgID with source "managed", so the gateway bills that org's wallet for the chats of that org.
func ManagedOrgIdentityKey(orgID string) string { return managedOrgIdentityPrefix + orgID }

// IsManagedIdentityKey reports whether key is a managed Reliant key's identity: the org-less
// one, or one minted in an org. provider_accounts_one_managed holds a managed row to these.
func IsManagedIdentityKey(key string) bool {
	return key == ManagedIdentityKey || (strings.HasPrefix(key, managedOrgIdentityPrefix) && len(key) > len(managedOrgIdentityPrefix))
}

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
