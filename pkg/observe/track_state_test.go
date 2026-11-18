package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/reliant-labs/forge/pkg/authn"
)

func TestTrackStateMarksTheEvent(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(authn.ContextWithClaims(context.Background(), &authn.Claims{UserID: "u-1"}),
		slog.New(slog.NewJSONHandler(&buf, nil)))
	TrackState(ctx, "billing_subscription_state", slog.String("plan", "pro"), slog.String(PropKind, "action"))
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("bad log line %q: %v", buf.String(), err)
	}
	want := map[string]any{
		"event.name": "product.billing_subscription_state", "user.id": "u-1",
		"product.plan": "pro", "product." + PropKind: KindState,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v (line %v)", k, m[k], v, m)
		}
	}
}

func TestStateAttrsKeepsTheMarkerWithinTheBound(t *testing.T) {
	attrs := make([]slog.Attr, 0, MaxProductProps+1)
	for i := range MaxProductProps + 1 {
		attrs = append(attrs, slog.Int(fmt.Sprintf("p%02d", i), i))
	}
	got := StateAttrs(attrs)
	if got[0].Key != PropKind || got[0].Value.String() != KindState {
		t.Fatalf("StateAttrs()[0] = %v, want the %s marker first", got[0], PropKind)
	}
	if len(got) != len(attrs)+1 {
		t.Fatalf("StateAttrs kept %d attrs, want %d", len(got), len(attrs)+1)
	}
	if len(attrs) != MaxProductProps+1 || attrs[0].Key != "p00" {
		t.Fatal("StateAttrs modified its input")
	}
}

// logLine is the one JSON log record emit wrote, decoded.
func logLine(t *testing.T, emit func(ctx context.Context)) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	emit(WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil))))
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("bad log line %q: %v", buf.String(), err)
	}
	return m
}

func TestTrackStateAsNamesTheUserAndMarksTheEvent(t *testing.T) {
	m := logLine(t, func(ctx context.Context) {
		TrackStateAs(ctx, "staff-1", "internal_user", slog.String("reason", "email_domain"))
	})
	want := map[string]any{
		"event.name": "product.internal_user", "user.id": "staff-1",
		"product.reason": "email_domain", "product." + PropKind: KindState,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v (line %v)", k, m[k], v, m)
		}
	}
}

// Under WithStateEvents every product event is marked, the ones the caller
// does not write itself (TrackAuth: the login broker's sign-ins) included, and
// a full set of props never pushes the marker out.
func TestWithStateEventsMarksEveryEventRecordedUnderIt(t *testing.T) {
	full := make([]slog.Attr, 0, MaxProductProps)
	for i := range MaxProductProps {
		full = append(full, slog.Int(fmt.Sprintf("p%02d", i), i))
	}
	for name, emit := range map[string]func(ctx context.Context){
		"Track": func(ctx context.Context) {
			Track(authn.ContextWithClaims(ctx, &authn.Claims{UserID: "u-1"}), "project_created", full...)
		},
		"TrackAuth": func(ctx context.Context) {
			TrackAuth(ctx, AuthSignedIn, "canary-subject", WithKind(AuthKindCredential), WithMethod(AuthMethodPassword))
		},
	} {
		t.Run(name, func(t *testing.T) {
			if m := logLine(t, emit); m["product."+PropKind] != nil {
				t.Errorf("unmarked context: %s = %v, want no marker", PropKind, m["product."+PropKind])
			}
			m := logLine(t, func(ctx context.Context) { emit(WithStateEvents(ctx)) })
			if m["product."+PropKind] != KindState {
				t.Errorf("%s = %v, want %q (line %v)", PropKind, m["product."+PropKind], KindState, m)
			}
		})
	}
	if StateEventsFrom(context.Background()) || !StateEventsFrom(WithStateEvents(context.Background())) {
		t.Error("StateEventsFrom does not report WithStateEvents")
	}
}

// The marker is written before the MaxProductProps bound is applied to the
// flattened props, so a group whose leaves alone fill the bound cannot push it
// out, and a caller prop that flattens to forge.kind (here inside a group)
// cannot override it.
func TestWithStateEventsKeepsTheMarkerAheadOfGroupedProps(t *testing.T) {
	leaves := make([]any, 0, MaxProductProps+1)
	for i := range MaxProductProps {
		leaves = append(leaves, slog.Int(fmt.Sprintf("p%02d", i), i))
	}
	grouped := []slog.Attr{
		slog.Group("forge", slog.String("kind", "action")),
		slog.Group("g", leaves...),
	}
	for name, emit := range map[string]func(ctx context.Context){
		"Track": func(ctx context.Context) {
			Track(WithStateEvents(authn.ContextWithClaims(ctx, &authn.Claims{UserID: "u-1"})), "project_created", grouped...)
		},
		"TrackState": func(ctx context.Context) {
			TrackState(authn.ContextWithClaims(ctx, &authn.Claims{UserID: "u-1"}), "billing_subscription_state", grouped...)
		},
		"TrackStateAs": func(ctx context.Context) {
			TrackStateAs(ctx, "staff-1", "internal_user", grouped...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := logLine(t, emit)
			if m["product."+PropKind] != KindState {
				t.Errorf("%s = %v, want %q (line %v)", PropKind, m["product."+PropKind], KindState, m)
			}
			props := 0
			for k := range m {
				if len(k) > len(AttrPropPrefix) && k[:len(AttrPropPrefix)] == AttrPropPrefix {
					props++
				}
			}
			if props != MaxProductProps {
				t.Errorf("kept %d props, want %d (the marker plus %d leaves)", props, MaxProductProps, MaxProductProps-1)
			}
			if m["product.g.p30"] == nil || m["product.g.p31"] != nil {
				t.Errorf("bound applied wrongly: g.p30 = %v, g.p31 = %v", m["product.g.p30"], m["product.g.p31"])
			}
		})
	}
}
