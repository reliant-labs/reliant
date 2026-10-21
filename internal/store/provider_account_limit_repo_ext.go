// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ProviderAccountLimit. The generated provider_account_limit_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: providerAccountLimitRepo (a *crud.Repo[ProviderAccountLimit]) and the
//     package-level GetProviderAccountLimitByID / ListProviderAccountLimit / CreateProviderAccountLimit / UpdateProviderAccountLimit / DeleteProviderAccountLimit
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ProviderAccountLimit struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindProviderAccountLimitBy(ctx context.Context, db orm.Context, /* args */) ([]*ProviderAccountLimit, error) {
//		var rows []*ProviderAccountLimit
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveProviderAccountLimits(ctx context.Context, db orm.Context) ([]*ProviderAccountLimit, error) {
//		return ListProviderAccountLimit(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Provider account limit state (P5b lane L6): the latest rate-limit snapshot, the bench, and the
// usage-poll schedule of each account. Every write is one statement keyed on the account, and
// every insert copies user_id from provider_accounts, so a write for a deleted account lands
// nowhere.

// limitsNeverObserved stands in for "never observed" in the NOT NULL observed_at column.
var limitsNeverObserved = time.Unix(0, 0).UTC()

func limitStateFromRow(r *ProviderAccountLimit) (core.LimitState, error) {
	l := core.LimitState{
		AccountID: r.AccountID, UserID: r.UserID, Status: acctText(r.Status),
		RepresentativeWindow: acctText(r.RepresentativeWindow), Source: acctText(r.Source),
		ObservedAt: r.ObservedAt, LimitedUntil: r.LimitedUntil, LimitedWindow: acctText(r.LimitedWindow),
		LimitedProbes: int(r.LimitedProbes), UsageAPINextAt: r.UsageAPINextAt,
		UsageAPIFailures: int(r.UsageAPIFailures), UsageAPIError: acctText(r.UsageAPIError),
	}
	if err := json.Unmarshal(r.Windows, &l.Windows); err != nil {
		return core.LimitState{}, fmt.Errorf("decoding limit windows of account %s: %w", r.AccountID, err)
	}
	if r.LastTrafficAt != nil {
		l.LastTrafficAt = *r.LastTrafficAt
	}
	if !l.ObservedAt.After(limitsNeverObserved) {
		l.ObservedAt = time.Time{}
	}
	return l, nil
}

// ListUserAccountLimits returns the limit state of the user's accounts that have any, by account id.
func ListUserAccountLimits(ctx context.Context, db orm.Context, userID string) (map[string]core.LimitState, error) {
	rows, err := ListProviderAccountLimit(ctx, db, orm.WhereEq(ProviderAccountLimitFieldUserID, userID))
	if err != nil {
		return nil, err
	}
	out := make(map[string]core.LimitState, len(rows))
	for _, r := range rows {
		l, err := limitStateFromRow(r)
		if err != nil {
			return nil, err
		}
		out[l.AccountID] = l
	}
	return out, nil
}

// GetAccountLimitState returns one account's limit state, or (nil, nil) when it has none.
func GetAccountLimitState(ctx context.Context, db orm.Context, accountID string) (*core.LimitState, error) {
	row, err := GetProviderAccountLimitByID(ctx, db, accountID)
	if errors.Is(err, orm.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	l, err := limitStateFromRow(row)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// EnsureUsagePollScheduled schedules a usage poll at next only when none is scheduled and the
// account's provider is one of providers. It never moves an existing schedule and keeps the
// failure count. An absent account, or one without a limits row, is a no-op.
func EnsureUsagePollScheduled(ctx context.Context, db orm.Context, accountID string, next time.Time, providers []string, now time.Time) error {
	_, err := db.Bun().NewRaw(`UPDATE provider_account_limits l SET usage_api_next_at = ?1, updated_at = ?3
		FROM provider_accounts a
		WHERE l.account_id = ?0 AND a.id = l.account_id AND a.provider = ANY(?2::text[]) AND l.usage_api_next_at IS NULL`,
		accountID, next.UTC(), pgdialect.Array(providers), now.UTC()).Exec(ctx)
	return err
}

// ClaimUsageFetch records a usage-API call for the account at now and reports whether the
// caller may make it: false when another claim landed within floor. One statement, so replicas
// share the floor. An absent account claims nothing.
func ClaimUsageFetch(ctx context.Context, db orm.Context, accountID string, now time.Time, floor time.Duration) (bool, error) {
	res, err := db.Bun().NewRaw(`INSERT INTO provider_account_limits
			(account_id, user_id, status, windows, source, observed_at, usage_polled_at, updated_at)
		SELECT id, user_id, 'unknown', '[]'::jsonb, 'usage_api', ?1, ?2, ?2 FROM provider_accounts WHERE id = ?0
		ON CONFLICT (account_id) DO UPDATE SET usage_polled_at = EXCLUDED.usage_polled_at, updated_at = EXCLUDED.updated_at
		WHERE provider_account_limits.usage_polled_at IS NULL OR provider_account_limits.usage_polled_at <= ?3`,
		accountID, limitsNeverObserved, now.UTC(), now.Add(-floor).UTC()).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// UpsertAccountLimitSnapshot stores snap's windows when snap is newer than the stored state
// (monotonic in observed_at, so processes never regress it). An "allowed" snapshot lifts the
// bench and clears the probe count; a "headers" one also stamps last_traffic_at. It never
// touches the poll schedule. An empty status is "unknown" and an empty source "headers". A
// snapshot for a deleted account inserts nothing.
func UpsertAccountLimitSnapshot(ctx context.Context, db orm.Context, snap core.LimitState, now time.Time) error {
	ws := snap.Windows
	if ws == nil {
		ws = []core.LimitWindow{}
	}
	windows, err := json.Marshal(ws)
	if err != nil {
		return err
	}
	status := snap.Status
	if status == "" {
		status = "unknown"
	}
	source := snap.Source
	if source == "" {
		source = "headers"
	}
	_, err = db.Bun().NewRaw(`INSERT INTO provider_account_limits
			(account_id, user_id, status, windows, representative_window, source, observed_at, last_traffic_at, updated_at)
		SELECT id, user_id, ?2, ?3::jsonb, ?4, ?5::text, ?6, CASE WHEN ?5::text = 'headers' THEN ?6::timestamptz END, ?7
		FROM provider_accounts WHERE id = ?0 AND user_id = ?1
		ON CONFLICT (account_id) DO UPDATE SET
			last_traffic_at = CASE WHEN EXCLUDED.source = 'headers' THEN EXCLUDED.observed_at ELSE provider_account_limits.last_traffic_at END,
			status = EXCLUDED.status,
			windows = EXCLUDED.windows,
			representative_window = EXCLUDED.representative_window,
			source = EXCLUDED.source,
			observed_at = EXCLUDED.observed_at,
			limited_until = CASE WHEN EXCLUDED.status = 'allowed' THEN NULL ELSE provider_account_limits.limited_until END,
			limited_window = CASE WHEN EXCLUDED.status = 'allowed' THEN NULL ELSE provider_account_limits.limited_window END,
			limited_probes = CASE WHEN EXCLUDED.status = 'allowed' THEN 0 ELSE provider_account_limits.limited_probes END,
			updated_at = EXCLUDED.updated_at
		WHERE provider_account_limits.observed_at < EXCLUDED.observed_at`,
		snap.AccountID, snap.UserID, status, string(windows), acctNullText(snap.RepresentativeWindow), source,
		snap.ObservedAt.UTC(), now.UTC()).Exec(ctx)
	return err
}

// BenchProviderAccount refuses one of the user's accounts until until (GREATEST with a bench
// already set) for window. probe marks an unknown-reset bench and counts it; otherwise the
// count resets. It reports whether the account exists.
func BenchProviderAccount(ctx context.Context, db orm.Context, userID, accountID string, until time.Time, window string, probe bool, now time.Time) (bool, error) {
	probes := 0
	if probe {
		probes = 1
	}
	res, err := db.Bun().NewRaw(`INSERT INTO provider_account_limits
			(account_id, user_id, status, windows, source, observed_at, limited_until, limited_window, limited_probes, updated_at)
		SELECT id, user_id, 'rejected', '[]'::jsonb, 'usage_limit_error', ?2, ?3, ?4, ?5, ?2
		FROM provider_accounts WHERE id = ?0 AND user_id = ?1
		ON CONFLICT (account_id) DO UPDATE SET
			status = 'rejected',
			source = 'usage_limit_error',
			observed_at = GREATEST(provider_account_limits.observed_at, EXCLUDED.observed_at),
			limited_window = CASE WHEN EXCLUDED.limited_until >= COALESCE(provider_account_limits.limited_until, '-infinity'::timestamptz)
				THEN EXCLUDED.limited_window ELSE provider_account_limits.limited_window END,
			limited_until = GREATEST(provider_account_limits.limited_until, EXCLUDED.limited_until),
			limited_probes = CASE WHEN ?6 THEN provider_account_limits.limited_probes + 1 ELSE 0 END,
			updated_at = EXCLUDED.updated_at`,
		accountID, userID, now.UTC(), until.UTC(), acctNullText(window), orm.Arg(probes), probe).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// LeaseUsagePolls takes up to limit accounts whose poll is due at now and pushes each one's
// schedule to now+lease, so concurrent leasers get disjoint sets. It returns their ids.
func LeaseUsagePolls(ctx context.Context, db orm.Context, now time.Time, limit int, lease time.Duration) ([]string, error) {
	var ids []string
	err := db.Bun().NewRaw(`UPDATE provider_account_limits SET usage_api_next_at = ?1
		WHERE account_id IN (
			SELECT account_id FROM provider_account_limits
			WHERE usage_api_next_at IS NOT NULL AND usage_api_next_at <= ?0
			ORDER BY usage_api_next_at LIMIT ?2 FOR UPDATE SKIP LOCKED)
		RETURNING account_id`, now.UTC(), now.Add(lease).UTC(), orm.Arg(limit)).Scan(ctx, &ids)
	return ids, err
}

// ScheduleUsagePoll sets the account's next poll (zero = unscheduled), failure count and last
// error, creating its limits row when it has none. It reports whether the account exists.
func ScheduleUsagePoll(ctx context.Context, db orm.Context, accountID string, next time.Time, failures int, errText string, now time.Time) (bool, error) {
	res, err := db.Bun().NewRaw(`INSERT INTO provider_account_limits
			(account_id, user_id, status, windows, source, observed_at, usage_api_next_at, usage_api_failures, usage_api_error, updated_at)
		SELECT id, user_id, 'unknown', '[]'::jsonb, 'usage_api', ?1, ?2, ?3, ?4, ?5 FROM provider_accounts WHERE id = ?0
		ON CONFLICT (account_id) DO UPDATE SET
			usage_api_next_at = EXCLUDED.usage_api_next_at,
			usage_api_failures = EXCLUDED.usage_api_failures,
			usage_api_error = EXCLUDED.usage_api_error,
			updated_at = EXCLUDED.updated_at`,
		accountID, limitsNeverObserved, acctNullTime(next), orm.Arg(failures), acctNullText(errText), now.UTC()).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ProviderAccountLimitExtra carries fields on ProviderAccountLimit that are NOT columns — computed
// values, transient state, anything derived rather than stored. ProviderAccountLimit is
// embedded on ProviderAccountLimitExtra (see provider_account_limit_orm.go), so a field added here is a field on
// every ProviderAccountLimit value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ProviderAccountLimitExtra struct {
}
