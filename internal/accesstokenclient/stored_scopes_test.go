package accesstokenclient

import (
	"context"
	"errors"
	"sync"
	"testing"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// introspectScopes introspects a token whose control plane reports scopes
// (an explicit list unless all).
func introspectScopes(t *testing.T, scopes string, all bool) (*fat.Principal, error) {
	t.Helper()
	srv := fakeCP(t, func(string, map[string]any) (int, string) {
		return 200, `{"active":true,"tokenId":"tok-1","orgId":"org-1","actingUserId":"user-1",
			"scopes":` + scopes + `,"allScopes":` + map[bool]string{true: "true", false: "false"}[all] + `}`
	})
	defer srv.Close()
	return New(Deps{BaseURL: srv.URL, Sign: sign}).Introspect(context.Background(), liveToken)
}

// One known and one unknown scope: the token authenticates with ONLY the
// known one, and the skip is counted against the unknown scope's name.
func TestIntrospect_OneKnownOneUnknownIsUsableWithOnlyTheKnown(t *testing.T) {
	before := skippedCount(t, "future:scope")
	p, err := introspectScopes(t, `["deploy:read","future:scope"]`, false)
	if err != nil {
		t.Fatalf("a token with one known scope was refused: %v", err)
	}
	if len(p.Scopes) != 1 || !p.Scopes.Has(fat.ScopeDeployRead) {
		t.Fatalf("scopes = %s, want only deploy:read", p.Scopes)
	}
	if got := skippedCount(t, "future:scope") - before; got != 1 {
		t.Fatalf("%s{scope=future:scope} grew by %d, want 1", UnknownScopeSkippedMetric, got)
	}
}

// A token every entry of which is unknown (a scope, a bundle this binary has
// never heard of) is refused, never authenticated as nothing — wildcard or
// explicit.
func TestIntrospect_AllUnknownIsRefused(t *testing.T) {
	for _, all := range []bool{false, true} {
		before := skippedCount(t, "bundle:future")
		p, err := introspectScopes(t, `["future:scope","bundle:future"]`, all)
		if !errors.Is(err, ErrNoRecognisedScope) {
			t.Fatalf("allScopes=%v: err = %v (principal %+v), want ErrNoRecognisedScope", all, err, p)
		}
		if got := skippedCount(t, "bundle:future") - before; got != 1 {
			t.Fatalf("allScopes=%v: %s{scope=bundle:future} grew by %d, want 1", all, UnknownScopeSkippedMetric, got)
		}
	}
}

// A stored "bundle:admin" expands at read, and never to a sensitive scope:
// db:query, secret:write, token:write and the rest are held only by an
// entry naming them.
func TestResolveStoredScopes_BundleAdminNeverYieldsASensitiveScope(t *testing.T) {
	ctx := context.Background()
	scopes, err := ResolveStoredScopes(ctx, "tok-1", []string{fat.BundleRef(fat.BundleAdmin)})
	if err != nil {
		t.Fatalf("bundle:admin was refused: %v", err)
	}
	if !scopes.Has(fat.ScopeDeployWrite) || !scopes.Has(fat.ScopeSecretRead) {
		t.Fatalf("bundle:admin did not expand: %s", scopes)
	}
	for _, s := range append([]fat.Scope{fat.ScopeDBQuery, fat.ScopeSecretWrite, fat.ScopeTokenWrite},
		fat.ScopesOfClass(fat.ClassSensitive)...) {
		if scopes.Has(s) {
			t.Errorf("bundle:admin yielded the sensitive scope %s", s)
		}
	}

	// The same through an introspection, beside an explicit sensitive scope,
	// which is kept: only the bundle is barred from yielding one.
	p, err := introspectScopes(t, `["bundle:admin","db:query"]`, false)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if !p.Scopes.Has(fat.ScopeDBQuery) || p.Scopes.Has(fat.ScopeSecretWrite) || p.Scopes.Has(fat.ScopeTokenWrite) {
		t.Fatalf("scopes = %s, want db:query (named) and no other sensitive scope", p.Scopes)
	}
}

// skippedCount is the UnknownScopeSkippedMetric total for one scope so far
// in this process. Tests compare deltas: the counter is the process's.
func skippedCount(t *testing.T, scope string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := skippedReader().Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if m.Name != UnknownScopeSkippedMetric || !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value(attribute.Key("scope")); ok && v.AsString() == scope {
					n += dp.Value
				}
			}
		}
	}
	return n
}

// skippedReader is the reader of the process's one meter provider, installed
// on first use: otel's global delegates to the first provider set, and the
// counter is built once (unknownScopeSkippedOnce), so a second provider would
// never see it.
var skippedReader = sync.OnceValue(func() *sdkmetric.ManualReader {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	return reader
})
