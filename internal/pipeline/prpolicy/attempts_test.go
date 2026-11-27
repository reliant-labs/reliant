package prpolicy

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

func TestNextAttempt(t *testing.T) {
	three := config.Defaults().Fix
	cases := []struct {
		name      string
		state     AttemptState
		sig       Signature
		cfg       config.ResolvedFix
		want      AttemptState
		exhausted bool
	}{
		{"first attempt", AttemptState{}, "v1:a", three, AttemptState{"v1:a", 1}, false},
		{"second at the same failure", AttemptState{"v1:a", 1}, "v1:a", three, AttemptState{"v1:a", 2}, false},
		{"third at the same failure", AttemptState{"v1:a", 2}, "v1:a", three, AttemptState{"v1:a", 3}, false},
		{"cap reached: needs a person", AttemptState{"v1:a", 3}, "v1:a", three, AttemptState{"v1:a", 3}, true},
		{"a new failure starts again, even past the cap", AttemptState{"v1:a", 3}, "v1:b", three, AttemptState{"v1:b", 1}, false},
		{"a cap of one", AttemptState{"v1:a", 1}, "v1:a", config.ResolvedFix{AttemptsPerSignature: 1}, AttemptState{"v1:a", 1}, true},
		{"an unresolved config uses the default", AttemptState{"v1:a", 2}, "v1:a", config.ResolvedFix{}, AttemptState{"v1:a", 3}, false},
		{"an unresolved config caps at the default", AttemptState{"v1:a", 3}, "v1:a", config.ResolvedFix{}, AttemptState{"v1:a", 3}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, exhausted := NextAttempt(c.state, c.sig, c.cfg)
			if got != c.want || exhausted != c.exhausted {
				t.Fatalf("got %+v, %v; want %+v, %v", got, exhausted, c.want, c.exhausted)
			}
		})
	}
}

// The default (§10.1): 3 attempts per signature, then needs_person.
func TestNextAttemptDefaultSequence(t *testing.T) {
	cfg := config.Defaults().Fix
	var s AttemptState
	sig := FailureSignature([]CheckFailure{{Check: "test", Test: "TestX", Detail: "x_test.go:1: boom"}})
	for i := 1; i <= 3; i++ {
		var exhausted bool
		if s, exhausted = NextAttempt(s, sig, cfg); exhausted || s.Attempts != i {
			t.Fatalf("attempt %d: %+v %v", i, s, exhausted)
		}
	}
	if _, exhausted := NextAttempt(s, sig, cfg); !exhausted {
		t.Fatal("a fourth attempt at the same failure was allowed")
	}
	if r := ExhaustedReason(s); !strings.Contains(r, "3 fix attempts") || !strings.Contains(r, sig.Short()) {
		t.Fatalf("reason: %q", r)
	}
	if r := ExhaustedReason(AttemptState{Signature: sig, Attempts: 1}); !strings.Contains(r, "1 fix attempt left") {
		t.Fatalf("reason: %q", r)
	}
}

func TestReviewRoundsExhausted(t *testing.T) {
	def := config.Defaults().Review
	for rounds, want := range map[int]bool{0: false, 2: false, 3: true, 4: true} {
		if got := ReviewRoundsExhausted(rounds, def); got != want {
			t.Errorf("after %d rounds: %v, want %v", rounds, got, want)
		}
	}
	if ReviewRoundsExhausted(2, config.ResolvedReview{}) || !ReviewRoundsExhausted(3, config.ResolvedReview{}) {
		t.Error("an unresolved config does not use the default of 3")
	}
	if !ReviewRoundsExhausted(1, config.ResolvedReview{MaxRounds: 1}) {
		t.Error("max_rounds 1 allows a second round")
	}
}
