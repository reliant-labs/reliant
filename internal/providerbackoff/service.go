// yours: scaffolded once, never touched again — forge will not overwrite this file
// Service implementation; canonical pattern in `forge skill load service-layer`.
package providerbackoff

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the providerbackoff package.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Markers holds the per-thread provider backoff rows.
	Markers Markers
	// Tx opens the transaction a usage-limit marker and its chat-activity
	// announcement are written in.
	Tx TxRunner
	// Activity announces a chat's current activity to its watchers.
	Activity ChatActivity
}

//forge:contract
type service struct {
	deps Deps
}

// New creates a new providerbackoff service instance.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.Markers == nil || deps.Tx == nil || deps.Activity == nil {
		return nil, errors.New("providerbackoff: Deps.Markers, Tx and Activity are required")
	}
	return &service{deps: deps}, nil
}

// RecordProviderBackoff marks a thread as parked in provider backoff and rolls
// the previous wait, if any, into the cumulative total.
//
// Called BEFORE the driver sleeps, which is the only ordering that makes the
// marker useful: one written afterwards cannot distinguish a run that is waiting
// right now from one that is working.
//
// The cumulative total charges only the portion of the previous wait that was
// actually slept, so a wait cut short by cancellation is not counted as if it
// had run to completion.
func (s *service) RecordProviderBackoff(ctx context.Context, chatID, threadID string, attempt, maxAttempts, statusCode int, reason string, delay time.Duration, now time.Time) error {
	if chatID == "" || threadID == "" {
		return fmt.Errorf("chat ID and thread ID are required to record provider backoff")
	}
	if err := s.deps.Markers.RecordProviderBackoff(ctx, chatID, threadID, attempt, maxAttempts, statusCode, reason, delay, now); err != nil {
		return fmt.Errorf("failed to record provider backoff: %w", err)
	}
	return nil
}

// ClearProviderBackoff releases a thread from provider backoff, charging the
// slept portion of the open wait to the cumulative total. The row is kept, not
// deleted: the cumulative columns are what `reliant-dev workflow analyze` reads
// to say where a finished run's time went.
//
// A no-op when the thread has no row, so the caller can clear unconditionally.
func (s *service) ClearProviderBackoff(ctx context.Context, threadID string, now time.Time) error {
	if threadID == "" {
		return fmt.Errorf("thread ID cannot be empty")
	}
	if err := s.deps.Markers.ClearProviderBackoff(ctx, threadID, now); err != nil {
		return fmt.Errorf("failed to clear provider backoff: %w", err)
	}
	return nil
}

// RecordUsageLimitWait marks a thread as parked until a provider's usage limit
// resets, and tells the chat's watchers when the thread is the chat's root run.
//
// Called by the workflow BEFORE it parks on its timer, for the same reason
// RecordProviderBackoff is called before a driver sleeps. probing is true when
// the provider named no reset and resumeAt is only the next rung of the probe
// ladder.
func (s *service) RecordUsageLimitWait(ctx context.Context, chatID, threadID, provider string, probing bool, resumeAt, now time.Time) error {
	if chatID == "" || threadID == "" {
		return fmt.Errorf("chat ID and thread ID are required to record a usage-limit wait")
	}
	reason := core.ReasonUsageLimit
	if probing {
		reason = core.ReasonUsageLimitProbe
	}
	return s.deps.Tx.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		if err := s.deps.Markers.RecordUsageLimitWait(txCtx, chatID, threadID, provider, reason, resumeAt, now); err != nil {
			return fmt.Errorf("failed to record usage-limit wait: %w", err)
		}
		return s.deps.Activity.EmitChatActivityIfChanged(txCtx, chatID)
	})
}

// ClearUsageLimitWait releases a thread from a usage-limit wait, charging the
// slept portion to the cumulative total. An empty threadID releases every
// thread of the chat, which is what a stop needs: a terminated run executes no
// code to clear its own markers. A no-op when nothing is waiting.
func (s *service) ClearUsageLimitWait(ctx context.Context, chatID, threadID string, now time.Time) error {
	if chatID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	return s.deps.Tx.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		n, err := s.deps.Markers.ClearUsageLimitWait(txCtx, chatID, threadID, now)
		if err != nil {
			return fmt.Errorf("failed to clear usage-limit wait: %w", err)
		}
		if n == 0 {
			return nil
		}
		return s.deps.Activity.EmitChatActivityIfChanged(txCtx, chatID)
	})
}

// ProviderBackoffByChat returns every thread of a chat that has a provider
// backoff row, keyed by thread id. Read-only: one SELECT.
func (s *service) ProviderBackoffByChat(ctx context.Context, chatID string) (map[string]core.ProviderBackoff, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	out, err := s.deps.Markers.ListChatProviderBackoff(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to read provider backoff: %w", err)
	}
	return out, nil
}

// ChatsHeldOnUsageLimit returns the chats among chatIDs with a thread parked
// on a usage-limit wait that began after since. Read-only: one SELECT.
func (s *service) ChatsHeldOnUsageLimit(ctx context.Context, chatIDs []string, since time.Time) ([]string, error) {
	if len(chatIDs) == 0 {
		return nil, nil
	}
	ids, err := s.deps.Markers.ListChatsHeldOnUsageLimit(ctx, chatIDs, since)
	if err != nil {
		return nil, fmt.Errorf("failed to list chats held on a usage limit: %w", err)
	}
	return ids, nil
}

// ListCapacityWakeTargets returns the root workflow ids of userID's chats whose
// live run may have a thread blocked on a provider: one parked on a usage
// limit, a paused root, or a stopped sub-agent. These are the runs a capacity
// wake is sent to when the user's providers gain capacity. Read-only.
func (s *service) ListCapacityWakeTargets(ctx context.Context, userID string) ([]string, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	ids, err := s.deps.Markers.ListCapacityWakeTargets(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list chats to wake: %w", err)
	}
	return ids, nil
}

// ListOrgCapacityWakeTargets is ListCapacityWakeTargets over every chat of
// orgID: the runs a capacity wake is sent to when the org's policy changes.
// Read-only.
func (s *service) ListOrgCapacityWakeTargets(ctx context.Context, orgID string) ([]string, error) {
	if orgID == "" {
		return nil, fmt.Errorf("org ID cannot be empty")
	}
	ids, err := s.deps.Markers.ListOrgCapacityWakeTargets(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to list the org's chats to wake: %w", err)
	}
	return ids, nil
}
