package changes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// recordSnapshot holds one change per outcome the recorder must tell apart.
func recordSnapshot() string {
	return fmt.Sprintf(`{"schema":"forge.changes/v1","revision":7,"changes":{
		"exp":{"variants":{"control":false,"treatment":true},"default_variant":"control",
			"delivery":{"mode":"experiment","unit":"user","run":"r_1","salt":"r_1","percent_bp":10000,
				"audience":{"attribute":"plan","in":["paid"]},
				"split":[{"variant":"treatment","weight_bp":10000}],
				"preview":[{"unit_hash":%q,"variant":"control"}]}},
		"acct":{"variants":{"on":true},
			"delivery":{"mode":"rollout","unit":"account","run":"r_2","salt":"r_2","percent_bp":10000,"rollout_variant":"on"}},
		"zero":{"variants":{"on":true},
			"delivery":{"mode":"rollout","unit":"user","run":"r_3","salt":"r_3","percent_bp":0,"rollout_variant":"on"}},
		"all":{"variants":{"blue":"blue"},"delivery":{"mode":"everyone","variant":"blue"}},
		"off":{"variants":{"on":true,"no":false},"default_variant":"no","delivery":{"mode":"off"}},
		"bad":{"variants":{"on":true},"delivery":{"mode":"sideways"}}
	}}`, PreviewHash("exp", "previewer"))
}

// recordingStore is a Store over recordSnapshot that records through the
// default recorder, with every event it tracks written to the returned
// buffer through ctx's logger.
func recordingStore(t *testing.T) (*Store, context.Context, *bytes.Buffer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.json")
	writeFile(t, path, recordSnapshot())
	s := OpenFile(context.Background(), path, Options{Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() { _ = s.Close() })
	var buf bytes.Buffer
	ctx := observe.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)))
	return s, ctx, &buf
}

func events(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func user(id, plan string) EvalContext {
	return EvalContext{TargetingKey: id, Attributes: map[string]string{"plan": plan}}
}

func TestStoreRecordsAnAssignmentOnce(t *testing.T) {
	s, ctx, buf := recordingStore(t)
	for range 3 {
		if r := s.Evaluate(ctx, "exp", user("u1", "paid")); r.Reason != ReasonExperiment {
			t.Fatalf("reason %q, want experiment", r.Reason)
		}
	}
	got := events(t, buf)
	if len(got) != 1 {
		t.Fatalf("three evaluations of one unit recorded %d events, want 1: %v", len(got), got)
	}
	want := map[string]any{
		"msg":              "product.flag_evaluated",
		"event.name":       "product.flag_evaluated",
		"product.flag":     "exp",
		"product.variant":  "treatment",
		"product.provider": "forge",
		"product.run":      "r_1",
		"product.unit":     "user",
		"product.reason":   "experiment",
		"product.revision": "7",
	}
	for k, v := range want {
		if got[0][k] != v {
			t.Errorf("%s = %v, want %v (event %v)", k, got[0][k], v, got[0])
		}
	}

	s.Evaluate(ctx, "exp", user("u2", "paid"))
	if n := len(events(t, buf)); n != 2 {
		t.Fatalf("a second unit: %d events in all, want 2", n)
	}
}

// A change with no unit (everyone mode) still records each caller once a
// day: by its targeting key, else the actor the context carries. It never
// collapses every caller into the first one.
func TestStoreRecordsEachCallerOfAChangeWithNoUnit(t *testing.T) {
	s, ctx, buf := recordingStore(t)
	for _, id := range []string{"u1", "u2", "u1", "u3", "u2"} {
		s.Evaluate(ctx, "all", EvalContext{TargetingKey: id})
	}
	if n := len(events(t, buf)); n != 3 {
		t.Fatalf("three callers of an everyone change recorded %d events, want 3", n)
	}
	for _, a := range []observe.Actor{{UserID: "a"}, {UserID: "b"}, {UserID: "a"}, {AnonymousID: "a"}} {
		s.Evaluate(observe.WithActor(ctx, a), "all", EvalContext{})
	}
	if n := len(events(t, buf)); n != 6 {
		t.Fatalf("three more actors (user a, user b, visitor a): %d events in all, want 6", n)
	}
}

func TestStoreRecordsOnlyUsefulReasons(t *testing.T) {
	cases := []struct {
		key    string
		ec     EvalContext
		reason Reason
		record bool
	}{
		{"exp", user("u1", "paid"), ReasonExperiment, true},
		{"acct", EvalContext{Attributes: map[string]string{"account": "a1"}}, ReasonRollout, true},
		{"exp", user("previewer", "free"), ReasonPreview, true},
		{"all", EvalContext{}, ReasonEveryone, true},
		{"exp", user("u3", "free"), ReasonIneligible, false},
		{"exp", EvalContext{}, ReasonNoUnit, false},
		{"zero", user("u4", ""), ReasonNotEnrolled, false},
		{"off", user("u5", ""), ReasonDefault, false},
		{"bad", user("u6", ""), ReasonInvalid, false},
		{"missing", user("u7", ""), ReasonUnknown, false},
	}
	for _, c := range cases {
		t.Run(string(c.reason), func(t *testing.T) {
			s, ctx, buf := recordingStore(t)
			if r := s.Evaluate(ctx, c.key, c.ec); r.Reason != c.reason {
				t.Fatalf("reason %q, want %q", r.Reason, c.reason)
			}
			got := events(t, buf)
			if c.record != (len(got) == 1) || len(got) > 1 {
				t.Fatalf("%s recorded %d events, want recorded=%v: %v", c.reason, len(got), c.record, got)
			}
			if c.record && c.key == "acct" && got[0]["product.unit"] != "account" {
				t.Fatalf("account unit recorded as %v", got[0]["product.unit"])
			}
			if c.record && c.key == "all" {
				if _, ok := got[0]["product.run"]; ok {
					t.Fatalf("an everyone result carries a run: %v", got[0])
				}
			}
		})
	}
}

// An account-unit change's assignment names the account it hashed, so a
// readout can count an app whose accounts are not its telemetry orgs. A
// user-unit one does not: the user id is a column of every event already.
func TestStoreRecordsTheAccountOfAnAccountUnit(t *testing.T) {
	s, ctx, buf := recordingStore(t)
	s.Evaluate(ctx, "acct", EvalContext{TargetingKey: "u1", Attributes: map[string]string{"account": "a1"}})
	s.Evaluate(ctx, "exp", EvalContext{TargetingKey: "u1", Attributes: map[string]string{"plan": "paid", "account": "a1"}})
	got := events(t, buf)
	if len(got) != 2 {
		t.Fatalf("recorded %d events, want 2: %v", len(got), got)
	}
	if got[0]["product.flag"] != "acct" || got[0]["product.account"] != "a1" {
		t.Errorf("account-unit assignment: account = %v, want a1 (event %v)", got[0]["product.account"], got[0])
	}
	if _, ok := got[1]["product.account"]; ok {
		t.Errorf("user-unit assignment carries an account: %v", got[1])
	}
}

func TestStoreCustomOnEvaluateReplacesRecording(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	writeFile(t, path, recordSnapshot())
	var seen []string
	s := OpenFile(context.Background(), path, Options{OnEvaluate: func(_ context.Context, key string, r Result) {
		seen = append(seen, key+"="+string(r.Reason))
	}})
	defer s.Close()
	var buf bytes.Buffer
	ctx := observe.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)))
	s.Evaluate(ctx, "exp", user("u1", "paid"))
	s.Evaluate(ctx, "zero", user("u1", ""))
	if fmt.Sprint(seen) != "[exp=experiment zero=not_enrolled]" {
		t.Fatalf("OnEvaluate saw %v", seen)
	}
	if buf.Len() != 0 {
		t.Fatalf("a custom OnEvaluate still recorded: %s", buf.String())
	}
}

func TestRecorderForgetsDaily(t *testing.T) {
	now := time.Date(2026, 11, 20, 23, 59, 0, 0, time.UTC)
	rec := &recorder{now: func() time.Time { return now }}
	k := seenKey{who: [32]byte{1}, key: "exp", run: "r_1", variant: "treatment"}
	if !rec.first(k) || rec.first(k) {
		t.Fatal("the first evaluation must record and the second must not")
	}
	if !rec.first(seenKey{who: [32]byte{1}, key: "exp", run: "r_2", variant: "treatment"}) {
		t.Fatal("a new run is a new assignment")
	}
	now = now.Add(2 * time.Minute)
	if !rec.first(k) {
		t.Fatal("a new UTC day records again")
	}
}

// whoOf is a distinct identity digest per i.
func whoOf(i int) (w [32]byte) {
	w[0], w[1], w[2] = byte(i), byte(i>>8), byte(i>>16)
	return w
}

func TestRecorderIsBounded(t *testing.T) {
	rec := newRecorder()
	first := seenKey{who: [32]byte{0}, key: "exp"}
	rec.first(first)
	for i := 1; i < maxRemembered; i++ {
		rec.first(seenKey{who: whoOf(i), key: "exp"})
	}
	if len(rec.seen) != maxRemembered {
		t.Fatalf("remembered %d, want %d", len(rec.seen), maxRemembered)
	}
	// Full: the next new key clears the memory, so the first is new again.
	rec.first(seenKey{who: whoOf(maxRemembered), key: "exp"})
	if len(rec.seen) != 1 || !rec.first(first) {
		t.Fatalf("a full memory was not cleared (%d remembered)", len(rec.seen))
	}
}
