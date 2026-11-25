// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/store"
)

// Agents › Insights (dev-docs/design/agent-analytics.md §3.6, PR C1): the
// reads of the insights rollups and the audit of member drill-downs. Who may
// call each is decided by the handler (internal/handlers/insights) before it
// calls here.

// InsightsDim is a dimension an insights read groups by.
type InsightsDim = store.InsightsDim

// InsightsSource is model calls or tool calls.
type InsightsSource = store.InsightsSource

// The dimensions and sources an insights read takes (store.InsightsDim*).
const (
	InsightsDimDay      = store.InsightsDimDay
	InsightsDimProject  = store.InsightsDimProject
	InsightsDimWorkflow = store.InsightsDimWorkflow
	InsightsDimPreset   = store.InsightsDimPreset
	InsightsDimModel    = store.InsightsDimModel
	InsightsDimProvider = store.InsightsDimProvider
	InsightsDimPurpose  = store.InsightsDimPurpose
	InsightsDimTool     = store.InsightsDimTool
	InsightsDimToolKind = store.InsightsDimToolKind

	InsightsModelCalls = store.InsightsModelCalls
	InsightsToolCalls  = store.InsightsToolCalls

	// InsightsLatencyBuckets is the number of buckets of every histogram.
	InsightsLatencyBuckets = store.InsightsLatencyBuckets
)

// InsightsDimApplies reports whether src's calls can be grouped by dim.
func InsightsDimApplies(src InsightsSource, dim InsightsDim) bool {
	return store.InsightsDimApplies(src, dim)
}

// InsightsLatencyBucketBounds is the [lower, upper) range in ms of
// histogram bucket k; the last bucket's upper bound is +Inf.
func InsightsLatencyBucketBounds(k int) (lower, upper float64) {
	return store.InsightsLatencyBucketBounds(k)
}

// InsightsModelRow is one group's model calls; InsightsToolRow one group's
// tool calls.
type (
	InsightsModelRow    = store.InsightsModelRow
	InsightsToolRow     = store.InsightsToolRow
	InsightsContributor = store.InsightsContributor
)

// InsightsDrilldown is one recorded member drill-down.
type InsightsDrilldown = store.InsightsDrilldown

// InsightsDrilldownCursor is the position after a page of drill-downs.
type InsightsDrilldownCursor = store.InsightsDrilldownCursor

// ListInsightsTeamModelCalls is orgID's team model-call totals by dim over
// the UTC days from..to inclusive. It names no person.
func (r *Repo) ListInsightsTeamModelCalls(ctx context.Context, orgID string, from, to time.Time, dim InsightsDim) ([]InsightsModelRow, error) {
	rows, err := store.ListInsightsTeamModelCalls(ctx, r.ORM(), orgID, from, to, dim)
	if err != nil {
		return nil, fmt.Errorf("list insights team model calls: %w", err)
	}
	return rows, nil
}

// ListInsightsTeamToolCalls is orgID's team tool-call totals by dim.
func (r *Repo) ListInsightsTeamToolCalls(ctx context.Context, orgID string, from, to time.Time, dim InsightsDim) ([]InsightsToolRow, error) {
	rows, err := store.ListInsightsTeamToolCalls(ctx, r.ORM(), orgID, from, to, dim)
	if err != nil {
		return nil, fmt.Errorf("list insights team tool calls: %w", err)
	}
	return rows, nil
}

// ListInsightsContributors is, per group of dim, the opaque ordinals of the
// members who contributed to it: what the small-group floor counts.
func (r *Repo) ListInsightsContributors(ctx context.Context, src InsightsSource, orgID string, from, to time.Time, dim InsightsDim) ([]InsightsContributor, error) {
	rows, err := store.ListInsightsContributors(ctx, r.ORM(), src, orgID, from, to, dim)
	if err != nil {
		return nil, fmt.Errorf("list insights contributors: %w", err)
	}
	return rows, nil
}

// ListInsightsMemberModelCalls is userID's own model-call totals in orgID by
// dim. The caller decides who may read whose (the member themselves, or an
// org admin after recording the drill-down).
func (r *Repo) ListInsightsMemberModelCalls(ctx context.Context, orgID, userID string, from, to time.Time, dim InsightsDim) ([]InsightsModelRow, error) {
	rows, err := store.ListInsightsMemberModelCalls(ctx, r.ORM(), orgID, userID, from, to, dim)
	if err != nil {
		return nil, fmt.Errorf("list insights member model calls: %w", err)
	}
	return rows, nil
}

// ListInsightsMemberToolCalls is userID's own tool-call totals in orgID by dim.
func (r *Repo) ListInsightsMemberToolCalls(ctx context.Context, orgID, userID string, from, to time.Time, dim InsightsDim) ([]InsightsToolRow, error) {
	rows, err := store.ListInsightsMemberToolCalls(ctx, r.ORM(), orgID, userID, from, to, dim)
	if err != nil {
		return nil, fmt.Errorf("list insights member tool calls: %w", err)
	}
	return rows, nil
}

// RecordInsightsDrilldown records that actorUserID read subjectUserID's
// insights in orgID for the UTC days from..to, at at, and returns the
// record's id.
func (r *Repo) RecordInsightsDrilldown(ctx context.Context, orgID, actorUserID, subjectUserID string, from, to, at time.Time) (string, error) {
	row := &store.InsightsDrilldown{
		ID:            uuid.NewString(),
		OrgID:         orgID,
		ActorUserID:   actorUserID,
		SubjectUserID: subjectUserID,
		StartDate:     from.UTC(),
		EndDate:       to.UTC(),
		CreatedAt:     at.UTC(),
	}
	if err := store.CreateInsightsDrilldown(ctx, r.ORM(), row); err != nil {
		return "", fmt.Errorf("record insights drilldown: %w", err)
	}
	return row.ID, nil
}

// ListInsightsDrilldowns is a page of orgID's drill-down records, newest
// first; subjectUserID, when not empty, keeps only those into that member.
func (r *Repo) ListInsightsDrilldowns(ctx context.Context, orgID, subjectUserID string, after *InsightsDrilldownCursor, limit int) ([]*InsightsDrilldown, error) {
	rows, err := store.ListInsightsDrilldowns(ctx, r.ORM(), orgID, subjectUserID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list insights drilldowns: %w", err)
	}
	return rows, nil
}
