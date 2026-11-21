package contractkit

import (
	"sync"
	"testing"
	"time"
)

func TestRecorder_RecordAndCallCount(t *testing.T) {
	var r Recorder
	if got := r.CallCount("Send"); got != 0 {
		t.Errorf("CallCount on empty = %d, want 0", got)
	}
	r.Record("Send", "ctx", "to@example.com")
	r.Record("Send", "ctx", "other@example.com")
	r.Record("Close")

	if got := r.CallCount("Send"); got != 2 {
		t.Errorf("CallCount(Send) = %d, want 2", got)
	}
	if got := r.CallCount("Close"); got != 1 {
		t.Errorf("CallCount(Close) = %d, want 1", got)
	}
	if got := r.CallCount("Other"); got != 0 {
		t.Errorf("CallCount(Other) = %d, want 0", got)
	}
}

func TestRecorder_Calls_ReturnsArgsCopy(t *testing.T) {
	var r Recorder
	r.Record("Send", "to@example.com", 42)

	calls := r.Calls("Send")
	if len(calls) != 1 {
		t.Fatalf("Calls(Send) len = %d, want 1", len(calls))
	}
	if len(calls[0].Args) != 2 {
		t.Fatalf("Args len = %d, want 2", len(calls[0].Args))
	}
	if calls[0].Args[0] != "to@example.com" {
		t.Errorf("Args[0] = %v, want to@example.com", calls[0].Args[0])
	}
	if calls[0].Args[1] != 42 {
		t.Errorf("Args[1] = %v, want 42", calls[0].Args[1])
	}
	if calls[0].Time.IsZero() {
		t.Error("Call.Time should be non-zero")
	}

	// Mutating the returned slice must not affect future Calls().
	calls[0].Args[0] = "tampered"
	again := r.Calls("Send")
	if again[0].Args[0] != "to@example.com" {
		t.Errorf("internal copy was mutated: %v", again[0].Args[0])
	}
}

func TestRecorder_Reset(t *testing.T) {
	var r Recorder
	r.Record("Send")
	r.Reset()
	if got := r.CallCount("Send"); got != 0 {
		t.Errorf("CallCount after Reset = %d, want 0", got)
	}
	// Reset on already-empty is fine.
	r.Reset()
}

func TestRecorder_NilSafe(t *testing.T) {
	var r *Recorder // nil pointer
	r.Record("Send")
	if got := r.CallCount("Send"); got != 0 {
		t.Errorf("CallCount on nil = %d, want 0", got)
	}
	if got := r.Calls("Send"); got != nil {
		t.Errorf("Calls on nil = %v, want nil", got)
	}
	r.Reset()
}

func TestRecorder_ConcurrentRecord(t *testing.T) {
	var r Recorder
	var wg sync.WaitGroup
	const goroutines = 16
	const callsEach = 100
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < callsEach; j++ {
				r.Record("Hot", j)
			}
		}()
	}
	wg.Wait()
	if got := r.CallCount("Hot"); got != goroutines*callsEach {
		t.Errorf("CallCount(Hot) = %d, want %d", got, goroutines*callsEach)
	}
}

func TestRecorder_ArgsSliceReuseSafe(t *testing.T) {
	// Simulate a generated mock that builds a single args slice and
	// reuses it between calls (defensive against future changes).
	var r Recorder
	args := []any{"first"}
	r.Record("M", args...)
	args[0] = "second"
	r.Record("M", args...)

	got := r.Calls("M")
	if got[0].Args[0] != "first" {
		t.Errorf("first call's args mutated: %v", got[0].Args[0])
	}
	if got[1].Args[0] != "second" {
		t.Errorf("second call's args wrong: %v", got[1].Args[0])
	}
}

func TestRecorder_TimeMonotonic(t *testing.T) {
	var r Recorder
	r.Record("M")
	time.Sleep(time.Millisecond)
	r.Record("M")
	calls := r.Calls("M")
	if !calls[1].Time.After(calls[0].Time) {
		t.Errorf("call times not ordered: %v vs %v", calls[0].Time, calls[1].Time)
	}
}
