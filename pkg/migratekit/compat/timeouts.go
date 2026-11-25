package compat

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// LockTimeoutDirective and StatementTimeoutDirective are the lines a migration
// carries to override the migrator's timeouts for that one file:
//
//	-- forge:lock-timeout 30s
//	-- forge:statement-timeout 2h — backfills 40M rows
//
// The value is a Go duration (`500ms`, `30s`, `5m`, `1h30m`), or `0` / `off` to
// disable the timeout for the file. A rationale may follow the value. Exported
// so remediation text quotes the spelling the parser matches.
const (
	LockTimeoutDirective      = "-- forge:lock-timeout"
	StatementTimeoutDirective = "-- forge:statement-timeout"
)

// timeoutDirectiveRe matches any `forge:lock…` / `forge:statement…` line
// comment, so a misspelling (`forge:lock-timout`) is an error and not a
// directive that silently does nothing.
var timeoutDirectiveRe = regexp.MustCompile(`(?i)^\s*--\s*forge:(lock|statement)(\S*)(.*)$`)

// maxTimeout is the largest value postgres accepts for lock_timeout and
// statement_timeout: a signed 32-bit count of milliseconds.
const maxTimeout = math.MaxInt32 * time.Millisecond

// Timeouts is the per-file override one migration's up SQL declares. A nil
// field is "not declared", which is not the same as a declared 0: 0 disables
// the timeout for that file.
type Timeouts struct {
	Lock      *time.Duration
	Statement *time.Duration
}

// TimeoutSyntaxError is a malformed timeout directive. It carries the line so
// the migrator's message can point at it.
type TimeoutSyntaxError struct {
	Line   int
	Reason string
}

func (e *TimeoutSyntaxError) Error() string {
	return fmt.Sprintf("line %d: %s", e.Line, e.Reason)
}

// ParseTimeout reads one timeout value: a positive Go duration of at least a
// millisecond, or `0` / `off` for "no timeout" (returned as 0). It is the
// grammar of both the directives and the `forge db migrate up` flags.
func ParseTimeout(value string) (time.Duration, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "0" || v == "off" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration — write a number with a unit (500ms, 30s, 5m, 1h) or `off`", value)
	}
	if d < time.Millisecond {
		return 0, fmt.Errorf("%q is below the 1ms postgres can enforce — write `off` to disable the timeout", value)
	}
	if d > maxTimeout {
		return 0, fmt.Errorf("%q is above the largest timeout postgres accepts (about 24 days) — write `off` to disable it", value)
	}
	return d, nil
}

// ParseTimeouts reads the timeout overrides one migration's up SQL declares.
// No directive is the zero Timeouts and no error: almost every migration
// declares none.
//
// The grammar is strict for the same reason ParseAdoption's is: the migrator
// parses every embedded file at boot, so a malformed or doubled directive
// fails the first run that migrates a fresh database instead of waiting for
// the production table it was written for.
func ParseTimeouts(upSQL string) (Timeouts, error) {
	var out Timeouts
	err := directiveLines(upSQL, func(line int, text string) error {
		m := timeoutDirectiveRe.FindStringSubmatch(text)
		if m == nil {
			return nil
		}
		var (
			into *(*time.Duration)
			want string
		)
		switch strings.ToLower(m[1] + m[2]) {
		case "lock-timeout":
			into, want = &out.Lock, LockTimeoutDirective
		case "statement-timeout":
			into, want = &out.Statement, StatementTimeoutDirective
		default:
			return &TimeoutSyntaxError{Line: line, Reason: fmt.Sprintf("unknown directive `forge:%s%s` — the timeout directives are spelled %q and %q", m[1], m[2], LockTimeoutDirective, StatementTimeoutDirective)}
		}
		if *into != nil {
			return &TimeoutSyntaxError{Line: line, Reason: fmt.Sprintf("%q is declared twice — a migration has one", want)}
		}
		fields := strings.Fields(m[3])
		if len(fields) == 0 {
			return &TimeoutSyntaxError{Line: line, Reason: fmt.Sprintf("%q names no value — write `%s <duration>` (30s, 5m, 1h) or `%s off`", want, want, want)}
		}
		d, err := ParseTimeout(fields[0])
		if err != nil {
			return &TimeoutSyntaxError{Line: line, Reason: fmt.Sprintf("%q: %v", want, err)}
		}
		*into = &d
		return nil
	})
	if err != nil {
		return Timeouts{}, err
	}
	return out, nil
}
