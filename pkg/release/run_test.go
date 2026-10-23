package release

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRun_Validate(t *testing.T) {
	cases := []struct {
		name string
		run  Run
		ok   bool
	}{
		{"zero", Run{}, true},
		{"id only", Run{ID: "github:acme/app/1/1"}, true},
		{"full", Run{ID: "github:acme/app/1/1", URL: "https://gh/runs/1", Provider: "github"}, true},
		{"manual", Run{ID: "manual:sean@box:1700000000", Provider: "manual"}, true},
		{"url with no id", Run{URL: "https://gh/runs/1"}, false},
		{"provider with no id", Run{Provider: "github"}, false},
		{"blank id with a url", Run{ID: "  ", URL: "https://gh/runs/1"}, false},
	}
	for _, c := range cases {
		err := c.run.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: want valid, got %v", c.name, err)
		}
		if !c.ok && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", c.name, err)
		}
	}
}

func TestRun_Zero(t *testing.T) {
	if !(Run{}).Zero() {
		t.Error("the zero Run must report Zero")
	}
	if (Run{ID: "x"}).Zero() {
		t.Error("a run with an id is not zero")
	}
}

// A run belongs to a promotion and a release, and neither may refuse one
// that has no run: --no-run is a supported choice.
func TestRunAttachesToPromotionAndRelease(t *testing.T) {
	r := Release{Version: "v1", Artifacts: map[string]Artifact{"api": image(digest("a"))}}
	if err := r.Validate(); err != nil {
		t.Fatalf("a release with no run must validate: %v", err)
	}
	r.Run = Run{ID: "github:acme/app/7/1", URL: "https://gh/7", Provider: "github"}
	if err := r.Validate(); err != nil {
		t.Fatalf("a release with a run must validate: %v", err)
	}
	r.Run = Run{URL: "https://gh/7"}
	if err := r.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a release whose run has no id must be refused, got %v", err)
	}

	p := NewPromotion("prod", Release{Version: "v1", Artifacts: map[string]Artifact{"api": image(digest("a"))}}, KindPromote)
	p.Run = Run{ID: "github:acme/app/7/1"}
	if err := p.Validate(); err != nil {
		t.Fatalf("a promotion with a run must validate: %v", err)
	}
	p.Run = Run{Provider: "github"}
	if err := p.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a promotion whose run has no id must be refused, got %v", err)
	}
}

// A run id is NOT part of a release's identity: CI's retry re-cuts the same
// bytes under a new attempt and that is still an idempotent re-cut, not a
// conflict.
func TestRelease_RunIsNotIdentity(t *testing.T) {
	base := Release{Version: "v1", Artifacts: map[string]Artifact{"api": image(digest("a"))}}
	first := base
	first.Run = Run{ID: "github:acme/app/7/1"}
	retry := base
	retry.Run = Run{ID: "github:acme/app/7/2"}
	if !first.SameContent(retry) {
		t.Fatal("two cuts of the same bytes under different run ids name the same release")
	}
	if err := CheckRecut(first, retry); err != nil {
		t.Fatalf("a retry under a new run id is an idempotent re-cut, got %v", err)
	}
}

func TestStageKindAndStatus_Closed(t *testing.T) {
	for _, raw := range []string{"", "deploy", "CUT", "converge", "verify"} {
		if _, err := ParseStageKind(raw); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseStageKind(%q): want ErrInvalid, got %v", raw, err)
		}
	}
	for _, raw := range []string{"build", "cut", "check", "promote", "rollout"} {
		if _, err := ParseStageKind(raw); err != nil {
			t.Errorf("ParseStageKind(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"", "ok", "RUNNING", "in_progress", "degraded"} {
		if _, err := ParseStageStatus(raw); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseStageStatus(%q): want ErrInvalid, got %v", raw, err)
		}
	}
	for _, raw := range []string{"passed", "failed", "running", "skipped", "error"} {
		if _, err := ParseStageStatus(raw); err != nil {
			t.Errorf("ParseStageStatus(%q): %v", raw, err)
		}
	}
	var k StageKind
	if err := json.Unmarshal([]byte(`"converge"`), &k); !errors.Is(err, ErrInvalid) {
		t.Errorf("stage kind decode: want ErrInvalid, got %v", err)
	}
	var s StageStatus
	if err := json.Unmarshal([]byte(`"degraded"`), &s); !errors.Is(err, ErrInvalid) {
		t.Errorf("stage status decode: want ErrInvalid, got %v", err)
	}
}

// A gate has always finished, so it never maps to running; an
// uninterpretable stored status maps to error rather than to a pass.
func TestStageStatusOfGate(t *testing.T) {
	cases := []struct {
		gate Gate
		want StageStatus
	}{
		{Gate{Name: "test", Status: GateStatusPassed}, StagePassed},
		{Gate{Name: "test", Status: GateStatusFailed}, StageFailed},
		{Gate{Name: "smoke", Status: GateStatusSkipped}, StageSkipped},
		{Gate{Name: "wait", Status: GateStatusError}, StageErrored},
		{Gate{Name: "old", Status: GateStatusError, RawStatus: "all good"}, StageErrored},
	}
	for _, c := range cases {
		if got := StageStatusOfGate(c.gate); got != c.want {
			t.Errorf("gate %+v → %q, want %q", c.gate, got, c.want)
		}
		if got := StageStatusOfGate(c.gate); got == StageRunning {
			t.Errorf("gate %+v mapped to running; a recorded gate has finished", c.gate)
		}
	}
}

func TestStage_Validate(t *testing.T) {
	early := time.Unix(1700000000, 0).UTC()
	late := early.Add(time.Minute)
	cases := []struct {
		name  string
		stage Stage
		ok    bool
	}{
		{"promote", Stage{Kind: StagePromote, Name: "prod", Env: "prod", Status: StagePassed, PromotionID: "p1"}, true},
		{"rollout running", Stage{Kind: StageRollout, Name: "prod", Status: StageRunning, StartedAt: &early}, true},
		{"unknown kind", Stage{Kind: StageKind("converge"), Status: StagePassed}, false},
		{"unknown status", Stage{Kind: StageCheck, Status: StageStatus("degraded")}, false},
		{"backwards window", Stage{Kind: StageCheck, Name: "lint", Status: StagePassed, StartedAt: &late, FinishedAt: &early}, false},
	}
	for _, c := range cases {
		err := c.stage.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: want valid, got %v", c.name, err)
		}
		if !c.ok && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", c.name, err)
		}
	}
}

// The verdict order is the one a pipeline's final step needs: a failure
// outranks a still-running stage, and running outranks passed, because a
// run that has not finished has not passed.
func TestRunTimeline_Verdict(t *testing.T) {
	stage := func(s StageStatus) Stage { return Stage{Kind: StageCheck, Name: "x", Status: s} }
	cases := []struct {
		name   string
		stages []Stage
		want   StageStatus
	}{
		{"no stages is not a pass", nil, StageRunning},
		{"all passed", []Stage{stage(StagePassed), stage(StagePassed)}, StagePassed},
		{"a failure beats a pass", []Stage{stage(StagePassed), stage(StageFailed)}, StageFailed},
		{"a failure beats running", []Stage{stage(StageRunning), stage(StageFailed)}, StageFailed},
		{"running beats a pass", []Stage{stage(StagePassed), stage(StageRunning)}, StageRunning},
		{"an error beats running", []Stage{stage(StageRunning), stage(StageErrored)}, StageErrored},
		{"a failure beats an error", []Stage{stage(StageErrored), stage(StageFailed)}, StageFailed},
		{"all skipped passes", []Stage{stage(StageSkipped)}, StagePassed},
		{"skipped does not mask a pass", []Stage{stage(StageSkipped), stage(StagePassed)}, StagePassed},
	}
	for _, c := range cases {
		if got := (RunTimeline{Stages: c.stages}).Verdict(); got != c.want {
			t.Errorf("%s: verdict = %q, want %q", c.name, got, c.want)
		}
	}
}

// A promotion's evidence trail is pre-promote gates then post-promote ones,
// in that order, from one call — so a renderer cannot show half of it.
func TestPromotion_AllGates(t *testing.T) {
	p := Promotion{
		Gates:         []Gate{{Name: "lint", Status: GateStatusPassed}, {Name: "test", Status: GateStatusPassed}},
		RecordedGates: []Gate{{Name: "wait", Status: GateStatusPassed}, {Name: "smoke", Status: GateStatusFailed}},
	}
	got := p.AllGates()
	want := []string{"lint", "test", "wait", "smoke"}
	if len(got) != len(want) {
		t.Fatalf("AllGates returned %d gates, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("AllGates[%d] = %q, want %q", i, got[i].Name, name)
		}
	}
	if only := (Promotion{Gates: p.Gates}).AllGates(); len(only) != 2 {
		t.Errorf("with no recorded gates, AllGates is the promote-time gates: got %d", len(only))
	}
}

// The post-promote half is a SEPARATE field because the entry is
// append-only: evidence that arrives later cannot be written into Gates
// without rewriting history.
func TestPromotion_RecordedGatesRoundTripAndValidate(t *testing.T) {
	p := NewPromotion("prod", Release{Version: "v1", Artifacts: map[string]Artifact{"api": image(digest("a"))}}, KindPromote)
	p.Gates = []Gate{{Name: "lint", Status: GateStatusPassed}}
	p.RecordedGates = []Gate{{Name: "smoke", Status: GateStatusFailed, RecordedBy: "token:t1"}}
	p.Run = Run{ID: "github:acme/app/9/1"}
	p.FromPromotionID = "p-source"
	p.SupersededInFlight = true
	if err := p.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var got Promotion
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.RecordedGates) != 1 || got.RecordedGates[0].Name != "smoke" ||
		got.RecordedGates[0].RecordedBy != "token:t1" {
		t.Fatalf("recorded gates lost on round trip: %+v", got.RecordedGates)
	}
	if got.Run.ID != "github:acme/app/9/1" || got.FromPromotionID != "p-source" || !got.SupersededInFlight {
		t.Fatalf("run / from-promotion / supersede lost on round trip: %+v", got)
	}

	// A gate with no name cannot be attributed to any check, so it is
	// refused on either side.
	bad := p
	bad.RecordedGates = []Gate{{Status: GateStatusPassed}}
	if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("a nameless recorded gate must be refused, got %v", err)
	}
	bad = p
	bad.Gates = []Gate{{Status: GateStatusPassed}}
	if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("a nameless promote-time gate must be refused, got %v", err)
	}
}

// A promotion read back from a ledger written before the status set closed
// must still validate: its gates are old, not uninterpretable.
func TestPromotion_LegacyGateStatusStillReads(t *testing.T) {
	raw := `{"env":"prod","release":"v1","kind":"promote","resolved":{},` +
		`"gates":[{"name":"lint","status":"all good"}],"promoted_at":"2026-01-01T00:00:00Z"}`
	var p Promotion
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("an old ledger line must decode: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("an old ledger line must validate — its gate is old, not invalid: %v", err)
	}
	if p.Gates[0].Status != GateStatusError || p.Gates[0].RawStatus != "all good" {
		t.Fatalf("legacy gate mapped wrong: %+v", p.Gates[0])
	}
}

// A promotion cannot be its own source. FromEnv already had this rule; the
// exact-entry field needs it too.
func TestPromotion_FromPromotionNotSelf(t *testing.T) {
	p := NewPromotion("prod", Release{Version: "v1", Artifacts: map[string]Artifact{"api": image(digest("a"))}}, KindPromote)
	p.ID = "p1"
	p.FromPromotionID = "p1"
	if err := p.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	p.FromPromotionID = "p0"
	if err := p.Validate(); err != nil {
		t.Fatalf("a distinct source promotion is fine: %v", err)
	}
}
