package release

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// The write path is strict: a typo in a --gate flag must fail before
// anything is recorded, and empty is a typo too (a check that reported no
// verdict has not reported).
func TestParseGateStatus_ClosedOnWrite(t *testing.T) {
	for _, raw := range []string{"", "ok", "PASSED", "pass", "success", "green", "unknown"} {
		if _, err := ParseGateStatus(raw); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseGateStatus(%q): want ErrInvalid, got %v", raw, err)
		}
	}
	for _, raw := range []string{"passed", "failed", "skipped", "error"} {
		got, err := ParseGateStatus(raw)
		if err != nil {
			t.Fatalf("ParseGateStatus(%q): %v", raw, err)
		}
		if string(got) != raw {
			t.Errorf("ParseGateStatus(%q) = %q", raw, got)
		}
	}
}

// The error message names the whole set, because a human fixing a typo
// should not have to find the source.
func TestParseGateStatus_ErrorNamesTheSet(t *testing.T) {
	_, err := ParseGateStatus("green")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"passed", "failed", "skipped", "error"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// The READ path is where history lives. A promotion recorded before the set
// closed carries free text; refusing to decode it would make old promotions
// unrenderable, and silently reading it as a pass would be worse. It maps to
// error and keeps the raw value.
func TestGate_UnrecognisedStoredStatus_ReadsAsErrorAndKeepsRaw(t *testing.T) {
	var g Gate
	if err := json.Unmarshal([]byte(`{"name":"lint","status":"all good"}`), &g); err != nil {
		t.Fatalf("an old ledger entry must still decode: %v", err)
	}
	if g.Status != GateStatusError {
		t.Errorf("status = %q, want %q", g.Status, GateStatusError)
	}
	if g.RawStatus != "all good" {
		t.Errorf("RawStatus = %q, want the stored value kept verbatim", g.RawStatus)
	}
	// Readable, but not writable: leniency on read must not become
	// leniency on write.
	if err := g.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("a gate carrying a raw status must not validate for writing, got %v", err)
	}
}

// A recognised status leaves RawStatus empty, so a non-empty RawStatus is
// exactly the signal "this entry predates the vocabulary".
func TestGate_RecognisedStatusLeavesRawEmpty(t *testing.T) {
	var g Gate
	if err := json.Unmarshal([]byte(`{"name":"test","status":"failed","summary":"3 failed"}`), &g); err != nil {
		t.Fatal(err)
	}
	if g.Status != GateStatusFailed || g.RawStatus != "" {
		t.Fatalf("got status=%q raw=%q, want failed with no raw", g.Status, g.RawStatus)
	}
	if err := g.Validate(); err != nil {
		t.Errorf("a failed gate is valid evidence — recording it is not an error: %v", err)
	}
}

// A bare GateStatus field is only ever decoded where a CALLER supplied one,
// so it is strict, unlike a whole stored Gate.
func TestGateStatus_UnmarshalIsStrict(t *testing.T) {
	var s GateStatus
	if err := json.Unmarshal([]byte(`"green"`), &s); !errors.Is(err, ErrInvalid) {
		t.Errorf("want ErrInvalid, got %v", err)
	}
	if err := json.Unmarshal([]byte(`"skipped"`), &s); err != nil || s != GateStatusSkipped {
		t.Errorf("got %q, %v", s, err)
	}
}

func TestGate_Validate(t *testing.T) {
	early := time.Unix(1700000000, 0).UTC()
	late := early.Add(time.Minute)
	cases := []struct {
		name string
		gate Gate
		ok   bool
	}{
		{"full", Gate{Name: "test", Status: GateStatusPassed, StartedAt: &early, FinishedAt: &late,
			Summary: "412 passed", URL: "https://ci", RunID: "github:a/b/1/1",
			Details: map[string]any{"passed": 412}}, true},
		{"skipped is a real verdict", Gate{Name: "smoke", Status: GateStatusSkipped}, true},
		{"no name", Gate{Status: GateStatusPassed}, false},
		{"blank name", Gate{Name: "   ", Status: GateStatusPassed}, false},
		{"no status", Gate{Name: "lint"}, false},
		{"status outside the set", Gate{Name: "lint", Status: GateStatus("green")}, false},
		{"finished before started", Gate{Name: "wait", Status: GateStatusFailed, StartedAt: &late, FinishedAt: &early}, false},
		{"only a start", Gate{Name: "wait", Status: GateStatusPassed, StartedAt: &early}, true},
	}
	for _, c := range cases {
		err := c.gate.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: want valid, got %v", c.name, err)
		}
		if !c.ok && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", c.name, err)
		}
	}
}

// Every field the primitives put on a gate must survive a round trip, or a
// backend that stores the JSON loses evidence silently.
func TestGate_RoundTrip(t *testing.T) {
	started := time.Unix(1700000000, 0).UTC()
	finished := started.Add(90 * time.Second)
	recorded := finished.Add(time.Second)
	want := Gate{
		Name: "smoke", Status: GateStatusFailed, URL: "https://ci/run/1",
		Summary: "2 of 3 routes healthy", StartedAt: &started, FinishedAt: &finished,
		RunID: "github:acme/app/12345/1", Details: map[string]any{"routes": "2/3"},
		RecordedBy: "token:tok_1", RecordedAt: &recorded,
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Gate
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != want.Name || got.Status != want.Status || got.URL != want.URL ||
		got.Summary != want.Summary || got.RunID != want.RunID ||
		got.RecordedBy != want.RecordedBy || got.RawStatus != "" {
		t.Fatalf("round trip lost a field:\n got %+v\nwant %+v", got, want)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) ||
		got.FinishedAt == nil || !got.FinishedAt.Equal(finished) ||
		got.RecordedAt == nil || !got.RecordedAt.Equal(recorded) {
		t.Fatalf("round trip lost timing: %+v", got)
	}
	if got.Details["routes"] != "2/3" {
		t.Fatalf("round trip lost details: %+v", got.Details)
	}
}

// An absent gate status is absent, not a pass — the same rule as a missing
// artifact kind.
func TestGate_MissingStatusIsNotAPass(t *testing.T) {
	var g Gate
	if err := json.Unmarshal([]byte(`{"name":"lint"}`), &g); err != nil {
		t.Fatal(err)
	}
	if g.Status == GateStatusPassed {
		t.Fatal("a gate with no status must never read as passed")
	}
	if err := g.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("want ErrInvalid, got %v", err)
	}
}
