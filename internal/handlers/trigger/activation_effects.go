package trigger

import "context"

// Activation verification and the app's database mutation share one SERIALIZABLE
// snapshot. External projections must not escape a transaction that can abort.
// The queue is private to one transaction attempt; retries discard it.
type activationEffects struct{ pending []func(context.Context) error }
type activationEffectsKey struct{}

func deferActivationEffect(ctx context.Context, effect func(context.Context) error) bool {
	queue, _ := ctx.Value(activationEffectsKey{}).(*activationEffects)
	if queue == nil {
		return false
	}
	queue.pending = append(queue.pending, effect)
	return true
}
