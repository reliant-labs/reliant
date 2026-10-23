// Package contractkit provides runtime helpers used by the per-package
// mock_gen.go file forge generates from contract.go.
//
// # Scope
//
// As of the observe.* migration, contractkit's surface is the mock-side
// helpers (Recorder + MockNotSet) only. The middleware/tracing/metrics
// helpers (LogCall, TraceStart, Metrics, Record) historically lived
// here to support the per-method middleware_gen.go / tracing_gen.go /
// metrics_gen.go wrappers; those wrappers were removed in favour of
// Connect interceptors + opt-in helpers in forge/pkg/observe. The
// helpers themselves remain in this package for backward compatibility
// with any user code that imported them directly.
//
// # The mock side
//
//   - Recorder is embedded by every Mock<Iface> struct. Tests assert
//     against captured calls via m.CallCount(...) / m.Calls(...).
//   - MockNotSet returns the canonical "<MockName>.<Method>Func not set"
//     error string that fallthrough mock methods emit when the user
//     hasn't set the corresponding XxxFunc field. The format is locked
//     by TestMockNotSet_FingerprintLocked because dogfood projects
//     assert on the substring.
//
// # Where the other helpers went
//
// The non-mock helpers — LogCallErr / LogCall / TraceStart /
// RecordSpanError / NewMetrics / RecordCall / RecordDuration /
// RecordError — are still present here for compile-time stability.
// New code should prefer the equivalent in forge/pkg/observe:
//
//   - observe.LogCall replaces contractkit.LogCallErr / LogCall.
//   - observe.TraceCall replaces contractkit.TraceStart +
//     RecordSpanError.
//   - observe.NewCallMetrics + (*CallMetrics).RecordCall replaces
//     contractkit.NewMetrics + the trio of Record* methods.
//
// observe.* is also where the Connect interceptors live, which is
// usually the right level of granularity for new projects.
package contractkit
