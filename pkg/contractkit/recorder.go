package contractkit

import (
	"sync"
	"time"
)

// Call captures a single recorded invocation of a mock method.
//
// Args is a snapshot of the positional arguments passed to the mock at
// the moment Record was called. Time is the wall-clock time of the call
// (UTC monotonic via time.Now), useful for ordering assertions across
// methods.
type Call struct {
	Args []any
	Time time.Time
}

// Recorder is a thread-safe per-method call log embedded in every mock
// produced by forge's mock_gen.go. Tests use it to assert that a mock
// method was invoked the expected number of times with the expected
// arguments.
//
// The zero value is ready to use.
type Recorder struct {
	mu    sync.Mutex
	calls map[string][]Call
}

// Record appends a single call entry for the named method. args is
// stored by reference; callers should not mutate the slice's contents
// after the call returns. nil-safe — calling on a nil receiver is a
// no-op so generated shims can use a value receiver without risk.
func (r *Recorder) Record(method string, args ...any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == nil {
		r.calls = make(map[string][]Call)
	}
	// Copy args so callers reusing a slice between calls don't corrupt
	// the recorded history.
	argsCopy := append([]any(nil), args...)
	r.calls[method] = append(r.calls[method], Call{
		Args: argsCopy,
		Time: time.Now(),
	})
}

// Calls returns the ordered list of recorded calls for method. Returns
// nil if Record has never been called for that method. The returned
// slice (and each Call's Args) is a deep copy — callers may mutate it
// freely without affecting the recorder's state.
func (r *Recorder) Calls(method string) []Call {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	in := r.calls[method]
	if in == nil {
		return nil
	}
	out := make([]Call, len(in))
	for i, c := range in {
		argsCopy := append([]any(nil), c.Args...)
		out[i] = Call{Args: argsCopy, Time: c.Time}
	}
	return out
}

// CallCount returns the number of recorded calls for method. Returns 0
// if the method was never called. Cheap (no allocation).
func (r *Recorder) CallCount(method string) int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls[method])
}

// Reset clears the recorded call history for all methods. Useful in
// table-driven tests that share a single mock across rows.
func (r *Recorder) Reset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = nil
}
