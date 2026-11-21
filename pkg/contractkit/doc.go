// Package contractkit provides runtime helpers used by the per-package
// mock_gen.go file forge generates from contract.go.
//
// # Scope
//
// contractkit's surface is the mock-side helpers (Recorder + MockNotSet)
// only. Per-call logging, tracing and metrics live in forge/pkg/observe
// (observe.LogCall, observe.TraceCall, observe.NewCallMetrics), and the
// Connect interceptors there are usually the right level of granularity.
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
package contractkit
