package recordshadow

import "context"

// Service has a method literally named Record — the name of the
// embedded contractkit.Recorder's call-capture method. The generated
// MockService.Record SHADOWS the promoted Recorder.Record, so the mock
// body must call m.Recorder.Record(...) explicitly; a bare
// m.Record(...) is a self-call that fails to compile (wrong arity) or,
// with a matching signature, recurses forever. Caught by the fixture
// corpus: a ledger package with `Record(ctx, entry)` broke the build
// of every generated mock.
type Service interface {
	Record(ctx context.Context, entry string) error
}
