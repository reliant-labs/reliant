// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/gtm"
)

// allowGate lets every actor into GTM.
type allowGate struct{}

func (allowGate) Allowed(context.Context, featuregate.Feature, string, string) (bool, error) {
	return true, nil
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// service is gtm.Service over a, as internal/app composes it.
func service(a gtm.Analytics) gtm.Service {
	return gtm.New(gtm.Deps{Gate: allowGate{}, Clock: fixedClock{to}, Analytics: a})
}

var agent = gtm.Actor{UserID: "user-1", OrgID: tenantZero, Kind: gtm.ActorAgent}

// TestFixedErrorsSurviveTheService: ReadProductAnalytics passes this
// package's fixed errors through to the caller (a *gtm.ValidationError,
// ErrAnalyticsUnavailable or ErrAnalyticsFailed) with their text intact, and
// flattens anything else to ErrAnalyticsFailed.
func TestFixedErrorsSurviveTheService(t *testing.T) {
	q := gtm.AnalyticsQuery{Template: gtm.AnalyticsEventCounts}
	for _, want := range []error{
		errNoOrg, errInvalidName, errIdentifierProperty, errDuplicateStep, errWindowTooLong,
		errCredentialRefused, errNotSetUp, errQuotaExhausted, errTimedOut, errQueryRefused, errBadResponse,
		gtm.ErrAnalyticsFailed, gtm.ErrAnalyticsUnavailable,
	} {
		t.Run(want.Error(), func(t *testing.T) {
			stub := &gtm.MockAnalytics{QueryFunc: func(context.Context, string, gtm.AnalyticsQuery) (gtm.AnalyticsResult, error) {
				return gtm.AnalyticsResult{}, want
			}}
			_, err := service(stub).ReadProductAnalytics(context.Background(), agent, q)
			if !errors.Is(err, want) || err.Error() != want.Error() {
				t.Fatalf("err = %v, want %v", err, want)
			}
		})
	}
	// A cause that is none of them is flattened, its text dropped.
	stub := &gtm.MockAnalytics{QueryFunc: func(context.Context, string, gtm.AnalyticsQuery) (gtm.AnalyticsResult, error) {
		return gtm.AnalyticsResult{}, errors.New("clickhouse at ch-0.internal said no")
	}}
	if _, err := service(stub).ReadProductAnalytics(context.Background(), agent, q); err != gtm.ErrAnalyticsFailed {
		t.Fatalf("err = %v, want ErrAnalyticsFailed", err)
	}
}

// TestRealClientErrorsSurviveTheService runs the real client under the
// Service for the refusals the domain's own Validate lets through to it (the
// domain refuses a bad name, a long window and a repeated funnel step
// itself, and an actor with no org never gets past its gate).
func TestRealClientErrorsSurviveTheService(t *testing.T) {
	for name, tc := range map[string]struct {
		q    gtm.AnalyticsQuery
		want error
	}{
		"identifier property":        {gtm.AnalyticsQuery{Template: gtm.AnalyticsPropertyBreakdown, Events: []string{"page_viewed"}, Property: "customerid"}, errIdentifierProperty},
		"identifier property, camel": {gtm.AnalyticsQuery{Template: gtm.AnalyticsPropertyBreakdown, Events: []string{"page_viewed"}, Property: "contactEmail"}, errIdentifierProperty},
	} {
		t.Run(name, func(t *testing.T) {
			cp := &fakeCP{}
			_, err := service(newTestClient(cp, testToken, nil)).ReadProductAnalytics(context.Background(), agent, tc.q)
			if err != tc.want || err.Error() != tc.want.Error() {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(cp.reqs) != 0 {
				t.Fatal("called the control plane")
			}
		})
	}
	t.Run("control-plane refusal", func(t *testing.T) {
		cp := &fakeCP{err: errors.New("dial tcp 10.0.0.7:443: refused")}
		_, err := service(newTestClient(cp, testToken, nil)).ReadProductAnalytics(context.Background(), agent, sqlQueries["event_counts"])
		if err != gtm.ErrAnalyticsFailed {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no token", func(t *testing.T) {
		_, err := service(newTestClient(&fakeCP{}, "", nil)).ReadProductAnalytics(context.Background(), agent, sqlQueries["event_counts"])
		if err != gtm.ErrAnalyticsUnavailable {
			t.Fatalf("err = %v", err)
		}
	})
}
