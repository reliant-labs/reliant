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
