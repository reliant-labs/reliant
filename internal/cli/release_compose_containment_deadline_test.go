package cli

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

type deadlineContainmentBindings struct {
	emptyBindings
	elapsed *time.Duration
}

func (b deadlineContainmentBindings) HistoryPage(ctx context.Context, _ string, _ historyQuery) (historyPage, error) {
	started := time.Now()
	<-ctx.Done()
	*b.elapsed = time.Since(started)
	return historyPage{}, ctx.Err()
}

func TestCompositionCheckoutContainmentStillRefusesMissingHistory(t *testing.T) {
	held := checkoutLedger{Promotions: []release.Promotion{retiredPromotion("prod", "v1.0.0", "missing")}}
	err := checkCheckoutLedgerImported(context.Background(), held, t.TempDir(), "prod", envLedger{Bindings: emptyBindings{}, Releases: emptyReleases{}})
	if !errors.Is(err, errLedgerNotImported) {
		t.Fatalf("mandatory archived history containment bypassed: %v", err)
	}
}

func TestCompositionCheckoutContainmentRemoteBudgetStartsAfterLocalWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(31 * time.Second)
		ctx, cancel := boundCompositionLookup(context.Background())
		defer cancel()
		var elapsed time.Duration
		held := checkoutLedger{Promotions: []release.Promotion{retiredPromotion("prod", "v1.0.0", "p1")}}
		err := checkCheckoutLedgerImported(ctx, held, "unused", "prod", envLedger{Bindings: deadlineContainmentBindings{elapsed: &elapsed}, Releases: emptyReleases{}})
		if !errors.Is(err, context.DeadlineExceeded) || elapsed != 30*time.Second {
			t.Fatalf("remote containment must get fresh bounded30s, elapsed=%s err=%v", elapsed, err)
		}
	})
}
