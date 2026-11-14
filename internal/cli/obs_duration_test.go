package cli

import (
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func TestParseObsDuration(t *testing.T) {
	const day = 24 * time.Hour
	for in, want := range map[string]time.Duration{
		"7d":    7 * day,
		"1d12h": day + 12*time.Hour,
		"30d":   30 * day,
		"0d":    0,
		"168h":  168 * time.Hour,
		"90m":   90 * time.Minute,
		"1h30m": 90 * time.Minute,
		"0":     0,
		"-1d":   -day,
		"-2h":   -2 * time.Hour,
	} {
		got, err := parseObsDuration(in)
		if err != nil || got != want {
			t.Errorf("parseObsDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "-", "d", "7", "7days", "1.5d", "1d-2h", "1d+2h", "+1d", "--1d", "x7d", "7dd"} {
		if got, err := parseObsDuration(in); err == nil {
			t.Errorf("parseObsDuration(%q) = %v; want an error", in, got)
		}
	}
}

// The flag keeps pflag's own duration rendering, so --help and the default
// shown there do not change for the verbs that switched to it.
func TestObsDurationVarKeepsDurationHelp(t *testing.T) {
	f := pflag.NewFlagSet("t", pflag.ContinueOnError)
	var since time.Duration
	obsDurationVar(f, &since, "since", time.Hour, "how far back")
	fl := f.Lookup("since")
	if fl.Value.Type() != "duration" || fl.DefValue != "1h0m0s" || since != time.Hour {
		t.Fatalf("type %q default %q value %v", fl.Value.Type(), fl.DefValue, since)
	}
	if err := f.Parse([]string{"--since", "7d"}); err != nil || since != 7*24*time.Hour {
		t.Fatalf("--since 7d: %v, %v", since, err)
	}
	if err := f.Parse([]string{"--since", "7x"}); err == nil {
		t.Fatal("--since 7x was accepted")
	}
}
