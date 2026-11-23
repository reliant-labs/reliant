package webhook

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/prrecords"
	"github.com/reliant-labs/reliant/internal/store"
)

func TestDeliveryInboxFeedsPRRecordsOnce(t *testing.T) {
	f := newReceiptFixture(t)
	f.relay.WithDeliveryConsumer(PRRecordConsumer(prrecords.New(prrecords.Deps{
		Store: store.NewPrRecordExtStore(f.Repo.ORM()), Scope: prrecords.NewScope([]string{"acme/app"})})))
	body := gitHubFixture(t, "pull_request.labeled")

	require.Equal(t, http.StatusOK, f.github("pull_request", "pr-delivery-1", body).Code)
	// A worker applies the delivery, then dies before settling it.
	rows, err := NewStores(f.Repo).ClaimTriggerDeliveries(t.Context(), f.now, f.now.Add(time.Minute), "dead-worker", 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	_, err = f.relay.process(t.Context(), rows[0])
	require.NoError(t, err)
	f.count(t, "SELECT count(*) FROM pr_records WHERE number = 7 AND labels ? 'agent:ready'", 1)
	f.count(t, "SELECT count(*) FROM pr_record_deliveries", 1)
	var updated time.Time
	require.NoError(t, f.SQL.QueryRowContext(t.Context(), "SELECT observed_at FROM pr_records WHERE number = 7").Scan(&updated))

	// The lease expires and the delivery is replayed: it applies nothing.
	f.now = f.now.Add(2 * time.Minute)
	f.drain(t, 1)
	// GitHub redelivers it: the inbox dedupes the receipt.
	require.Equal(t, http.StatusOK, f.github("pull_request", "pr-delivery-1", body).Code)
	f.drain(t, 0)

	f.count(t, "SELECT count(*) FROM pr_records", 1)
	f.count(t, "SELECT count(*) FROM pr_record_deliveries", 1)
	var after time.Time
	require.NoError(t, f.SQL.QueryRowContext(t.Context(), "SELECT observed_at FROM pr_records WHERE number = 7").Scan(&after))
	require.True(t, updated.Equal(after), "replay rewrote the record: %v -> %v", updated, after)
}
