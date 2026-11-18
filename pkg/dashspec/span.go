package dashspec

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

var spanPattern = regexp.MustCompile(`^([1-9][0-9]{0,3})([smhd])$`)

// ParseSpan reads a duration as the spec writes it: a number and a unit of s,
// m, h or d ("90m", "7d"). It is the form of Dashboard.DefaultRange and
// Panel.MinStep, and is refused when longer than MaxDefaultRange.
func ParseSpan(s string) (time.Duration, error) {
	m := spanPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q is not a duration: a number and a unit of s, m, h or d, like \"24h\" or \"7d\"", s)
	}
	n, _ := strconv.Atoi(m[1])
	unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
	d := time.Duration(n) * unit
	if d > MaxDefaultRange {
		return 0, fmt.Errorf("%q is longer than %s", s, MaxDefaultRange)
	}
	return d, nil
}
