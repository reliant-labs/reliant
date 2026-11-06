package readguard

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/wirecheck"
)

// What a client reads when the guard refuses a list read: ResourceExhausted,
// the guard's own sentence and a whole-second Retry-After (never under one).
func TestRefusalWire(t *testing.T) {
	for name, tc := range map[string]struct {
		retryAfter time.Duration
		secs       string
	}{
		"rounded to seconds": {retryAfter: 2400 * time.Millisecond, secs: "2"},
		"at least one":       {retryAfter: time.Millisecond, secs: "1"},
	} {
		t.Run(name, func(t *testing.T) {
			g := New(Deps{Limits: Limits{PerUser: 1, RetryAfter: tc.retryAfter}}).(*guard)
			err := g.refuse(context.Background(), "u1", Read[pb.ListChatsResponse]("list"), "queue full")
			wirecheck.Assert(t, err, wirecheck.Want{
				Code:    connect.CodeResourceExhausted,
				Message: "too many list requests at once; retry shortly",
				Meta:    map[string]string{"Retry-After": tc.secs},
			})
		})
	}
}

// A panic in a shared read answers Internal with the generic sentence; the
// panic value never reaches the client.
func TestPanicWire(t *testing.T) {
	g := New(Deps{Limits: Limits{PerUser: 1}}).(*guard)
	_, err := g.do(context.Background(), "user", "key", Read[pb.ListChatsResponse]("list"), func(context.Context) (connect.AnyResponse, error) {
		panic("secret handler detail")
	})
	require.Error(t, err)
	wirecheck.Assert(t, err, wirecheck.Want{Code: connect.CodeInternal, Message: "internal server error"})
}
