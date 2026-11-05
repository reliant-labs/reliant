package drainbudget_test

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit/drainbudget"
)

// TestParseAndEffective is the rule, as one table: how a value is written,
// what it is as configured, and how long the phase then takes. Every reader
// of PRE_STOP_DELAY / SHUTDOWN_TIMEOUT — serverkit's Drainer, pkg/deploy's
// grace period, a hand-written main — goes through these two functions.
func TestParseAndEffective(t *testing.T) {
	t.Parallel()
	const def = 30 * time.Second
	for _, c := range []struct {
		raw        string
		configured time.Duration
		effective  time.Duration
	}{
		{"", 0, def},   // unset: the default
		{"0", 0, def},  // zero: the default
		{"0s", 0, def}, // zero as a duration: the default
		{"30s", 30 * time.Second, 30 * time.Second},
		{"1m30s", 90 * time.Second, 90 * time.Second},
		{"500ms", 500 * time.Millisecond, 500 * time.Millisecond},
		{" 10s ", 10 * time.Second, 10 * time.Second},
		{"-1s", -time.Second, 0}, // negative: none — a copy of this rule once read it as malformed
		{"-5m", -5 * time.Minute, 0},
	} {
		got, err := drainbudget.Parse(c.raw)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.raw, err)
			continue
		}
		if got != c.configured {
			t.Errorf("Parse(%q) = %v, want %v", c.raw, got, c.configured)
		}
		if eff := drainbudget.Effective(got, def); eff != c.effective {
			t.Errorf("Effective(Parse(%q)) = %v, want %v", c.raw, eff, c.effective)
		}
	}
	for _, raw := range []string{"abc", "${SHUTDOWN_TIMEOUT}", "30 s", "1.5", "99999999999999999999", "30", "-1"} {
		if d, err := drainbudget.Parse(raw); err == nil {
			t.Errorf("Parse(%q) = %v, want an error", raw, d)
		}
	}
}

// TestParseRefusesBareSeconds pins that the drain reader takes the config
// loader's language: a forge server reads PRE_STOP_DELAY / SHUTDOWN_TIMEOUT
// as Duration config fields, which refuse "30", so a reader that accepted it
// derived a grace period for a value the server would not boot on. The error
// names the fix.
func TestParseRefusesBareSeconds(t *testing.T) {
	t.Parallel()
	_, err := drainbudget.Parse("30")
	if err == nil {
		t.Fatal(`Parse("30") accepted a bare number the config loader refuses`)
	}
	if !strings.Contains(err.Error(), `"30s"`) {
		t.Errorf("error must say how to write it (\"30s\"); got: %v", err)
	}
}

func TestFromEnv(t *testing.T) {
	t.Parallel()
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}

	b, err := drainbudget.FromEnv(env(nil))
	if err != nil {
		t.Fatalf("FromEnv(unset): %v", err)
	}
	if b != (drainbudget.Budget{}) || b.PreStop() != drainbudget.DefaultPreStopDelay || b.Shutdown() != drainbudget.DefaultShutdownTimeout {
		t.Errorf("unset = %+v (%v, %v), want zero values with the defaults", b, b.PreStop(), b.Shutdown())
	}

	b, err = drainbudget.FromEnv(env(map[string]string{"PRE_STOP_DELAY": "-1s", "SHUTDOWN_TIMEOUT": "45s"}))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if b.PreStopDelay != -time.Second || b.PreStop() != 0 {
		t.Errorf("PRE_STOP_DELAY=-1s: configured %v effective %v, want -1s and none", b.PreStopDelay, b.PreStop())
	}
	if b.ShutdownTimeout != 45*time.Second || b.Shutdown() != 45*time.Second {
		t.Errorf("SHUTDOWN_TIMEOUT=45s: configured %v effective %v, want 45s", b.ShutdownTimeout, b.Shutdown())
	}

	for name, m := range map[string]map[string]string{
		"PRE_STOP_DELAY":                {"PRE_STOP_DELAY": "soon"},
		"SHUTDOWN_TIMEOUT":              {"SHUTDOWN_TIMEOUT": "forever"},
		"PRE_STOP_DELAY (bare seconds)": {"PRE_STOP_DELAY": "30"},
	} {
		if _, err := drainbudget.FromEnv(env(m)); err == nil || !strings.Contains(err.Error(), strings.Fields(name)[0]) {
			t.Errorf("FromEnv(%v) = %v, want an error naming %s", m, err, name)
		}
	}
}
