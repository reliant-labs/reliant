package httpaction

import "context"

// SecretScrubber removes a call's resolved secret values, and the forms they
// take when echoed, from text. *stepsecrets.Masker implements it.
type SecretScrubber interface {
	Mask(string) string
	MaskAny(any) any
}

type secretCallKey struct{}

// WithSecretCall marks the call as one whose request carries a workflow step's
// resolved secret values (in the URL, headers or body). The runner then holds
// it to the rule a saved credential gets: https only, and redirects and
// pagination pinned to the host the call started at. Anything that records the
// call (the write ledger) masks what it persists with s. A nil s marks nothing.
func WithSecretCall(ctx context.Context, s SecretScrubber) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, secretCallKey{}, s)
}

// SecretCall is the scrubber WithSecretCall stored, or nil for an ordinary call.
func SecretCall(ctx context.Context) SecretScrubber {
	s, _ := ctx.Value(secretCallKey{}).(SecretScrubber)
	return s
}
