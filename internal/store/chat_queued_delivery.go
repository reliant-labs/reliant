package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// chat_queued_deliveries records what a chat's queued message waits for and
// how often delivering it has been tried (forge:no-entity: these queries are
// its only reads and writes). chats.queued_for_machine_at says THAT a message
// is queued; this row says for which machine, and bounds the retries.

// listChatsQueuedForMachineSQL lists chats holding a message queued for a
// machine, oldest first, with the delivery record when there is one. An empty
// user id lists every user's (the delivery sweep); otherwise one user's (a
// machine of theirs just connected). Archived chats are left alone. A chat
// delivery stopped retrying has its queued marker cleared and its record
// parked (gave_up_at); it is listed only when ?2 asks, and only once its hold
// (next_attempt_at) has passed. Queued chats come first, so a backlog of
// parked ones cannot crowd them out of a batch.
const listChatsQueuedForMachineSQL = `
SELECT c.id, c.user_id, c.active_daemon_id,
       COALESCE(c.queued_for_machine_at, d.gave_up_at) AS queued_for_machine_at,
       COALESCE(d.queued_since, c.queued_for_machine_at) AS queued_since,
       COALESCE(d.daemon_id, '') AS daemon_id,
       COALESCE(d.daemon_name, '') AS daemon_name,
       COALESCE(d.daemon_type, '') AS daemon_type,
       COALESCE(d.daemon_labels, '') AS daemon_labels,
       COALESCE(d.attempts, 0) AS attempts,
       d.next_attempt_at,
       COALESCE(d.last_failure, '') AS last_failure,
       d.gave_up_at
FROM chats c
LEFT JOIN chat_queued_deliveries d ON d.chat_id = c.id
WHERE (c.queued_for_machine_at IS NOT NULL
       OR (?2::boolean AND d.gave_up_at IS NOT NULL AND (d.next_attempt_at IS NULL OR d.next_attempt_at <= NOW())))
  AND c.state IS DISTINCT FROM 3
  AND (?0::text = '' OR c.user_id = ?0::text)
ORDER BY (c.queued_for_machine_at IS NULL) ASC, COALESCE(c.queued_for_machine_at, d.gave_up_at) ASC, c.id ASC
LIMIT ?1`

// ListChatsQueuedForMachine lists up to limit chats holding a message queued
// for a machine; includeGivenUp adds the ones delivery stopped retrying.
func ListChatsQueuedForMachine(ctx context.Context, db orm.Context, userID string, limit int, includeGivenUp bool) ([]core.QueuedForMachineChat, error) {
	var rows []struct {
		ID                 string     `bun:"id"`
		UserID             string     `bun:"user_id"`
		ActiveDaemonID     *string    `bun:"active_daemon_id"`
		QueuedForMachineAt time.Time  `bun:"queued_for_machine_at"`
		QueuedSince        time.Time  `bun:"queued_since"`
		DaemonID           string     `bun:"daemon_id"`
		DaemonName         string     `bun:"daemon_name"`
		DaemonType         string     `bun:"daemon_type"`
		DaemonLabels       string     `bun:"daemon_labels"`
		Attempts           int        `bun:"attempts"`
		NextAttemptAt      *time.Time `bun:"next_attempt_at"`
		LastFailure        string     `bun:"last_failure"`
		GaveUpAt           *time.Time `bun:"gave_up_at"`
	}
	if err := db.Bun().NewRaw(listChatsQueuedForMachineSQL, userID, limit, includeGivenUp).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list chats queued for their machine: %w", err)
	}
	chats := make([]core.QueuedForMachineChat, len(rows))
	for i, row := range rows {
		target := core.QueuedMachineTarget{DaemonID: row.DaemonID, DaemonName: row.DaemonName, DaemonType: row.DaemonType}
		if row.DaemonLabels != "" {
			// A record this code wrote always parses; one that does not is
			// read as carrying no labels rather than failing the listing.
			_ = json.Unmarshal([]byte(row.DaemonLabels), &target.Labels)
		}
		chats[i] = core.QueuedForMachineChat{
			ChatID:         row.ID,
			UserID:         row.UserID,
			ActiveDaemonID: row.ActiveDaemonID,
			QueuedAt:       row.QueuedForMachineAt,
			Since:          row.QueuedSince,
			Target:         target,
			Attempts:       row.Attempts,
			NextAttemptAt:  row.NextAttemptAt,
			LastFailure:    row.LastFailure,
			GaveUpAt:       row.GaveUpAt,
		}
	}
	return chats, nil
}

// recordChatQueuedDeliverySQL notes the machine a run waited on and why it
// failed. The attempt count survives: it is the streak delivery is bounding.
// A record untouched for the streak window belongs to an earlier outage, so
// it starts over. The chat is queued again by the caller in the same
// transaction, so a parked record is un-parked (its hold, next_attempt_at,
// stays: the sweep gives up again at once if the attempts are spent).
const recordChatQueuedDeliverySQL = `
INSERT INTO chat_queued_deliveries (chat_id, daemon_id, daemon_name, daemon_type, daemon_labels, last_failure)
VALUES (?0, ?1, ?2, ?3, ?4, ?5)
ON CONFLICT (chat_id) DO UPDATE SET
    daemon_id = EXCLUDED.daemon_id,
    daemon_name = EXCLUDED.daemon_name,
    daemon_type = EXCLUDED.daemon_type,
    daemon_labels = EXCLUDED.daemon_labels,
    last_failure = EXCLUDED.last_failure,
    attempts = CASE WHEN chat_queued_deliveries.updated_at < NOW() - make_interval(secs => ?6::float8) THEN 0 ELSE chat_queued_deliveries.attempts END,
    queued_since = CASE WHEN chat_queued_deliveries.updated_at < NOW() - make_interval(secs => ?6::float8) THEN NOW() ELSE chat_queued_deliveries.queued_since END,
    next_attempt_at = CASE WHEN chat_queued_deliveries.updated_at < NOW() - make_interval(secs => ?6::float8) THEN NULL ELSE chat_queued_deliveries.next_attempt_at END,
    gave_up_at = NULL,
    updated_at = NOW()`

// maxFailureText bounds the stored failure: it is read on every listing.
const maxFailureText = 500

// truncateOnRune cuts s to at most max bytes without splitting a character.
func truncateOnRune(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// RecordChatQueuedDelivery records the machine target a chat's queued message
// waits for, and why the run that queued it gave up on it.
func RecordChatQueuedDelivery(ctx context.Context, db orm.Context, chatID string, target core.QueuedMachineTarget, failure string) error {
	labels := ""
	if len(target.Labels) > 0 {
		b, err := json.Marshal(target.Labels)
		if err != nil {
			return fmt.Errorf("failed to encode queued delivery labels: %w", err)
		}
		labels = string(b)
	}
	failure = truncateOnRune(failure, maxFailureText)
	if _, err := db.Bun().NewRaw(recordChatQueuedDeliverySQL, chatID, target.DaemonID, target.DaemonName, target.DaemonType, labels, failure,
		core.QueuedDeliveryStreakWindow.Seconds()).Exec(ctx); err != nil {
		return fmt.Errorf("failed to record queued delivery target: %w", err)
	}
	return nil
}

// ClearChatQueuedDelivery forgets a chat's delivery record.
func ClearChatQueuedDelivery(ctx context.Context, db orm.Context, chatID string) error {
	if _, err := db.Bun().NewRaw(`DELETE FROM chat_queued_deliveries WHERE chat_id = ?0`, chatID).Exec(ctx); err != nil {
		return fmt.Errorf("failed to clear queued delivery record: %w", err)
	}
	return nil
}

// recordQueuedDeliveryAttemptSQL counts one run delivery started and holds the
// next until nextAttemptAt. It also creates the record for a message queued
// before records existed — but only while the chat still holds a queued
// message: the owner's send settles (deletes) the record and clears the marker
// in one transaction, and an attempt that was in flight must not bring the
// record back.
const recordQueuedDeliveryAttemptSQL = `
INSERT INTO chat_queued_deliveries (chat_id, attempts, next_attempt_at)
SELECT ?0, 1, ?1
WHERE EXISTS (SELECT 1 FROM chats WHERE id = ?0 AND queued_for_machine_at IS NOT NULL)
ON CONFLICT (chat_id) DO UPDATE SET
    attempts = chat_queued_deliveries.attempts + 1,
    next_attempt_at = EXCLUDED.next_attempt_at,
    updated_at = NOW()`

// RecordQueuedDeliveryAttempt counts a delivery run started for the chat and
// holds the sweep off the next one until nextAttemptAt. A chat that is no
// longer queued is not counted.
func RecordQueuedDeliveryAttempt(ctx context.Context, db orm.Context, chatID string, nextAttemptAt time.Time) error {
	if _, err := db.Bun().NewRaw(recordQueuedDeliveryAttemptSQL, chatID, nextAttemptAt).Exec(ctx); err != nil {
		return fmt.Errorf("failed to record queued delivery attempt: %w", err)
	}
	return nil
}

// giveUpQueuedDeliverySQL parks the record: delivery stopped retrying. It is an
// UPDATE, never an insert, and it applies only to a record that has spent its
// attempts (?1) on a chat that still holds a queued message. So a give-up that
// loses a race with the owner's send — which deletes the record and clears the
// marker — matches nothing, instead of recreating a parked record that would
// swallow the owner's next message. The row changes only while gave_up_at is
// unset, so of two replicas exactly one sees it change and posts the notice.
// It is held for ?2 seconds (QueuedDeliveryGiveUpHold): the sweep revives it
// only after that, and only if the machine connected since.
const giveUpQueuedDeliverySQL = `
UPDATE chat_queued_deliveries SET
    gave_up_at = NOW(),
    next_attempt_at = NOW() + make_interval(secs => ?2::float8),
    updated_at = NOW()
WHERE chat_id = ?0
  AND gave_up_at IS NULL
  AND attempts >= ?1
  AND EXISTS (SELECT 1 FROM chats WHERE id = ?0 AND queued_for_machine_at IS NOT NULL)`

// GiveUpQueuedDelivery parks the chat's delivery record if it has spent
// maxAttempts on a chat that is still queued, and reports whether it changed.
func GiveUpQueuedDelivery(ctx context.Context, db orm.Context, chatID string, maxAttempts int) (bool, error) {
	res, err := db.Bun().NewRaw(giveUpQueuedDeliverySQL, chatID, maxAttempts, core.QueuedDeliveryGiveUpHold.Seconds()).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to give up queued delivery: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// reviveQueuedDeliverySQL un-parks a record for ONE more attempt: attempts is
// left one short of the limit. A machine connecting is news worth a try, but
// not a fresh budget, or a machine that connects and drops every few seconds
// would put the loop back at the rate of its reconnects. Reviving is the
// claim: of two callers exactly one sees the row change.
const reviveQueuedDeliverySQL = `
UPDATE chat_queued_deliveries SET
    attempts = GREATEST(?1::int - 1, 0), next_attempt_at = NULL, gave_up_at = NULL, updated_at = NOW()
WHERE chat_id = ?0 AND gave_up_at IS NOT NULL`

// ReviveQueuedDelivery un-parks the chat's delivery record for one more
// attempt and reports whether it was parked.
func ReviveQueuedDelivery(ctx context.Context, db orm.Context, chatID string) (bool, error) {
	res, err := db.Bun().NewRaw(reviveQueuedDeliverySQL, chatID, core.MaxQueuedDeliveryAttempts).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to revive queued delivery: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
