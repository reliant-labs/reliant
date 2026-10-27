package osutil

import "context"

type cancelProofKey struct{}

// WithCancellationUncertainty records failed subprocess shutdown proofs in the
// owning daemon execution. It does not turn a sent signal into an exit proof.
func WithCancellationUncertainty(ctx context.Context, record func(error)) context.Context {
	return context.WithValue(ctx, cancelProofKey{}, record)
}

// ReportCancellationUncertainty is called before a command handler completes.
func ReportCancellationUncertainty(ctx context.Context, err error) {
	if err == nil {
		return
	}
	if record, ok := ctx.Value(cancelProofKey{}).(func(error)); ok {
		record(err)
	}
}
