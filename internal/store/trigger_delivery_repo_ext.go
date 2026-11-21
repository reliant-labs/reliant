package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// InsertTriggerDelivery retains the first authenticated delivery for a key.
// An inline receipt may be born leased, so a worker cannot race its caller.
func InsertTriggerDelivery(ctx context.Context, db orm.Context, d core.TriggerDelivery) (bool, error) {
	return deliveryChanged(db.Bun().NewRaw(`INSERT INTO trigger_delivery_inbox
		(id, source, provider, scope_key, delivery_key, received_at, payload, available_at, lease_token, lease_until)
		VALUES (?, ?, ?, ?, ?, ?, ?::jsonb, ?, NULLIF(?, ''), ?)
		ON CONFLICT (source, provider, scope_key, delivery_key) DO NOTHING`,
		d.ID, d.Source, d.Provider, d.ScopeKey, d.DeliveryKey, d.ReceivedAt,
		string(d.Payload), d.ReceivedAt, d.LeaseToken, deliveryTime(d.LeaseUntil)).Exec(ctx))
}

func deliveryTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func deliveryChanged(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// FindTriggerDelivery returns only receipt metadata; HTTP responses never read the stored body.
func FindTriggerDelivery(ctx context.Context, db orm.Context, key core.TriggerDelivery) (*core.TriggerDelivery, error) {
	var d core.TriggerDelivery
	err := db.Bun().NewRaw(`SELECT id, completed_at, dead_at, failure_code, event_id, outcome
		FROM trigger_delivery_inbox WHERE source = ? AND provider = ? AND scope_key = ? AND delivery_key = ?`,
		key.Source, key.Provider, key.ScopeKey, key.DeliveryKey).
		Scan(ctx, &d.ID, &d.CompletedAt, &d.DeadAt, &d.FailureCode, &d.EventID, &d.Outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &d, err
}

// ClaimTriggerDeliveries leases only ready rows. Lease tokens fence settlement
// from an expired worker; a crash leaves the row eligible for another worker.
func ClaimTriggerDeliveries(ctx context.Context, db orm.Context, now, until time.Time, token string, limit int) ([]core.TriggerDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	type row struct {
		ID, Source, Provider, ScopeKey, DeliveryKey, LeaseToken, EventID string
		ReceivedAt, LeaseUntil                                           time.Time
		Payload                                                          []byte
		Failures                                                         int
	}
	var rows []row
	err := db.Bun().NewRaw(`WITH ready AS (
		SELECT id FROM trigger_delivery_inbox
		WHERE completed_at IS NULL AND dead_at IS NULL AND available_at <= ?
		  AND (lease_until IS NULL OR lease_until <= ?)
		ORDER BY available_at, received_at, id LIMIT ? FOR UPDATE SKIP LOCKED
	)
	UPDATE trigger_delivery_inbox AS d SET lease_token = ?, lease_until = ?
	FROM ready WHERE d.id = ready.id
	RETURNING d.id, d.source, d.provider, d.scope_key, d.delivery_key, d.received_at,
		d.payload, d.lease_token, d.lease_until, d.failures, d.event_id`, now, now, limit, token, until).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	out := make([]core.TriggerDelivery, 0, len(rows))
	for _, r := range rows {
		out = append(out, core.TriggerDelivery{ID: r.ID, Source: r.Source, Provider: r.Provider,
			ScopeKey: r.ScopeKey, DeliveryKey: r.DeliveryKey, ReceivedAt: r.ReceivedAt,
			Payload: r.Payload, LeaseToken: r.LeaseToken, LeaseUntil: r.LeaseUntil, Failures: r.Failures, EventID: r.EventID})
	}
	return out, nil
}

// RetryCallerNotifiedTriggerDelivery allows only a caller redelivery to
// process an inline failure. It must re-authenticate before reaching here.
func RetryCallerNotifiedTriggerDelivery(ctx context.Context, db orm.Context, d core.TriggerDelivery) (*core.TriggerDelivery, error) {
	var payload []byte
	err := db.Bun().NewRaw(`UPDATE trigger_delivery_inbox
		SET dead_at = NULL, failure_code = '', lease_token = ?, lease_until = ?
		WHERE id = ? AND dead_at IS NOT NULL AND failure_code = ?
		RETURNING received_at, payload`, d.LeaseToken, d.LeaseUntil, d.ID, core.TriggerDeliveryCallerNotified).
		Scan(ctx, &d.ReceivedAt, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	d.Payload = payload
	return &d, err
}

// SettleTriggerDelivery applies the result only to the active lease. A
// completed row retains just its receipt, never the delivery body.
func SettleTriggerDelivery(ctx context.Context, db orm.Context, id, token string, s core.TriggerDeliverySettlement) (bool, error) {
	const fence = ` WHERE id = ? AND lease_token = ? AND lease_until > ?`
	if s.FailureCode == "" {
		return deliveryChanged(db.Bun().NewRaw(`UPDATE trigger_delivery_inbox
			SET completed_at = ?, payload = NULL, lease_token = NULL, lease_until = NULL,
			failure_code = '', event_id = ?, outcome = ?`+fence,
			s.Now, s.EventID, s.Outcome, id, token, s.Now).Exec(ctx))
	}
	if s.Terminal {
		return deliveryChanged(db.Bun().NewRaw(`UPDATE trigger_delivery_inbox
			SET dead_at = ?, failure_code = ?, failures = failures + 1,
			lease_token = NULL, lease_until = NULL`+fence,
			s.Now, s.FailureCode, id, token, s.Now).Exec(ctx))
	}
	return deliveryChanged(db.Bun().NewRaw(`UPDATE trigger_delivery_inbox
		SET available_at = ?, failure_code = ?, failures = failures + 1,
		lease_token = NULL, lease_until = NULL`+fence,
		s.RetryAt, s.FailureCode, id, token, s.Now).Exec(ctx))
}

// RenewTriggerDelivery extends only a still-active lease.
func RenewTriggerDelivery(ctx context.Context, db orm.Context, id, token string, now, until time.Time) (bool, error) {
	return deliveryChanged(db.Bun().NewRaw(`UPDATE trigger_delivery_inbox SET lease_until = ?
		WHERE id = ? AND lease_token = ? AND lease_until > ?`, until, id, token, now).Exec(ctx))
}

// LoadDeliveryTriggerEvent reads a linked event only for its authenticated trigger owner.
func LoadDeliveryTriggerEvent(ctx context.Context, db orm.Context, id, triggerID, userID string) (*core.TriggerEvent, error) {
	return firstTriggerEvent(ctx, db, "load receipt's trigger event",
		orm.WhereEq(TriggerEventFieldID, id), orm.WhereEq(TriggerEventFieldTriggerID, triggerID), orm.WhereEq(TriggerEventFieldUserID, userID))
}

// LockTriggerDeliveryEvent guards the first event linked to a targeted receipt.
// Call in the transaction that records the trigger event, never across I/O.
func LockTriggerDeliveryEvent(ctx context.Context, db orm.Context, id, token string) (string, error) {
	var eventID string
	err := db.Bun().NewRaw(`SELECT event_id FROM trigger_delivery_inbox
		WHERE id = ? AND lease_token = ? AND lease_until > clock_timestamp() FOR UPDATE`, id, token).Scan(ctx, &eventID)
	return eventID, err
}

// LinkTriggerDeliveryEvent records the event identity under the row lock, so
// a replay cannot re-evaluate a changed custom dedupe rule into a second event.
func LinkTriggerDeliveryEvent(ctx context.Context, db orm.Context, id, token, eventID string) error {
	_, err := db.Bun().NewRaw(`UPDATE trigger_delivery_inbox SET event_id = ? WHERE id = ? AND lease_token = ?`, eventID, id, token).Exec(ctx)
	return err
}

// PruneTriggerDeliveries only removes terminal receipts: a pending delivery
// remains recoverable regardless of how long the worker was down.
func PruneTriggerDeliveries(ctx context.Context, db orm.Context, before time.Time) (int64, error) {
	res, err := db.Bun().NewRaw(`DELETE FROM trigger_delivery_inbox WHERE id IN (
		SELECT id FROM trigger_delivery_inbox WHERE completed_at < ? OR dead_at < ?
		LIMIT 1000 FOR UPDATE SKIP LOCKED)`, before, before).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
