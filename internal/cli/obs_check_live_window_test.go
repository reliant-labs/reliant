package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/releasehealth"
)

// The suggested window is the needed width rounded UP to whole minutes, so
// it always covers the need; and it is offered only when that rounded width
// is one a check may judge. The boundary cases sit right at the cap.
func TestLiveWindowCheckStaysWithinTheCapAndCoversTheNeed(t *testing.T) {
	from := time.Date(2026, 11, 10, 3, 47, 30, 0, time.UTC)
	report := func(delay time.Duration) releasehealth.Report {
		return releasehealth.Report{
			Subject:       releasehealth.Subject{PromotionID: "p-1", PromotedAt: from, LiveAt: from.Add(delay)},
			After:         releasehealth.Range{From: from, To: from.Add(15 * time.Minute)},
			AfterComplete: true,
		}
	}
	// With a 15m judged window and a delay over 15m, the need is 2*delay.
	for name, tc := range map[string]struct {
		delay time.Duration
		want  string // the --window value, "" for no suggestion
	}{
		"exactly the cap":                 {delay: 3 * time.Hour, want: "6h"},
		"just under the cap rounds to it": {delay: 3*time.Hour - 15*time.Second, want: "6h"},
		"1ns over the cap":                {delay: 3*time.Hour + time.Nanosecond, want: ""},
		"1s over the cap":                 {delay: 3*time.Hour + time.Second, want: ""},
		"a partial minute rounds up":      {delay: 20*time.Minute + 10*time.Second, want: "41m"},
	} {
		t.Run(name, func(t *testing.T) {
			got := liveWindowCheck("prod", report(tc.delay))
			if tc.want == "" {
				if got != "" {
					t.Fatalf("suggested %q for a need of %s, over the %s cap", got, 2*tc.delay, release.MaxHealthWindow)
				}
				return
			}
			if !strings.HasSuffix(got, "--window "+tc.want) || !strings.Contains(got, "--promotion p-1") {
				t.Fatalf("suggested %q, want --promotion p-1 --window %s", got, tc.want)
			}
			w, err := time.ParseDuration(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if need := 2 * tc.delay; w < need || w > release.MaxHealthWindow {
				t.Fatalf("--window %s does not cover the need %s within the cap %s", w, need, release.MaxHealthWindow)
			}
		})
	}
}
