// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/providerbackoff"
	"github.com/reliant-labs/reliant/internal/store"
)

// Provider backoff (deprecated forwarders). The per-thread markers live in
// internal/providerbackoff. These stay as forwarding wrappers so code that
// calls them through the Repo keeps compiling within v1.

// ProviderBackoff is one thread's provider backoff marker.
//
// Deprecated: use core.ProviderBackoff.
type ProviderBackoff = core.ProviderBackoff

// The reasons a workflow-level usage-limit wait writes.
//
// Deprecated: use core.ReasonUsageLimit and core.ReasonUsageLimitProbe.
const (
	ReasonUsageLimit      = core.ReasonUsageLimit
	ReasonUsageLimitProbe = core.ReasonUsageLimitProbe
)

func (r *Repo) providerBackoff() (providerbackoff.Service, error) {
	r.backoffOnce.Do(func() {
		r.backoffSvc, r.backoffErr = providerbackoff.NewInstrumented(providerbackoff.Deps{
			Markers:  store.NewProviderBackoffExtStore(r.ORM()),
			Tx:       r,
			Activity: r,
		})
	})
	return r.backoffSvc, r.backoffErr
}

// RecordProviderBackoff marks a thread as parked in provider backoff.
//
// Deprecated: use providerbackoff.Service.RecordProviderBackoff.
func (r *Repo) RecordProviderBackoff(ctx context.Context, chatID, threadID string, attempt, maxAttempts, statusCode int, reason string, delay time.Duration, now time.Time) error {
	svc, err := r.providerBackoff()
	if err != nil {
		return err
	}
	return svc.RecordProviderBackoff(ctx, chatID, threadID, attempt, maxAttempts, statusCode, reason, delay, now)
}

// ClearProviderBackoff releases a thread from provider backoff.
//
// Deprecated: use providerbackoff.Service.ClearProviderBackoff.
func (r *Repo) ClearProviderBackoff(ctx context.Context, threadID string, now time.Time) error {
	svc, err := r.providerBackoff()
	if err != nil {
		return err
	}
	return svc.ClearProviderBackoff(ctx, threadID, now)
}

// RecordUsageLimitWait marks a thread as parked until a usage limit resets.
//
// Deprecated: use providerbackoff.Service.RecordUsageLimitWait.
func (r *Repo) RecordUsageLimitWait(ctx context.Context, chatID, threadID, provider string, probing bool, resumeAt, now time.Time) error {
	svc, err := r.providerBackoff()
	if err != nil {
		return err
	}
	return svc.RecordUsageLimitWait(ctx, chatID, threadID, provider, probing, resumeAt, now)
}

// ClearUsageLimitWait releases a thread from a usage-limit wait.
//
// Deprecated: use providerbackoff.Service.ClearUsageLimitWait.
func (r *Repo) ClearUsageLimitWait(ctx context.Context, chatID, threadID string, now time.Time) error {
	svc, err := r.providerBackoff()
	if err != nil {
		return err
	}
	return svc.ClearUsageLimitWait(ctx, chatID, threadID, now)
}

// ProviderBackoffByChat returns every thread of a chat that has a backoff row.
//
// Deprecated: use providerbackoff.Service.ProviderBackoffByChat.
func (r *Repo) ProviderBackoffByChat(ctx context.Context, chatID string) (map[string]ProviderBackoff, error) {
	svc, err := r.providerBackoff()
	if err != nil {
		return nil, err
	}
	return svc.ProviderBackoffByChat(ctx, chatID)
}

// ChatsHeldOnUsageLimit returns the chats parked on a usage-limit wait since a time.
//
// Deprecated: use providerbackoff.Service.ChatsHeldOnUsageLimit.
func (r *Repo) ChatsHeldOnUsageLimit(ctx context.Context, chatIDs []string, since time.Time) ([]string, error) {
	svc, err := r.providerBackoff()
	if err != nil {
		return nil, err
	}
	return svc.ChatsHeldOnUsageLimit(ctx, chatIDs, since)
}

// ListCapacityWakeTargets returns the runs a capacity wake is sent to for a user.
//
// Deprecated: use providerbackoff.Service.ListCapacityWakeTargets.
func (r *Repo) ListCapacityWakeTargets(ctx context.Context, userID string) ([]string, error) {
	svc, err := r.providerBackoff()
	if err != nil {
		return nil, err
	}
	return svc.ListCapacityWakeTargets(ctx, userID)
}

// ListOrgCapacityWakeTargets is ListCapacityWakeTargets over every chat of an org.
//
// Deprecated: use providerbackoff.Service.ListOrgCapacityWakeTargets.
func (r *Repo) ListOrgCapacityWakeTargets(ctx context.Context, orgID string) ([]string, error) {
	svc, err := r.providerBackoff()
	if err != nil {
		return nil, err
	}
	return svc.ListOrgCapacityWakeTargets(ctx, orgID)
}
