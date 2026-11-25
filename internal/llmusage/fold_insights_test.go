package llmusage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/llmusage"
)

// FoldInsights works off a backlog a batch per transaction until a batch
// comes back short, model calls then tool calls, and stops on the first error
// with what it had folded.
func TestFoldInsightsDrainsEachLedgerInBatches(t *testing.T) {
	t.Parallel()
	models := []int64{1000, 1000, 7}
	tools := []int64{3}
	ledger := &llmusage.MockLedger{FoldLLMCallUsageIntoInsightsFunc: func(_ context.Context, batch int) (int64, error) {
		if batch != 1000 {
			t.Errorf("batch = %d, want 1000", batch)
		}
		n := models[0]
		models = models[1:]
		return n, nil
	}}
	toolLedger := &llmusage.MockToolLedger{FoldToolCallUsageIntoInsightsFunc: func(context.Context, int) (int64, error) {
		n := tools[0]
		tools = tools[1:]
		return n, nil
	}}
	tx := &llmusage.MockTxRunner{RunTxFunc: func(ctx context.Context, _ orm.TxPolicy, fn func(context.Context) error) error {
		return fn(ctx)
	}}
	svc, err := llmusage.New(llmusage.Deps{Ledger: ledger, Tools: toolLedger, Tx: tx})
	if err != nil {
		t.Fatal(err)
	}
	n, err := svc.FoldInsights(context.Background())
	if err != nil || n != 2010 {
		t.Fatalf("FoldInsights = %d, %v; want 2010 rows folded", n, err)
	}
	if got := tx.CallCount("RunTx"); got != 4 {
		t.Errorf("transactions = %d, want one per batch (4)", got)
	}

	boom := errors.New("boom")
	ledger.FoldLLMCallUsageIntoInsightsFunc = func(context.Context, int) (int64, error) { return 0, boom }
	if _, err := svc.FoldInsights(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("FoldInsights err = %v, want the ledger's", err)
	}
}
