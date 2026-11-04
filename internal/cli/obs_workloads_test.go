package cli

import (
	"testing"
	"time"
)

// The suggested `obs logs --since` must parse back to the range it came from.
func TestSinceFlagRoundTrips(t *testing.T) {
	for in, want := range map[time.Duration]string{
		10 * time.Minute: "10m", 30 * time.Minute: "30m", 70 * time.Minute: "1h10m",
		24 * time.Hour: "24h", 72 * time.Hour: "72h", 20 * time.Second: "1m",
	} {
		got := sinceFlag(in)
		if got != want {
			t.Errorf("sinceFlag(%s) = %q, want %q", in, got, want)
		}
		if d, err := time.ParseDuration(got); err != nil || (in >= time.Minute && d != in) {
			t.Errorf("%q does not parse back to %s: %v %v", got, in, d, err)
		}
	}
}
