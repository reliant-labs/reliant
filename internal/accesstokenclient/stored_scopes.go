package accesstokenclient

import (
	"context"
	"sync"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/reliant-labs/reliant/internal/logging"
)

// UnknownScopeSkippedMetric counts the entries of a stored scope list this
// binary skipped because its forge catalog does not know them (an unknown
// scope, or an unknown "bundle:<name>" reference), by scope. It stays at zero
// while the writer of the list (control plane, or this server's own
// access_tokens rows) runs the same forge as this binary.
const UnknownScopeSkippedMetric = "reliant.accesstoken.unknown_scope_skipped"

// ResolveStoredScopes reads a token's STORED scope list — what control plane's
// introspection reports, or a row of reliant's own access_tokens — into the
// authority this binary can evaluate, through forge's accesstoken.ResolveStored:
// a "bundle:<name>" reference expands to its bundle (which never yields a
// sensitive scope), and an entry this binary's catalog does not know is
// SKIPPED, never a reason to refuse the token.
//
// ── WHY SKIP, WHEN THIS USED TO REFUSE ───────────────────────────────
//
// Until forge's scope bundles (forge#2759), one unknown entry in an explicit
// list refused the whole token, on the reasoning that a token should not
// authenticate with a subset of what it was minted for. That made a newer
// writer an outage for an older reader: a control plane on a newer forge (or a
// row a newer reliant wrote, read after a rollback) took every such token
// offline until this binary caught up, and a stored bundle reference, which
// release N+1 writes, would do the same. Skipping can only NARROW: nothing in
// this binary can exercise a scope it does not know, so the resolved set is
// always a subset of what the writer meant. Writes stay strict: a mint still
// goes through fat.NewSet, which refuses an unknown scope.
//
// Every skipped entry is logged with the token id and the entry itself (never
// the secret) and counted on UnknownScopeSkippedMetric, so a token quietly
// narrower than its holder believes is never invisible. A list that resolves
// to nothing is refused with ErrNoRecognisedScope: a token with no authority
// that authenticates would read as an outage, not a refusal.
func ResolveStoredScopes(ctx context.Context, tokenID string, raw []string) (fat.Set, error) {
	scopes, skipped := fat.ResolveStored(raw)
	for _, s := range skipped {
		logging.WarnContext(ctx, "accesstoken: skipped a stored scope this server does not know",
			"token_id", tokenID, "scope", s)
		unknownScopeSkipped().Add(ctx, 1, metric.WithAttributes(attribute.String("scope", s)))
	}
	if len(scopes) == 0 {
		return nil, ErrNoRecognisedScope
	}
	return scopes, nil
}

var (
	unknownScopeSkippedOnce sync.Once
	unknownScopeSkippedCtr  metric.Int64Counter
)

// unknownScopeSkipped is the UnknownScopeSkippedMetric counter, built on first
// use from the process's meter provider (otel's global delegates to the one
// observe.Setup installs, even when that runs later).
func unknownScopeSkipped() metric.Int64Counter {
	unknownScopeSkippedOnce.Do(func() {
		var err error
		unknownScopeSkippedCtr, err = otel.GetMeterProvider().Meter("github.com/reliant-labs/reliant/internal/accesstokenclient").
			Int64Counter(UnknownScopeSkippedMetric,
				metric.WithUnit("{scope}"),
				metric.WithDescription("Entries of a stored token scope list skipped because this server's forge catalog does not know them, by scope."))
		// An instrument the SDK refuses still comes back usable (a no-op).
		otel.Handle(err)
	})
	return unknownScopeSkippedCtr
}
