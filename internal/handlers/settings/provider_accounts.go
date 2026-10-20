package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/internal/llm/providerusage"
	"github.com/reliant-labs/reliant/internal/logging"
)

// A sign-in is an account decision, made here once for every OAuth provider and for both
// transports (Electron's local receiver and `reliant auth serve` only deliver the code):
//
//  1. the provider's identity is derived from what it handed back (Claude: account + org uuid;
//     Codex: the id_token's account and user; Antigravity: the Google subject; Copilot: the
//     GitHub user id), together with the email that labels the account and its plan;
//  2. the account is upserted by that identity, so signing in to an upstream account that is
//     already connected refreshes it (created = false) and signing in to another adds one;
//  3. a reconnect names its target account: a different identity lands as its own account and
//     the response says so, leaving the target waiting for its own sign-in.

// signIn is one completed provider sign-in, ready to become an account.
type signIn struct {
	userID   string
	provider string
	upsert   core.AccountUpsert
	// label names a new account after the signed-in identity when it has no email (Copilot).
	label string
	// targetID is the account a reconnect named; "" = add / sign in.
	targetID string
}

// signInResult is what the Complete* RPCs answer with.
type signInResult struct {
	account  *reliantv1.ProviderAccount
	created  bool
	mismatch *string
}

// unidentified is the identity of a sign-in that carried nothing to tell accounts apart. It is a
// legacy placeholder so the first refresh that does learn the identity renames it in place.
func unidentified(provider string) string { return core.PlaceholderIdentityKey("signin-" + provider) }

// connectAccount stores a completed sign-in as a provider account.
func (s *Service) connectAccount(ctx context.Context, in signIn) (signInResult, error) {
	repo := s.deps.Repo
	if in.upsert.IdentityKey == "" {
		in.upsert.IdentityKey = unidentified(in.provider)
	}
	in.upsert.UserID, in.upsert.Provider = in.userID, in.provider

	var target *core.ProviderAccount
	if in.targetID != "" {
		accounts, err := repo.ListProviderAccounts(ctx, in.userID)
		if err != nil {
			return signInResult{}, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read accounts"))
		}
		for i := range accounts {
			if accounts[i].ID == in.targetID {
				target = &accounts[i]
			}
		}
		if target == nil || target.Provider != in.provider {
			// Another user's account, or one of another provider: NotFound, never a hint it exists.
			return signInResult{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("provider account not found"))
		}
		if core.IsPlaceholderIdentityKey(target.IdentityKey) {
			// A placeholder cannot be compared with a real identity; this sign-in is what teaches it.
			in.upsert.ReplaceIdentityOf = target.IdentityKey
		}
	}

	acct, created, err := repo.UpsertProviderAccount(ctx, in.upsert)
	if err != nil {
		logging.ErrorContext(ctx, "Failed to persist provider account", "provider", in.provider, "error", err)
		return signInResult{}, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save %s credentials", providerName(in.provider)))
	}
	if created && in.label != "" && acct.Label == "" && acct.Email == "" {
		if acct, err = repo.PatchProviderAccount(ctx, in.userID, acct.ID, core.AccountPatch{Label: &in.label}); err != nil {
			logging.WarnContext(ctx, "Failed to label new provider account", "provider", in.provider, "error", err)
		}
	}

	res := signInResult{created: created}
	if target != nil && acct.ID != target.ID {
		msg := fmt.Sprintf("You signed in as %s, not %s — saved as a separate account", acct.DisplayLabel(), target.DisplayLabel())
		res.mismatch = &msg
	}

	if providerusage.UsageAPIEnabled(in.provider) {
		if err := repo.ScheduleUsagePoll(ctx, acct.ID, time.Now(), 0, ""); err != nil {
			logging.WarnContext(ctx, "Failed to schedule usage poll for new account", "provider", in.provider, "error", err)
		}
	}

	analytics.ForUser(ctx, in.userID).TrackProviderSettingsUpdated(analytics.ProviderSettingsUpdatedMetrics{
		Provider: in.provider, Action: "connected", AuthMethod: "oauth",
	})
	if err := repo.EmitUserRefetch(ctx, in.userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after provider sign-in", "provider", in.provider, "error", err)
	}

	limits, _ := repo.ListAccountLimits(ctx, in.userID)
	var l *core.LimitState
	if v, ok := limits[acct.ID]; ok {
		l = &v
	}
	res.account = accountlimits.ProtoAccount(acct, l, time.Now())
	return res, nil
}

func providerName(provider string) string {
	switch provider {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "copilot":
		return "GitHub Copilot"
	case "antigravity":
		return "Antigravity"
	}
	return provider
}

// claudeIdentity derives the Claude subscription identity from the token exchange, and the plan
// from the profile endpoint (best effort: a failed profile read leaves the plan unknown, and the
// account polls it later).
func (s *Service) claudeIdentity(ctx context.Context, accessToken, accountUUID, orgUUID, email, orgName, scope string, expiresAt time.Time, refresh string) core.AccountUpsert {
	u := core.AccountUpsert{
		AuthKind: core.AuthKindOAuth,
		Email:    strings.TrimSpace(email), ExternalAccountID: accountUUID,
		OrganizationID: orgUUID, OrganizationName: orgName, Scope: scope,
		Secrets:   core.AccountSecrets{Secret: accessToken, Refresh: refresh},
		ExpiresAt: expiresAt,
	}
	if s.deps.ClaudeProfile != nil {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if p, err := s.deps.ClaudeProfile(pctx, accessToken); err != nil {
			logging.WarnContext(ctx, "Claude profile read failed; the plan stays unknown until the next poll", "error", err)
		} else {
			if u.Email == "" {
				u.Email = p.Email
			}
			if accountUUID == "" || orgUUID == "" {
				u.ExternalAccountID, u.OrganizationID = p.AccountUUID, p.OrganizationUUID
				if u.OrganizationName == "" {
					u.OrganizationName = p.OrganizationName
				}
			}
			u.Plan = p.Plan
			u.PlanRaw = p.RateLimitTier
			if u.PlanRaw == "" {
				u.PlanRaw = p.OrganizationType
			}
		}
	}
	if u.ExternalAccountID != "" && u.OrganizationID != "" {
		u.IdentityKey = core.ClaudeIdentityKey(u.ExternalAccountID, u.OrganizationID)
	}
	return u
}

// codexIdentity derives the ChatGPT identity, email and plan from the token claims.
func codexIdentity(accessToken, refresh, idToken, accountID string) core.AccountUpsert {
	u := core.AccountUpsert{
		AuthKind: core.AuthKindOAuth, ExternalAccountID: accountID,
		Secrets: core.AccountSecrets{Secret: accessToken, Refresh: refresh, IDToken: idToken},
	}
	// Each token is decoded on its own: CodexIdentity rejects a call when ANY supplied token is not
	// a JWT, and the id_token's claims must not be lost to an opaque access token.
	var info providerusage.CodexIdentityInfo
	for _, tok := range [][2]string{{idToken, ""}, {"", accessToken}} {
		got, err := providerusage.CodexIdentity(tok[0], tok[1])
		if err != nil {
			continue
		}
		if info.Email == "" {
			info.Email = got.Email
		}
		if info.AccountID == "" {
			info.AccountID = got.AccountID
		}
		if info.UserID == "" {
			info.UserID = got.UserID
		}
		if info.PlanRaw == "" {
			info.PlanRaw, info.Plan = got.PlanRaw, got.Plan
		}
	}
	if info.AccountID == "" && info.Email == "" {
		logging.Warn("Codex sign-in carried no readable account claims; the account stays unlabelled")
		return u
	}
	u.Email, u.Plan, u.PlanRaw = info.Email, info.Plan, info.PlanRaw
	if info.AccountID != "" {
		u.ExternalAccountID = info.AccountID
	}
	u.IdentityKey = info.IdentityKey()
	return u
}

// GitHubUser is the GitHub account behind a Copilot device-flow token.
type GitHubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Email string `json:"email"`
}

// FetchGitHubUser reads GET https://api.github.com/user with the device-flow token.
func FetchGitHubUser(ctx context.Context, accessToken string) (GitHubUser, error) {
	var id GitHubUser
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return id, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "reliant")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return id, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return id, fmt.Errorf("github user: HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&id); err != nil {
		return id, fmt.Errorf("github user: %w", err)
	}
	return id, nil
}
