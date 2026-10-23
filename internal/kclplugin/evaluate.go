package kclplugin

import "sync"

// evalMu serializes every KCL evaluation in this process. See Serialized.
var evalMu sync.Mutex

// Serialized runs fn — one call into the native KCL runtime — while holding
// the process-wide KCL evaluation lock. Every native evaluation forge makes
// (internal/kclrender.Run, internal/kcloptions' option discovery) goes
// through here, so no two ever overlap.
//
// # Why evaluations cannot overlap
//
// The KCL runtime reports a refusal — an `assert`, a schema `check:` — by
// panicking, and captures the panic through a PROCESS-GLOBAL panic hook that
// each evaluation swaps in for its own duration, recording into a
// THREAD-LOCAL slot that is never cleared (kcl crates/runner/src/runner.rs,
// FastRunner::run: take_hook → set_hook(record) → evaluate → set_hook(prev)).
// Two overlapping evaluations interleave those swaps: the one that finishes
// first restores whatever hook it found — possibly the default one — while
// the other is still running. The other's refusal then panics through the
// wrong hook, is printed to stderr as a bare Rust panic, and is never
// recorded. What that evaluation reports depends on the OS thread it ran on:
//
//   - no record on the thread: an empty refusal, or SUCCESS — a refused
//     render comes back as `{}`, a policy check that failed reads "passed";
//   - a stale record: ANOTHER evaluation's message, from a different file.
//
// All three were seen in CI as TestKCLModule_NegativeChecks failing a
// different fixture each run. `forge env status` renders every env
// concurrently, and build/up fan out too, so users were exposed to the same
// corruption. The kcl-go client is a single process-wide handle, so there is
// no per-evaluation instance to isolate instead; the lock is the fix until
// the runtime stops mutating global state per evaluation.
//
// # Reentrancy
//
// The lock is not reentrant, and kcl_plugin.forge callbacks (resolve_port,
// allocate_port, write_file, …) run INSIDE an evaluation. A callback must
// therefore never start another KCL evaluation — none does, and none may.
func Serialized[T any](fn func() (T, error)) (T, error) {
	// Install the plugin bridge before the native client can be initialized
	// by the evaluation fn is about to run, and refuse rather than call into
	// a runtime that failed to load (see install). See Register.
	if err := Ready(); err != nil {
		var zero T
		return zero, err
	}
	evalMu.Lock()
	defer evalMu.Unlock()
	return fn()
}
