// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/observe"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// reliantKeyStore is the slice of the repository the healer needs.
type reliantKeyStore interface {
	ListProviderAccountsWithSecrets(ctx context.Context, userID string) ([]core.AccountWithSecrets, error)
	UpsertProviderAccount(ctx context.Context, u core.AccountUpsert) (core.ProviderAccount, bool, error)
	ListUserIDsWithProviderAccount(ctx context.Context, provider string) ([]string, error)
	LockProviderKey(ctx context.Context, userID, provider string) (release func(), err error)
}

// reliantKeyMinter mints an rlat_ token acting as a user, without their JWT.
type reliantKeyMinter interface {
	MintForUser(ctx context.Context, req accesstokenclient.MintRequest) (accesstokenclient.Minted, error)
}

// ReliantKeyHealer replaces a legacy-format `reliant` provider key with an
// rlat_ access token minted by control-plane — the same credential
// SyncReliantProvider stores — so no user has to click re-sync.
//
// Safe to call concurrently and repeatedly: replacement is serialized per user
// across replicas and re-checks the stored key under the lock, so a loser sees
// the winner's valid key and does nothing. The stored key is only overwritten
// after a new one has been minted, and a failed heal leaves it untouched.
//
// With per-org keys on (ControlPlaneConfig.managed_keys_per_org) it also mints
// the managed key of each org a person's chats run in (EnsureOrgKey), and
// rotates them with the org-less one (RotateOrgKeys).
type ReliantKeyHealer struct {
	store  reliantKeyStore
	minter reliantKeyMinter
	perOrg bool

	// failedOrgMints holds, per user and org, when an org key's mint last
	// failed, so a resolution in an org control-plane will not mint in (a
	// lapsed membership, an outage) does not ask again on every call.
	mu             sync.Mutex
	failedOrgMints map[string]time.Time
	now            func() time.Time
}

func NewReliantKeyHealer(store reliantKeyStore, minter reliantKeyMinter) *ReliantKeyHealer {
	return &ReliantKeyHealer{store: store, minter: minter, failedOrgMints: map[string]time.Time{}, now: time.Now}
}

// WithPerOrgKeys turns on minting a managed key per (person, org) and returns h.
func (h *ReliantKeyHealer) WithPerOrgKeys(on bool) *ReliantKeyHealer {
	h.perOrg = on
	return h
}

const healTimeout = 30 * time.Second

// Heal returns the usable key (empty when the user has no reliant key at all)
// and whether this call replaced the stored one. A non-nil error means the
// stored key is still the legacy one.
func (h *ReliantKeyHealer) Heal(ctx context.Context, userID string) (key string, healed bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, healTimeout)
	defer cancel()

	stored, err := h.readKey(ctx, userID)
	if err != nil || !isLegacyReliantKey(stored) {
		return stored, false, err
	}

	release, err := h.store.LockProviderKey(ctx, userID, "reliant")
	if err != nil {
		return "", false, fmt.Errorf("could not lock the key for replacement: %w", err)
	}
	defer release()

	// Another replica may have healed while we waited for the lock.
	stored, err = h.readKey(ctx, userID)
	if err != nil || !isLegacyReliantKey(stored) {
		return stored, false, err
	}

	minted, err := h.minter.MintForUser(ctx, accesstokenclient.MintRequest{
		UserID: userID,
		Name:   controlplane.ReliantProviderKeyName,
		Scopes: []fat.Scope{fat.ScopeLLMInvoke},
		Rotate: true,
	})
	if err != nil {
		return "", false, fmt.Errorf("control-plane could not mint a replacement key: %w", err)
	}
	fresh := strings.TrimSpace(minted.Plaintext)
	if !IsReliantLLMKey(fresh) {
		return "", false, errors.New("control-plane returned a malformed replacement key")
	}
	if _, _, err := h.store.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: userID, Provider: "reliant", AuthKind: core.AuthKindManaged, IdentityKey: core.ManagedIdentityKey,
		KeyHint: core.KeyHint(fresh), Secrets: core.AccountSecrets{Secret: fresh},
	}); err != nil {
		return "", false, fmt.Errorf("could not store the replacement key: %w", err)
	}
	logging.InfoContext(ctx, "Healed legacy reliant provider key", "user_id", userID)
	return fresh, true, nil
}

// readKey is the user's org-less reliant key: the one a person syncs, and the
// one every release before per-org keys reads. A key minted in an org is never
// it (orgKeys).
func (h *ReliantKeyHealer) readKey(ctx context.Context, userID string) (string, error) {
	accounts, err := h.store.ListProviderAccountsWithSecrets(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("could not read the stored key: %w", err)
	}
	if a := orglessReliantKey(accounts); a != nil {
		return strings.TrimSpace(a.Secrets.Secret), nil
	}
	return "", nil
}

// orglessReliantKey is the person's reliant row that is not a key minted in an
// org, or nil.
func orglessReliantKey(accounts []core.AccountWithSecrets) *core.AccountWithSecrets {
	for i := range accounts {
		if accounts[i].Provider == "reliant" && accounts[i].AttributedOrgID == "" {
			return &accounts[i]
		}
	}
	return nil
}

// orgReliantKey is the managed key minted in orgID, or nil.
func orgReliantKey(accounts []core.AccountWithSecrets, orgID string) *core.AccountWithSecrets {
	for i := range accounts {
		if accounts[i].Provider == "reliant" && accounts[i].IdentityKey == core.ManagedOrgIdentityKey(orgID) {
			return &accounts[i]
		}
	}
	return nil
}

// usableOrgReliantKey is the managed key minted in orgID when it can serve: an
// rlat_ token the gateway has not rejected (a rejection marks it needs-sign-in,
// MarkNeedsSignIn, and the next resolution mints it again). Otherwise nil.
func usableOrgReliantKey(accounts []core.AccountWithSecrets, orgID string) *core.AccountWithSecrets {
	a := orgReliantKey(accounts, orgID)
	if a == nil || a.NeedsReauthAt != nil || !IsReliantLLMKey(strings.TrimSpace(a.Secrets.Secret)) {
		return nil
	}
	return a
}

// orgMintRetry is how long a failed org key mint is not retried for a user and org.
const orgMintRetry = 5 * time.Minute

// EnsureOrgKey returns the managed key minted in orgID for userID ("" when
// there is none to use), minting it first when it is missing, not an rlat_
// token, or rejected by the gateway, and whether this call minted it. A key
// another replica minted while this one waited is returned, not minted.
// It mints only with per-org keys on and only for a person who uses Reliant
// credits at all (holds the org-less managed key): it never signs anyone up.
// Membership is control-plane's to enforce: a mint for an org the person is
// not a live member of is refused there. A failed mint is not retried for that
// user and org for orgMintRetry. Safe across replicas: the mint and write are
// serialized per user and re-checked under the lock.
func (h *ReliantKeyHealer) EnsureOrgKey(ctx context.Context, userID, orgID string) (key string, minted bool, err error) {
	if !h.perOrg || userID == "" || orgID == "" {
		return "", false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, healTimeout)
	defer cancel()

	var previous string
	have := func() (string, bool, error) {
		accounts, err := h.store.ListProviderAccountsWithSecrets(ctx, userID)
		if err != nil {
			return "", false, fmt.Errorf("could not read the stored keys: %w", err)
		}
		if a := usableOrgReliantKey(accounts, orgID); a != nil {
			return strings.TrimSpace(a.Secrets.Secret), true, nil
		}
		if a := orgReliantKey(accounts, orgID); a != nil {
			previous = strings.TrimSpace(a.Secrets.Secret)
		}
		// No org-less key: the person does not use Reliant credits.
		return "", orglessReliantKey(accounts) == nil, nil
	}
	if key, done, err := have(); err != nil || done {
		return key, false, err
	}
	if h.recentlyFailed(userID, orgID) {
		return "", false, nil
	}

	release, err := h.store.LockProviderKey(ctx, userID, "reliant")
	if err != nil {
		return "", false, fmt.Errorf("could not lock the keys for minting: %w", err)
	}
	defer release()
	if key, done, err := have(); err != nil || done {
		return key, false, err
	}
	fresh, err := h.mintOrgKey(ctx, userID, orgID, previous)
	if err != nil {
		h.markFailed(userID, orgID)
		return "", false, err
	}
	logging.InfoContext(ctx, "Minted the managed Reliant key in an org", "user_id", userID, "org_id", orgID)
	return fresh, true, nil
}

// RotateOrgKeys re-mints every managed key userID holds in an org, as a
// re-sync rotates the org-less one. With per-org keys off it does nothing.
// One org's failure does not stop the rest; the first error is returned.
func (h *ReliantKeyHealer) RotateOrgKeys(ctx context.Context, userID string) error {
	if !h.perOrg {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, healTimeout)
	defer cancel()
	release, err := h.store.LockProviderKey(ctx, userID, "reliant")
	if err != nil {
		return fmt.Errorf("could not lock the keys for rotation: %w", err)
	}
	defer release()
	accounts, err := h.store.ListProviderAccountsWithSecrets(ctx, userID)
	if err != nil {
		return fmt.Errorf("could not read the stored keys: %w", err)
	}
	var first error
	for _, a := range accounts {
		if a.Provider != "reliant" || a.AttributedOrgID == "" || a.IdentityKey != core.ManagedOrgIdentityKey(a.AttributedOrgID) {
			continue
		}
		if _, err := h.mintOrgKey(ctx, userID, a.AttributedOrgID, strings.TrimSpace(a.Secrets.Secret)); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// mintOrgKey mints userID's managed key in orgID, stores it, and only then
// revokes previous (the key it replaces, "" for none). The new key is minted
// without rotation, so until it is stored the old one still works: a failed
// write leaves the stored key serving, and the token it orphaned is revoked by
// the sweep (revokeOrphanedOrgKeys). A failed revoke of previous is logged; the
// sweep revokes it too.
func (h *ReliantKeyHealer) mintOrgKey(ctx context.Context, userID, orgID, previous string) (string, error) {
	minted, err := h.minter.MintForUser(ctx, accesstokenclient.MintRequest{
		UserID: userID,
		Name:   controlplane.ReliantOrgProviderKeyName,
		Scopes: []fat.Scope{fat.ScopeLLMInvoke},
		OrgID:  orgID,
	})
	if err != nil {
		return "", fmt.Errorf("control-plane could not mint the org's key: %w", err)
	}
	fresh := strings.TrimSpace(minted.Plaintext)
	if !IsReliantLLMKey(fresh) {
		return "", errors.New("control-plane returned a malformed org key")
	}
	if _, _, err := h.store.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: userID, Provider: "reliant", AuthKind: core.AuthKindManaged, IdentityKey: core.ManagedOrgIdentityKey(orgID),
		AttributedOrgID: orgID, AttributionSource: core.AttributionManaged,
		KeyHint: core.KeyHint(fresh), Secrets: core.AccountSecrets{Secret: fresh},
	}); err != nil {
		return "", fmt.Errorf("could not store the org's key: %w", err)
	}
	if previous != "" && previous != fresh {
		if err := h.revokeOrgKey(ctx, userID, previous); err != nil {
			logging.WarnContext(ctx, "Could not revoke the org key just replaced; the sweep revokes it",
				"user_id", userID, "org_id", orgID, "error", err)
		}
	}
	return fresh, nil
}

// orgKeyTokens lists and revokes a person's control-plane tokens: the
// access-token client the minter is in production.
type orgKeyTokens interface {
	ListForUser(ctx context.Context, userID string, scope fat.Scope) ([]accesstokenclient.TokenInfo, error)
	RevokeForUser(ctx context.Context, userID, tokenID string) error
}

// liveOrgKeyTokens is userID's live per-org managed key tokens, every org's.
func (h *ReliantKeyHealer) liveOrgKeyTokens(ctx context.Context, userID string) (orgKeyTokens, []accesstokenclient.TokenInfo, error) {
	tokens, ok := h.minter.(orgKeyTokens)
	if !ok {
		return nil, nil, nil
	}
	all, err := tokens.ListForUser(ctx, userID, fat.ScopeLLMInvoke)
	if err != nil {
		return nil, nil, err
	}
	var out []accesstokenclient.TokenInfo
	for _, t := range all {
		if t.Name == controlplane.ReliantOrgProviderKeyName && t.DisplayPrefix != "" {
			out = append(out, t)
		}
	}
	return tokens, out, nil
}

// revokeOrgKey revokes the live per-org token whose plaintext is key.
func (h *ReliantKeyHealer) revokeOrgKey(ctx context.Context, userID, key string) error {
	tokens, live, err := h.liveOrgKeyTokens(ctx, userID)
	if err != nil || tokens == nil {
		return err
	}
	for _, t := range live {
		if strings.HasPrefix(key, t.DisplayPrefix) {
			return tokens.RevokeForUser(ctx, userID, t.ID)
		}
	}
	return nil
}

// orphanAge is how old an unstored per-org token must be before the sweep
// revokes it, so a mint another replica has not stored yet is left alone.
const orphanAge = 10 * time.Minute

// revokeOrphanedOrgKeys revokes userID's per-org tokens that no stored key is
// (a replaced key whose revoke failed, a mint whose write failed), once they
// are orphanAge old, and reports how many.
func (h *ReliantKeyHealer) revokeOrphanedOrgKeys(ctx context.Context, userID string, accounts []core.AccountWithSecrets) (int, error) {
	tokens, live, err := h.liveOrgKeyTokens(ctx, userID)
	if err != nil || tokens == nil {
		return 0, err
	}
	n := 0
	for _, t := range live {
		if h.now().Sub(t.CreatedAt) < orphanAge {
			continue
		}
		stored := false
		for _, a := range accounts {
			if a.Provider == "reliant" && a.AttributedOrgID != "" && strings.HasPrefix(strings.TrimSpace(a.Secrets.Secret), t.DisplayPrefix) {
				stored = true
				break
			}
		}
		if stored {
			continue
		}
		if err := tokens.RevokeForUser(ctx, userID, t.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// healOrgKeys mints again every org key of userID the gateway rejected (or
// that is not an rlat_ token), and revokes the orphaned per-org tokens. With
// per-org keys off it does nothing. It reports how many keys it minted.
func (h *ReliantKeyHealer) healOrgKeys(ctx context.Context, userID string) (int, error) {
	if !h.perOrg {
		return 0, nil
	}
	accounts, err := h.store.ListProviderAccountsWithSecrets(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("could not read the stored keys: %w", err)
	}
	healed := 0
	var first error
	for _, a := range accounts {
		org := a.AttributedOrgID
		if a.Provider != "reliant" || org == "" || a.IdentityKey != core.ManagedOrgIdentityKey(org) || usableOrgReliantKey(accounts, org) != nil {
			continue
		}
		_, minted, err := h.EnsureOrgKey(ctx, userID, org)
		if err != nil && first == nil {
			first = err
		}
		if minted {
			healed++
		}
	}
	if accounts, err = h.store.ListProviderAccountsWithSecrets(ctx, userID); err == nil {
		if _, err := h.revokeOrphanedOrgKeys(ctx, userID, accounts); err != nil && first == nil {
			first = fmt.Errorf("could not revoke orphaned org keys: %w", err)
		}
	}
	return healed, first
}

func (h *ReliantKeyHealer) recentlyFailed(userID, orgID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	at, ok := h.failedOrgMints[userID+"\x00"+orgID]
	return ok && h.now().Sub(at) < orgMintRetry
}

func (h *ReliantKeyHealer) markFailed(userID, orgID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failedOrgMints[userID+"\x00"+orgID] = h.now()
}

func isLegacyReliantKey(key string) bool {
	return key != "" && key != "dummy" && !IsReliantLLMKey(key)
}

// Sweep heals every user still holding a legacy key. A failure for one user
// never stops the rest; it returns how many were healed and how many failed.
func (h *ReliantKeyHealer) Sweep(ctx context.Context) (healed, failed int, err error) {
	ids, err := h.store.ListUserIDsWithProviderAccount(ctx, "reliant")
	if err != nil {
		return 0, 0, fmt.Errorf("listing users with a reliant key: %w", err)
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return healed, failed, ctx.Err()
		}
		_, did, herr := h.Heal(ctx, id)
		switch {
		case herr != nil:
			failed++
			logging.WarnContext(ctx, "Reliant key sweep: heal failed", "user_id", id, "error", herr)
		case did:
			healed++
		}
		orgHealed, oerr := h.healOrgKeys(ctx, id)
		healed += orgHealed
		if oerr != nil {
			failed++
			logging.WarnContext(ctx, "Reliant key sweep: an org key's heal failed", "user_id", id, "error", oerr)
		}
	}
	return healed, failed, nil
}

var (
	reliantKeyHealerMu sync.RWMutex
	reliantKeyHealer   *ReliantKeyHealer
)

// SetReliantKeyHealer installs the process-wide healer. Unset (self-hosted, no
// control-plane) a legacy key is only reported, never replaced.
func SetReliantKeyHealer(h *ReliantKeyHealer) {
	reliantKeyHealerMu.Lock()
	reliantKeyHealer = h
	reliantKeyHealerMu.Unlock()
}

func currentReliantKeyHealer() *ReliantKeyHealer {
	reliantKeyHealerMu.RLock()
	defer reliantKeyHealerMu.RUnlock()
	return reliantKeyHealer
}

// EnsureOrgReliantKey makes userID's managed key in orgID usable through the
// installed healer when per-org keys are on, minting it when it is missing or
// was rejected. available reports whether a usable key is stored now, whoever
// minted it (this call, or another resolution that won the race), so the
// caller reads the accounts again. With no healer installed it does nothing.
func EnsureOrgReliantKey(ctx context.Context, userID, orgID string) (available bool, err error) {
	h := currentReliantKeyHealer()
	if h == nil {
		return false, nil
	}
	key, _, err := h.EnsureOrgKey(ctx, userID, orgID)
	return key != "", err
}

// RotateOrgReliantKeys rotates userID's managed keys minted in orgs through
// the installed healer (a re-sync). With no healer installed it does nothing.
func RotateOrgReliantKeys(ctx context.Context, userID string) error {
	h := currentReliantKeyHealer()
	if h == nil {
		return nil
	}
	return h.RotateOrgKeys(ctx, userID)
}

// HealReliantKey heals userID's key through the installed healer. configured
// is false when no healer is installed.
func HealReliantKey(ctx context.Context, userID string) (key string, configured bool, err error) {
	h := currentReliantKeyHealer()
	if h == nil {
		return "", false, nil
	}
	key, _, err = h.Heal(ctx, userID)
	return key, true, err
}

// RunReliantKeySweeps sweeps once immediately and then every interval until ctx ends.
func RunReliantKeySweeps(ctx context.Context, h *ReliantKeyHealer, interval time.Duration) {
	run := func() {
		var healed, failed int
		// One trace per sweep, so its queries and control-plane calls nest under it.
		err := observe.RunRoot(ctx, "reliant_key.sweep", func(ctx context.Context) error {
			var err error
			healed, failed, err = h.Sweep(ctx)
			return err
		})
		if err != nil {
			logging.WarnContext(ctx, "Reliant key sweep failed", "error", err)
			return
		}
		logging.InfoContext(ctx, "Reliant key sweep finished", "healed", healed, "failed", failed)
	}
	run()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// InstallReliantKeyHealer wires the healer for a hosted deployment (a
// control-plane URL and internal-service secret) and returns it, for a caller
// that also runs the background sweep (RunReliantKeySweeps). It is a no-op
// returning nil otherwise: a self-hosted reliant has no control-plane to mint
// from.
//
// perOrg is ControlPlaneConfig.managed_keys_per_org: whether a managed key is
// minted per (person, org).
func InstallReliantKeyHealer(repo *db.Repo, baseURL, internalSecret string, perOrg bool) *ReliantKeyHealer {
	if baseURL == "" || internalSecret == "" {
		return nil
	}
	minter := accesstokenclient.NewInstrumented(accesstokenclient.Deps{
		BaseURL: baseURL,
		Sign:    func() (string, error) { return auth.SignInternalServiceToken(internalSecret) },
	})
	h := NewReliantKeyHealer(repo, minter).WithPerOrgKeys(perOrg)
	SetReliantKeyHealer(h)
	return h
}
