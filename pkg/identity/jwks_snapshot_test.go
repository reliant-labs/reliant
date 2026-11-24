package identity

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/auth"
)

// The delivered JWKS is forge/pkg/auth's snapshot document, so a validator
// handed it inline (JWT_JWKS_SNAPSHOT) can start while the issuer is down. An
// inline snapshot without delivered_at is unusable: nothing else dates it.
func TestOutputJWKSIsAUsableInlineSnapshot(t *testing.T) {
	f := newFake(t)
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	r, _ := newRec(t, f, func(o *Options) { o.Now = func() time.Time { return at } })
	rep, err := r.Reconcile(context.Background(), fullSpec())
	if err != nil {
		t.Fatal(err)
	}
	out := rep.Output

	var doc struct {
		Issuer      string            `json:"issuer"`
		DeliveredAt time.Time         `json:"delivered_at"`
		Keys        []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(out.JWKS, &doc); err != nil {
		t.Fatalf("JWKS is not JSON: %v\n%s", err, out.JWKS)
	}
	if doc.Issuer != out.Issuer || doc.Issuer == "" {
		t.Errorf("snapshot issuer = %q, want the output issuer %q", doc.Issuer, out.Issuer)
	}
	if !doc.DeliveredAt.Equal(at) {
		t.Errorf("delivered_at = %v, want the pass's clock %v", doc.DeliveredAt, at)
	}
	if len(doc.Keys) == 0 {
		t.Fatal("snapshot holds no keys")
	}

	// An issuer that refuses connections: the validator must start on the
	// inline snapshot alone.
	down := unreachableURL(t)
	if _, stop, err := auth.JWKSKeyfuncWithSnapshot(down, auth.JWKSSnapshot{Inline: string(out.JWKS), Issuer: out.Issuer}); err != nil {
		t.Fatalf("the delivered snapshot is not usable inline: %v", err)
	} else {
		stop()
	}
	// The raw JWKS the instance serves is not, which is the defect this pins.
	raw, _ := json.Marshal(map[string]any{"keys": doc.Keys})
	if _, stop, err := auth.JWKSKeyfuncWithSnapshot(down, auth.JWKSSnapshot{Inline: string(raw), Issuer: out.Issuer}); err == nil {
		stop()
		t.Error("an inline JWKS without delivered_at was accepted; the test no longer proves the stamp matters")
	}
	// A snapshot published for another issuer is refused.
	if _, stop, err := auth.JWKSKeyfuncWithSnapshot(down, auth.JWKSSnapshot{Inline: string(out.JWKS), Issuer: "https://elsewhere.example"}); err == nil {
		stop()
		t.Error("the snapshot was accepted for another issuer")
	}
}

func TestDryRunPublishesNoJWKS(t *testing.T) {
	f := newFake(t)
	r, _ := newRec(t, f, func(o *Options) { o.DryRun = true })
	rep, err := r.Reconcile(context.Background(), fullSpec())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Output.JWKS != nil {
		t.Errorf("a dry run published a JWKS snapshot: %s", rep.Output.JWKS)
	}
}

func unreachableURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return "http://" + addr + "/oauth/v2/keys"
}
