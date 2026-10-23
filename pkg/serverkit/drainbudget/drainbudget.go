// Package drainbudget is the one reading of a process's two drain knobs,
// PRE_STOP_DELAY and SHUTDOWN_TIMEOUT: how a value is written, what zero and a
// negative value mean, and the defaults.
//
// Two parties read those variables and must agree to the second: the server,
// which flips readiness, pauses PRE_STOP_DELAY and then shuts down bounded by
// SHUTDOWN_TIMEOUT; and forge's renderer, which derives the pod's
// terminationGracePeriodSeconds from the same two values (pkg/deploy). When
// they disagree the kubelet SIGKILLs a replica mid-drain, or holds a dead one
// for a budget nobody asked for. Each party used to carry its own parser, and
// they drifted: one read "-1s" (no pause) as malformed and fell back to the
// default. Both now call this package.
//
// The rule:
//
//   - A value is a Go duration ("30s", "1m30s", "500ms", "-1s", and "0").
//     Empty is unset. Anything else is an error — a bare count of seconds
//     ("30") included.
//   - Zero, set or unset, is the default: 5s for PRE_STOP_DELAY, 30s for
//     SHUTDOWN_TIMEOUT.
//   - Negative is none: a phase that takes no time. Never unbounded.
//
// WHY NO BARE SECONDS. A forge server reads both knobs as
// google.protobuf.Duration config fields, and the config language takes a Go
// duration only: a bare number has no unit, and for any other Duration field
// ("500" as an RPC timeout) guessing seconds is the wrong guess as often as
// not. This reader used to accept "30" as well, so the renderer derived a
// grace period from a value the server itself refused to boot on. Both
// readers now take the same language, the config loader's.
//
// It has no dependencies beyond the standard library, so the renderer can
// import it without linking serverkit.
package drainbudget

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// EnvPreStopDelay is how long the process keeps serving after its
	// readiness flips to draining, so load balancers observe the failing
	// probe before anything stops.
	EnvPreStopDelay = "PRE_STOP_DELAY"
	// EnvShutdownTimeout bounds everything after the pre-stop pause.
	EnvShutdownTimeout = "SHUTDOWN_TIMEOUT"

	// DefaultPreStopDelay is what a zero or unset PRE_STOP_DELAY means.
	DefaultPreStopDelay = 5 * time.Second
	// DefaultShutdownTimeout is what a zero or unset SHUTDOWN_TIMEOUT means.
	DefaultShutdownTimeout = 30 * time.Second
)

// Parse reads one drain knob as written: a Go duration. Empty (after
// trimming space) is 0, the default. The result is the value as configured —
// apply [Effective] for the length of the phase. A bare number other than 0
// is an error that says which unit to add.
func Parse(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err == nil {
		return d, nil
	}
	if _, nerr := strconv.ParseInt(raw, 10, 64); nerr == nil {
		return 0, fmt.Errorf("%q has no unit: write a Go duration (%q for seconds); the server's config loader refuses a bare number", raw, raw+"s")
	}
	return 0, fmt.Errorf("%q is not a Go duration (\"30s\", \"1m30s\", \"-1s\")", raw)
}

// Effective is the length of a drain phase configured as d: def when d is
// zero, none (0) when d is negative, d otherwise.
func Effective(d, def time.Duration) time.Duration {
	switch {
	case d == 0:
		return def
	case d < 0:
		return 0
	}
	return d
}

// Budget is a process's two drain knobs as configured — before [Effective]
// applies the rule, so a zero still means "the default" and a negative still
// means "none". Its fields map one to one onto serverkit.Config's
// PreStopDelay and ShutdownTimeout.
type Budget struct {
	PreStopDelay    time.Duration
	ShutdownTimeout time.Duration
}

// FromEnv reads both knobs through getenv — os.Getenv in a main; a library
// never reads the environment itself. An unset variable is zero (the
// default). A malformed one is an error naming the variable: a server should
// refuse to boot rather than run a budget its pod's grace period was not
// derived from.
func FromEnv(getenv func(string) string) (Budget, error) {
	var b Budget
	var err error
	if b.PreStopDelay, err = Parse(getenv(EnvPreStopDelay)); err != nil {
		return Budget{}, fmt.Errorf("%s: %w", EnvPreStopDelay, err)
	}
	if b.ShutdownTimeout, err = Parse(getenv(EnvShutdownTimeout)); err != nil {
		return Budget{}, fmt.Errorf("%s: %w", EnvShutdownTimeout, err)
	}
	return b, nil
}

// PreStop is the effective pre-stop pause.
func (b Budget) PreStop() time.Duration { return Effective(b.PreStopDelay, DefaultPreStopDelay) }

// Shutdown is the effective shutdown window.
func (b Budget) Shutdown() time.Duration {
	return Effective(b.ShutdownTimeout, DefaultShutdownTimeout)
}
