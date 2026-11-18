package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/release"
)

// prepareCompositionCheckout performs only local, caller-cancellable reads.
func prepareCompositionCheckout(ctx context.Context, projectDir, env string) (checkoutLedger, release.Provenance, error) {
	held, err := compositionCheckoutLedger(ctx, projectDir, env)
	if err != nil {
		return checkoutLedger{}, release.Provenance{}, err
	}
	provenance, err := release.CaptureProvenance(ctx, projectDir, release.CaptureOptions{})
	// An unhashed tree is a dirty one, refused just below with the message
	// that says what to do about it.
	if err != nil && !errors.Is(err, release.ErrUnhashed) {
		return checkoutLedger{}, release.Provenance{}, err
	}
	if provenance.Dirty || provenance.Repo == "" || !release.ValidObjectID(provenance.Commit) {
		return checkoutLedger{}, release.Provenance{}, fmt.Errorf("snapshot composition requires a clean resolved source checkout")
	}
	return held, provenance, nil
}

// An empty checkout needs no remote containment query, as in ledgerForRendered.
func checkCompositionCheckoutImported(ctx context.Context, held checkoutLedger, projectDir, env string, ledger envLedger) error {
	if !held.any() {
		return nil
	}
	return checkCheckoutLedgerImported(ctx, held, projectDir, env, ledger)
}

// compositionCheckoutLedger is the local preparation seam. It never reads the
// remote ledger; containment is checked separately under the lookup deadline.
var compositionCheckoutLedger = func(ctx context.Context, projectDir, env string) (checkoutLedger, error) {
	if err := ctx.Err(); err != nil {
		return checkoutLedger{}, err
	}
	if !hasRetiredLedger(ctx, projectDir) {
		return checkoutLedger{}, ctx.Err()
	}
	held, err := checkoutLedgerFor(ctx, projectDir, env)
	if err != nil {
		return checkoutLedger{}, err
	}
	return held, ctx.Err()
}
