package cli

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestCompositionRenderDoesNotSpendRemoteLookupBudget(t *testing.T) {
	previous := renderKCLRawHook
	t.Cleanup(func() { renderKCLRawHook = previous })
	synctest.Test(t, func(t *testing.T) {
		finishedRender := errors.New("render completed after remote lookup budget")
		renderKCLRawHook = func(ctx context.Context, _, _ string, _ ...string) ([]byte, error) {
			// synctest advances virtual time; this never sleeps thirty real seconds.
			time.Sleep(31 * time.Second)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, finishedRender
		}
		err := prepareComposition(context.Background(), &buildOptions{release: "v1.0.0", env: "prod"}, "", true, []string{"all"})
		if !errors.Is(err, finishedRender) {
			t.Fatalf("local render lost the caller context to the remote timeout: %v", err)
		}
	})
}

func TestCompositionRenderHonorsCallerCancellation(t *testing.T) {
	previous := renderKCLRawHook
	t.Cleanup(func() { renderKCLRawHook = previous })
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		renderKCLRawHook = func(renderCtx context.Context, _, _ string, _ ...string) ([]byte, error) {
			<-renderCtx.Done()
			return nil, renderCtx.Err()
		}
		err := prepareComposition(ctx, &buildOptions{release: "v1.0.0", env: "prod"}, "", true, []string{"all"})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("caller deadline must still cancel local render: %v", err)
		}
	})
}

func TestCompositionCheckoutDoesNotSpendRemoteLookupBudget(t *testing.T) {
	previousRender, previousCheckout := renderKCLRawHook, compositionCheckoutLedger
	t.Cleanup(func() { renderKCLRawHook, compositionCheckoutLedger = previousRender, previousCheckout })
	dir := newLedgerTestProject(t, "composition-local-deadline")
	t.Chdir(dir)
	renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) { return []byte(`{"items": []}`), nil }
	synctest.Test(t, func(t *testing.T) {
		finishedCheckout := errors.New("checkout completed after remote lookup budget")
		compositionCheckoutLedger = func(ctx context.Context, _, _ string) (checkoutLedger, error) {
			time.Sleep(31 * time.Second)
			if err := ctx.Err(); err != nil {
				return checkoutLedger{}, err
			}
			return checkoutLedger{}, finishedCheckout
		}
		err := prepareComposition(context.Background(), &buildOptions{release: "v1.0.0", env: "prod"}, "", true, []string{"all"})
		if !errors.Is(err, finishedCheckout) {
			t.Fatalf("local checkout consumed remote lookup budget: %v", err)
		}
	})
}

func TestCompositionCheckoutHonorsCallerCancellation(t *testing.T) {
	previousRender, previousCheckout := renderKCLRawHook, compositionCheckoutLedger
	t.Cleanup(func() { renderKCLRawHook, compositionCheckoutLedger = previousRender, previousCheckout })
	dir := newLedgerTestProject(t, "composition-local-cancel")
	t.Chdir(dir)
	renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) { return []byte(`{"items": []}`), nil }
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		compositionCheckoutLedger = func(ctx context.Context, _, _ string) (checkoutLedger, error) {
			<-ctx.Done()
			return checkoutLedger{}, ctx.Err()
		}
		err := prepareComposition(ctx, &buildOptions{release: "v1.0.0", env: "prod"}, "", true, []string{"all"})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("caller must cancel checkout: %v", err)
		}
	})
}

func TestCompositionRemoteLookupRemainsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Local work has already used more than the remote phase's budget.
		time.Sleep(time.Minute)
		lookup, cancel := boundCompositionLookup(context.Background())
		defer cancel()
		if err := lookup.Err(); err != nil {
			t.Fatalf("fresh remote budget already expired: %v", err)
		}
		started := time.Now()
		<-lookup.Done()
		if !errors.Is(lookup.Err(), context.DeadlineExceeded) || time.Since(started) != 30*time.Second {
			t.Fatalf("remote lookup must retain its thirty-second bound: elapsed=%s err=%v", time.Since(started), lookup.Err())
		}
	})
}
