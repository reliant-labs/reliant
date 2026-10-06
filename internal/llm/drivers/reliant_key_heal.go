// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// reliantKeyStore is the slice of the repository the healer needs.
type reliantKeyStore interface {
	GetProviderAPIKey(ctx context.Context, userID, provider string) (string, error)
	SetProviderAPIKey(ctx context.Context, userID, provider, apiKey string) error
	ListUserIDsWithProviderKey(ctx context.Context, provider string) ([]string, error)
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
type ReliantKeyHealer struct {
	store  reliantKeyStore
	minter reliantKeyMinter
}

func NewReliantKeyHealer(store reliantKeyStore, minter reliantKeyMinter) *ReliantKeyHealer {
	return &ReliantKeyHealer{store: store, minter: minter}
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
	if err := h.store.SetProviderAPIKey(ctx, userID, "reliant", fresh); err != nil {
		return "", false, fmt.Errorf("could not store the replacement key: %w", err)
	}
	logging.Info("Healed legacy reliant provider key", "user_id", userID)
	return fresh, true, nil
}

func (h *ReliantKeyHealer) readKey(ctx context.Context, userID string) (string, error) {
	key, err := h.store.GetProviderAPIKey(ctx, userID, "reliant")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("could not read the stored key: %w", err)
	}
	return strings.TrimSpace(key), nil
}

func isLegacyReliantKey(key string) bool {
	return key != "" && key != "dummy" && !IsReliantLLMKey(key)
}

// Sweep heals every user still holding a legacy key. A failure for one user
// never stops the rest; it returns how many were healed and how many failed.
func (h *ReliantKeyHealer) Sweep(ctx context.Context) (healed, failed int, err error) {
	ids, err := h.store.ListUserIDsWithProviderKey(ctx, "reliant")
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
			logging.Warn("Reliant key sweep: heal failed", "user_id", id, "error", herr)
		case did:
			healed++
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
		healed, failed, err := h.Sweep(ctx)
		if err != nil {
			logging.Warn("Reliant key sweep failed", "error", err)
			return
		}
		logging.Info("Reliant key sweep finished", "healed", healed, "failed", failed)
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
// control-plane URL and internal-service secret) and, when sweepEvery > 0,
// starts the background sweep. It is a no-op otherwise: a self-hosted reliant
// has no control-plane to mint from.
func InstallReliantKeyHealer(ctx context.Context, repo *db.Repo, baseURL, internalSecret string, sweepEvery time.Duration) {
	if baseURL == "" || internalSecret == "" {
		return
	}
	minter := accesstokenclient.New(accesstokenclient.Deps{
		BaseURL: baseURL,
		Sign:    func() (string, error) { return auth.SignInternalServiceToken(internalSecret) },
	})
	h := NewReliantKeyHealer(repo, minter)
	SetReliantKeyHealer(h)
	if sweepEvery > 0 {
		go RunReliantKeySweeps(ctx, h, sweepEvery)
	}
}
