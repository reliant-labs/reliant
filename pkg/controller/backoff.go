package controller

import (
	"math"
	"time"
)

// Backoff is a simple capped-exponential backoff helper. Reconcilers
// that track per-object retry counts (typically in CRD status) can use
// it to compute the next requeue delay without importing
// k8s.io/client-go/util/workqueue.
//
// The zero value is NOT useful; callers should set Initial, Max, and
// Factor explicitly. A typical configuration:
//
//	b := controller.Backoff{
//	    Initial: 1 * time.Second,
//	    Max:     5 * time.Minute,
//	    Factor:  2.0,
//	}
//	delay := b.Next(ws.Status.RetryCount)
type Backoff struct {
	// Initial is the delay for attempt 0. Required.
	Initial time.Duration

	// Max is the upper bound on the returned delay. Required.
	Max time.Duration

	// Factor is the multiplicative factor between attempts. Values
	// <= 1 collapse the backoff to Initial.
	Factor float64
}

// Next returns the backoff delay for `attempt` (0-indexed). attempt 0
// returns Initial; each subsequent attempt multiplies by Factor up to
// Max. Negative attempt is treated as 0.
func (b Backoff) Next(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if b.Factor <= 1 {
		if b.Initial > b.Max && b.Max > 0 {
			return b.Max
		}
		return b.Initial
	}
	// Compute Initial * Factor^attempt without integer overflow.
	mult := math.Pow(b.Factor, float64(attempt))
	if math.IsInf(mult, 1) || mult > float64(math.MaxInt64) {
		return b.Max
	}
	d := time.Duration(float64(b.Initial) * mult)
	if d > b.Max && b.Max > 0 {
		return b.Max
	}
	if d < 0 {
		return b.Max
	}
	return d
}
