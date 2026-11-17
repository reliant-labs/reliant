// Package debug wraps a Delve debugger so forge can drive a debugging
// session from the CLI layer.
//
// The package exposes two complementary surfaces:
//
//   - [DelveDebugger] — the rich per-session control surface (breakpoints,
//     execution control, inspection, lifecycle). One implementation today
//     (Delve via rpc2), so callers hold the concrete *DelveDebugger rather
//     than a single-impl interface. Constructed via [NewDelveDebugger];
//     callers hold the handle for the duration of a session.
//
//   - [Service] — the package's behavioral seam: load, save, and clear
//     the persisted SessionInfo on disk. Tests intercept the on-disk
//     session file by injecting a mock Service.
//
// The data carriers (SessionInfo, StopState, Variable, BreakpointInfo,
// StackFrame, GoroutineInfo) remain plain types — they are not behavior
// to mock.
package debug

// Service is the behavioral surface of the debug package.
//
// Session helpers (Load/Save/Clear) hang off this interface so tests can
// intercept the on-disk session file without touching the filesystem.
type Service interface {
	// LoadSession reads .forge/debug-session.json from dir. Returns
	// (nil, nil) when no session file exists.
	LoadSession(dir string) (*SessionInfo, error)

	// SaveSession writes session to dir/.forge/debug-session.json.
	SaveSession(dir string, session *SessionInfo) error

	// ClearSession removes the session file from dir.
	ClearSession(dir string) error
}

// Deps is the dependency set for the debug Service. Empty today — Delve
// is invoked via process exec / rpc2 and needs no injected collaborators.
type Deps struct{}

// New constructs a debug.Service.
//
// forge:no-observe
// Pure compute: empty Deps; reads and writes the local debug-session
// file. No injected collaborator to trace through.
func New(_ Deps) Service { return &svc{} }

type svc struct{}

// LoadSession delegates to the package-level loader.
func (s *svc) LoadSession(dir string) (*SessionInfo, error) { return loadSession(dir) }

// SaveSession delegates to the package-level writer.
func (s *svc) SaveSession(dir string, session *SessionInfo) error {
	return saveSession(dir, session)
}

// ClearSession delegates to the package-level deleter.
func (s *svc) ClearSession(dir string) error { return clearSession(dir) }
