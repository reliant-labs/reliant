package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"
)

// obsDurationValue is a time.Duration flag that also takes whole days: "7d",
// "1d12h", besides everything time.ParseDuration reads ("168h", "90m"). The obs
// verbs document their windows in days ("default 24h, max 7d", "--since up to
// 30d") and --compare already took "7d", so an agent naturally types
// `--since 7d`; a plain DurationVar refused it with "unknown unit d".
type obsDurationValue time.Duration

func (d *obsDurationValue) Set(s string) error {
	v, err := parseObsDuration(s)
	if err != nil {
		return err
	}
	*d = obsDurationValue(v)
	return nil
}

func (d *obsDurationValue) String() string { return time.Duration(*d).String() }

// Type is "duration", as pflag's own, so --help reads the same.
func (d *obsDurationValue) Type() string { return "duration" }

// obsDurationVar registers a duration flag that also accepts days.
func obsDurationVar(f *pflag.FlagSet, p *time.Duration, name string, value time.Duration, usage string) {
	*p = value
	f.Var((*obsDurationValue)(p), name, usage)
}

// parseObsDuration reads an optional leading "<n>d" and then, if anything is
// left, a Go duration: "7d", "1d12h", "36h", "-1d" (the caller judges sign).
func parseObsDuration(s string) (time.Duration, error) {
	rest, neg := s, false
	if r, ok := strings.CutPrefix(rest, "-"); ok {
		rest, neg = r, true
	}
	invalid := fmt.Errorf("invalid duration %q: use a Go duration (90m, 36h) or whole days (7d, 1d12h)", s)
	var days time.Duration
	hadDays := false
	if i := strings.IndexByte(rest, 'd'); i > 0 {
		n, err := strconv.Atoi(rest[:i])
		if err != nil || n < 0 || strings.HasPrefix(rest, "+") {
			return 0, invalid
		}
		days, hadDays = time.Duration(n)*24*time.Hour, true
		rest = rest[i+1:]
	}
	var tail time.Duration
	switch {
	case rest != "":
		if strings.HasPrefix(rest, "-") || strings.HasPrefix(rest, "+") {
			return 0, invalid
		}
		v, err := time.ParseDuration(rest)
		if err != nil {
			return 0, invalid
		}
		tail = v
	case !hadDays:
		return 0, invalid // "" or "-"
	}
	total := days + tail
	if neg {
		total = -total
	}
	return total, nil
}
