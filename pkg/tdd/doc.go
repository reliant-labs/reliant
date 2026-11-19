// Package tdd is a generics-based table-driven test library for Forge
// projects. It collapses the per-RPC test boilerplate (table struct, range
// loop, error-code assertion, mock construction) into a small set of
// reusable helpers so that scaffolded test files can be tiny shims that
// declare cases and let the library carry the iteration.
//
// The library is dependency-light on purpose — it uses only the standard
// library plus connectrpc.com/connect (the same RPC dependency Forge
// projects already pull in). It does not introduce assertion frameworks,
// mock generators, or other runtime dependencies.
//
// # The four helpers
//
//   - [TableRPC] runs a slice of [Case] rows against a Connect handler
//     function and asserts on either a happy-path response or an expected
//     [connect.Code] error.
//
//   - [TableContract] runs a slice of [ContractCase] rows against a
//     contract.go-defined Service interface implementation. Each case
//     supplies a closure that invokes one method; the helper compares the
//     returned error and (optionally) the returned value.
//
//   - [E2EClient] takes an *httptest.Server and a typed-client factory
//     and returns a client wired to the server's URL, registering
//     t.Cleanup(srv.Close) for you.
//
//   - [NewMock] is a tiny option-based constructor for the Func-field
//     mocks Forge generates from contract.go (mock_gen.go).
//
// # Standalone helpers
//
//   - [AssertConnectError] asserts that an error is a Connect error with
//     the expected code, with a clear message on mismatch.
//
//   - [WithTimeout] returns a context.Context with the given deadline and
//     a cleanup func, suitable for table-row Setup hooks.
//
//   - [SetupMockDB] returns a real-postgres *sql.DB (pkg/pgtest),
//     registers cleanup, and is the same shape used by the
//     bootstrap_testing scaffold.
//
// See pkg/tdd/*_test.go for usage examples; the same patterns appear in
// the templates under internal/templates/{service,internal-package,test/e2e}.
package tdd
