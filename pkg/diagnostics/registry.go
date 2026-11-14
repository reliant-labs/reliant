package diagnostics

import (
	"sort"
	"sync"
)

// registry.go — collection and lifecycle of Diagnostic records.
//
// The Registry is the bridge between codegen-emitted registration
// calls (in pkg/app/diagnostics_gen.go's init()) and the
// Bootstrap-time emit. It's intentionally tiny: codegen calls
// RegisterStub / RegisterNilDep at package init time, Bootstrap calls
// Boot once per process start.
//
// Thread-safety: Register* and Boot are mutex-guarded so a process
// that does multiple Bootstrap calls (rare — test harnesses) gets
// consistent results. The expected pattern is "register many at init,
// Boot once per process" so contention is effectively zero.

// Registry holds the set of diagnostics for one process.
type Registry struct {
	mu      sync.Mutex
	entries []Diagnostic
}

// NewRegistry returns a new empty Registry. Most callers use the
// process-wide registry returned by Default instead.
func NewRegistry() *Registry {
	return &Registry{}
}

// defaultRegistry backs Default. Unexported so no caller can replace the
// process-wide registry out from under code that already holds it.
var defaultRegistry = NewRegistry()

// Default returns the process-wide Registry — the one a registration
// producer targets and Bootstrap boots. Tests that want isolation can
// construct their own Registry via NewRegistry.
func Default() *Registry { return defaultRegistry }

// RegisterStub records a Tier-1 stub whose body is solely
// `return ..., ErrNotImplemented` (or the configured sentinel).
//
// Called from generated `diagnostics_gen.go::init()`. The symbol
// argument is the canonical `<pkg>.<Func>` identifier; file is
// project-relative; line is the 1-indexed line of the
// `// forge:gen unwired-stub` marker.
//
// Empty symbol is a no-op — codegen should never emit one, but the
// guard avoids a noisy log line if it does.
func (r *Registry) RegisterStub(symbol, file string, line int) {
	if symbol == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, Diagnostic{
		Kind:     KindStubImpl,
		Symbol:   symbol,
		File:     file,
		Line:     line,
		Message:  symbol + " is a forge-scaffolded stub returning ErrNotImplemented",
		Severity: SeverityWarn,
	})
}

// RegisterNilDep records a wire_gen.go DI site where a Deps field is
// constructed as nil (or typed zero) with no acknowledged-stub
// opt-out.
//
// Called from generated `diagnostics_gen.go::init()`. Component is
// the wire_gen function name (e.g. "wireWorkerCalibratorRefitDeps");
// depName is the Deps field; file is project-relative; line is the
// 1-indexed line of the `// TODO: wire <field>` marker.
//
// Empty component or depName is a no-op.
func (r *Registry) RegisterNilDep(component, depName, file string, line int) {
	if component == "" || depName == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, Diagnostic{
		Kind:      KindNilDep,
		Symbol:    component + "." + depName,
		File:      file,
		Line:      line,
		Component: component,
		DepName:   depName,
		Message:   component + " was wired with " + depName + "=nil; assign in pkg/app/setup.go or mark `// forge:stub-ok`",
		Severity:  SeverityWarn,
	})
}

// Boot emits every registered diagnostic through the supplied
// Emitter, then a single Summary call. Returns the full slice (in
// stable order) so callers — typically Bootstrap — can roll the data
// up into `forge project audit --json` without re-walking the registry.
//
// A nil Emitter is replaced by a NopEmitter; the slice is still
// returned so consumers that just want the data don't have to wire a
// logger.
//
// Boot is idempotent in the sense that repeated calls emit the same
// data; it does not clear entries. Tests that want isolation should
// use a per-test Registry rather than calling Boot on Default() twice.
func (r *Registry) Boot(e Emitter) []Diagnostic {
	if e == nil {
		e = NopEmitter{}
	}
	r.mu.Lock()
	// Copy under the lock so concurrent Register* calls during Boot
	// don't observe a half-iterated slice. Register* during Boot is
	// not a supported pattern but we don't want to crash if it
	// happens.
	out := make([]Diagnostic, len(r.entries))
	copy(out, r.entries)
	r.mu.Unlock()

	// Stable order: by Kind, then by Symbol. Keeps log output and
	// summary text deterministic across runs so log search and CI
	// snapshots don't churn.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Symbol < out[j].Symbol
	})

	for _, d := range out {
		e.Emit(d)
	}
	e.Summary(out)
	return out
}

// Len returns the number of registered diagnostics. Useful for tests
// and for the Bootstrap caller that wants to short-circuit when zero.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// Reset clears the registry. Test-only helper — production code does
// not call this. Exported so test packages outside diagnostics_test
// (such as integration suites that span multiple Bootstrap calls)
// can reset state between cases.
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = nil
}
