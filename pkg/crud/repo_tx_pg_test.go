package crud

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Repo inside a context-carried transaction (orm RunTx).
//
// The generated delegates — db.GetInvoiceByID(ctx, s.deps.DB, id),
// db.UpdateInvoiceMasked(ctx, s.deps.DB, inv, fields) — are one-line
// forwards onto these Repo methods, so what holds here holds for them: handed
// the PLAIN client, every method runs inside the transaction ctx carries.

type txInvoice struct {
	bun.BaseModel `bun:"table:tx_invoices,alias:tx_invoices"`

	ID         string `bun:"id,pk"`
	TotalCents int64  `bun:"total_cents,notnull"`
	PaidCents  int64  `bun:"paid_cents,notnull"`
}

type txPayment struct {
	bun.BaseModel `bun:"table:tx_payments,alias:tx_payments"`

	ID          string `bun:"id,pk"`
	InvoiceID   string `bun:"invoice_id,notnull"`
	AmountCents int64  `bun:"amount_cents,notnull"`
}

func newTxRepoTestDB(t *testing.T) *orm.Client {
	t.Helper()
	db := newRepoTestDB(t).(*orm.Client)
	for _, stmt := range []string{
		`CREATE TABLE tx_invoices (id TEXT PRIMARY KEY, total_cents BIGINT NOT NULL, paid_cents BIGINT NOT NULL)`,
		`CREATE TABLE tx_payments (id TEXT PRIMARY KEY, invoice_id TEXT NOT NULL, amount_cents BIGINT NOT NULL)`,
	} {
		if _, err := db.Exec(context.Background(), stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db
}

func TestRunTx_RepoMethodsSeeUncommittedWrites(t *testing.T) {
	db := newTxRepoTestDB(t)
	invoices := NewRepo[txInvoice]()
	outside := context.Background()

	err := db.RunTx(outside, orm.Serializable, func(ctx context.Context) error {
		inv := &txInvoice{ID: "inv-1", TotalCents: 100}
		if err := invoices.Create(ctx, db, inv); err != nil {
			return err
		}
		got, err := invoices.Get(ctx, db, "inv-1")
		if err != nil {
			t.Fatalf("Get inside RunTx must see the uncommitted Create: %v", err)
		}
		got.PaidCents = 40
		if err := invoices.UpdateMasked(ctx, db, got, []string{"paid_cents"}); err != nil {
			t.Fatalf("UpdateMasked inside RunTx: %v", err)
		}
		listed, err := invoices.List(ctx, db)
		if err != nil || len(listed) != 1 || listed[0].PaidCents != 40 {
			t.Fatalf("List inside RunTx = %+v, %v; want the one uncommitted, updated row", listed, err)
		}
		if n, err := invoices.Count(ctx, db); err != nil || n != 1 {
			t.Fatalf("Count inside RunTx = %d, %v; want 1", n, err)
		}

		// The plain-ctx observer reads through the pool and sees nothing:
		// the writes above belong to the transaction.
		if _, err := invoices.Get(outside, db, "inv-1"); !errors.Is(err, orm.ErrNoRows) {
			t.Fatalf("outside observer: Get = %v, want ErrNoRows before commit", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunTx: %v", err)
	}
	got, err := invoices.Get(outside, db, "inv-1")
	if err != nil || got.PaidCents != 40 {
		t.Fatalf("after commit: %+v, %v; want paid_cents=40", got, err)
	}

	// Delete inside a transaction that rolls back leaves the row in place.
	_ = db.RunTx(outside, orm.Serializable, func(ctx context.Context) error {
		if err := invoices.Delete(ctx, db, "inv-1"); err != nil {
			t.Fatalf("Delete inside RunTx: %v", err)
		}
		return errors.New("roll back")
	})
	if _, err := invoices.Get(outside, db, "inv-1"); err != nil {
		t.Fatalf("rolled-back Delete removed the row: %v", err)
	}
}

var errExceedsBalance = svcerr.FailedPrecondition("payment exceeds the remaining balance")

// payRemaining is the read-check-write state transition a service writes:
// read the invoice, refuse if the payment exceeds what is owed, otherwise
// record the payment and the new paid total. No lock anywhere.
//
// bothRead holds each caller's FIRST attempt until both have read, which
// forces the race deterministically: two writers acting on the same stale
// balance.
func payRemaining(
	ctx context.Context, db orm.Context, invoices *Repo[txInvoice], payments *Repo[txPayment],
	paymentID string, amount int64, bothRead *sync.WaitGroup, attempts *atomic.Int32,
) error {
	attempt := attempts.Add(1)
	inv, err := invoices.Get(ctx, db, "inv")
	if err != nil {
		return err
	}
	if inv.TotalCents-inv.PaidCents < amount {
		return errExceedsBalance
	}
	if attempt == 1 {
		bothRead.Done()
		waitOrFail(bothRead)
	}
	inv.PaidCents += amount
	if err := invoices.UpdateMasked(ctx, db, inv, []string{"paid_cents"}); err != nil {
		return err
	}
	return payments.Create(ctx, db, &txPayment{ID: paymentID, InvoiceID: inv.ID, AmountCents: amount})
}

// waitOrFail waits on wg with a ceiling, so a regression that stops one
// writer from reaching the barrier fails the test instead of hanging it.
func waitOrFail(wg *sync.WaitGroup) {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		panic("barrier: the other writer never arrived")
	}
}

func seedInvoice(t *testing.T, db *orm.Client) {
	t.Helper()
	if err := NewRepo[txInvoice]().Create(context.Background(), db,
		&txInvoice{ID: "inv", TotalCents: 100}); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
}

// Two concurrent "pay the remaining balance" transactions: under
// orm.Serializable exactly one lands. The other is aborted by postgres
// (40001), retried by RunTx, re-reads the COMMITTED invoice, and takes the
// refusal branch.
func TestRunTx_ConcurrentReadCheckWrite_ExactlyOneEffectLands(t *testing.T) {
	db := newTxRepoTestDB(t)
	seedInvoice(t, db)
	invoices, payments := NewRepo[txInvoice](), NewRepo[txPayment]()

	var bothRead sync.WaitGroup
	bothRead.Add(2)
	attempts := [2]atomic.Int32{}
	errs := [2]error{}
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = db.RunTx(context.Background(), orm.Serializable, func(ctx context.Context) error {
				return payRemaining(ctx, db, invoices, payments,
					[]string{"pay-a", "pay-b"}[i], 100, &bothRead, &attempts[i])
			})
		}()
	}
	wg.Wait()

	succeeded, refused := 0, 0
	for i, err := range errs {
		switch {
		case err == nil:
			succeeded++
			if attempts[i].Load() != 1 {
				t.Errorf("winner ran %d attempts, want 1", attempts[i].Load())
			}
		case errors.Is(err, errExceedsBalance):
			refused++
			if attempts[i].Load() != 2 {
				t.Errorf("loser ran %d attempts, want 2 (aborted once, then saw the committed balance)", attempts[i].Load())
			}
		default:
			t.Errorf("writer %d: unexpected error %v", i, err)
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("succeeded=%d refused=%d, want exactly one of each", succeeded, refused)
	}

	n, err := payments.Count(context.Background(), db)
	if err != nil || n != 1 {
		t.Fatalf("payments recorded = %d (%v), want exactly 1", n, err)
	}
	inv, err := invoices.Get(context.Background(), db, "inv")
	if err != nil || inv.PaidCents != 100 {
		t.Fatalf("invoice = %+v (%v), want paid_cents=100", inv, err)
	}
}

// Premise: the SAME code under orm.ReadCommitted (one attempt) double-pays.
// Both writers act on the stale balance and both commit — a lost update on
// paid_cents and two payments for one balance. This is the race the
// hand-written SELECT … FOR UPDATE helpers existed to close, why the
// lock-free state-transition recipe is orm.Serializable, and why the level
// has no default: it was READ COMMITTED here before v1.0.0, through the
// same-looking call that meant SERIALIZABLE on RunTx.
func TestRunTransaction_Premise_ReadCommittedReadCheckWriteDoublePays(t *testing.T) {
	db := newTxRepoTestDB(t)
	seedInvoice(t, db)
	invoices, payments := NewRepo[txInvoice](), NewRepo[txPayment]()

	var bothRead sync.WaitGroup
	bothRead.Add(2)
	attempts := [2]atomic.Int32{}
	errs := [2]error{}
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			errs[i] = db.RunTransaction(ctx, orm.ReadCommitted, func(tx orm.Context) error {
				return payRemaining(ctx, tx, invoices, payments,
					[]string{"pay-a", "pay-b"}[i], 100, &bothRead, &attempts[i])
			})
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v — the premise is that READ COMMITTED lets both commit", i, err)
		}
	}
	n, err := payments.Count(context.Background(), db)
	if err != nil || n != 2 {
		t.Fatalf("payments = %d (%v); premise: both writers commit a payment", n, err)
	}
}
