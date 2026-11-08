package serverkit

import (
	"testing"
	"time"
)

// PreStopDelay and ShutdownTimeout follow one rule, the one every Config
// duration does: zero is the default, negative is none. ShutdownTimeout used
// to turn every value <= 0 into the default, so "no shutdown window" could
// not be said while "no pause" had to be said with a negative.
func TestDrainer_DurationsFollowConfigsRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		set          time.Duration
		wantPreStop  time.Duration
		wantShutdown time.Duration
	}{
		{"zero is the default", 0, defaultPreStopDelay, defaultShutdownTimeout},
		{"negative is none", -1, 0, 0},
		{"positive is itself", 7 * time.Second, 7 * time.Second, 7 * time.Second},
	} {
		d := &Drainer{PreStopDelay: tc.set, ShutdownTimeout: tc.set}
		if got := d.preStopDelay(); got != tc.wantPreStop {
			t.Errorf("%s: PreStopDelay %v → %v, want %v", tc.name, tc.set, got, tc.wantPreStop)
		}
		if got := d.shutdownTimeout(); got != tc.wantShutdown {
			t.Errorf("%s: ShutdownTimeout %v → %v, want %v", tc.name, tc.set, got, tc.wantShutdown)
		}

		// Through Config, as Run builds it: Normalize keeps a negative.
		cfgd := NewDrainer(Config{PreStopDelay: tc.set, ShutdownTimeout: tc.set})
		if cfgd.preStopDelay() != tc.wantPreStop || cfgd.shutdownTimeout() != tc.wantShutdown {
			t.Errorf("%s: NewDrainer(Config) = (%v, %v), want (%v, %v)", tc.name,
				cfgd.preStopDelay(), cfgd.shutdownTimeout(), tc.wantPreStop, tc.wantShutdown)
		}
	}
}
