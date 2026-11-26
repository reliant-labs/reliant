package cli

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/exitcode"
)

// THE RESULT LINE: one line that ends a long command's text output and says
// how it ended.
//
// `forge env up`, `forge env deploy` and `forge env build --release` run for
// minutes, which is past an agent's default tool timeout, so an agent runs
// them in the background and reads the log afterwards. Hundreds of lines of
// build output then end in a sentence an agent has to interpret. The line
// makes the outcome unambiguous and greppable:
//
//	RESULT: ok env=prod url=https://shop.sites.reliantlabs.dev release=v0.3.0 next: forge obs check prod --wait
//	RESULT: failed stage=build env=prod reason="npm run build exited 1" next: …
//
// The words are fixed: the status is the process's exit code in one word
// (statusForExit), and the keys are lower-case, each at most once, in the
// order commandResult lists them. A value with a space or a quote is
// Go-quoted. `next:` comes last and runs to the end of the line, so it can
// hold a command with spaces unquoted.
//
// --json documents carry the same outcome in their envelope (ok, exit_code,
// error, error_reason), so a --json invocation prints no RESULT line: its
// stdout is exactly one document.
//
// ON FAILURE THE LINE RIDES THE ERROR (withResultLine). Whatever hosts forge
// prints the returned error last — forge's main, or `reliant forge` — so a
// line printed before returning would be followed by the error text and stop
// being the last line. Appending it to the error keeps it last under every
// host, and keeps the error's exit code (Unwrap).

// commandResult is one command's outcome, as the RESULT line renders it.
type commandResult struct {
	// Status is the exit code as a word: ok, failed, queued, pending,
	// undetermined, unconfirmed, declined, cancelled.
	Status string
	// Stage is where a failure happened (build, plan, promote, rollout,
	// readiness …). Empty on success.
	Stage string
	Env   string
	// URL is where the result is reachable: the deployed site, or the
	// first URL of a local stack.
	URL     string
	Release string
	// Claim is a claimable deploy's claim link.
	Claim string
	// Action is where a person resolves a queued deploy (billing).
	Action string
	// Reason is the failure in a few words (reasonOf). Empty on success.
	Reason string
	// Next is the command (or step) to take next.
	Next string
}

// Line renders the RESULT line.
func (r commandResult) Line() string {
	var b strings.Builder
	b.WriteString("RESULT: ")
	b.WriteString(emptyAs(r.Status, "failed"))
	for _, kv := range [][2]string{
		{"stage", r.Stage}, {"env", r.Env}, {"url", r.URL}, {"release", r.Release},
		{"claim", r.Claim}, {"action", r.Action}, {"reason", r.Reason},
	} {
		if kv[1] == "" {
			continue
		}
		b.WriteString(" ")
		b.WriteString(kv[0])
		b.WriteString("=")
		b.WriteString(resultValue(kv[1]))
	}
	if next := oneLineText(r.Next); next != "" {
		b.WriteString(" next: ")
		b.WriteString(next)
	}
	return b.String()
}

// resultValue quotes a value that would not survive splitting on spaces.
func resultValue(v string) string {
	v = oneLineText(v)
	if strings.ContainsAny(v, " \t\"=") {
		return strconv.Quote(v)
	}
	return v
}

// statusForExit names an exit code. The table is exitcodes.go's: 2 is "could
// not look", 5 "nobody approved the plan", 7 "accepted and waiting on a
// person", 8 "still in progress when the budget ran out". Every other non-zero
// code is a failure.
func statusForExit(code int) string {
	switch code {
	case 0:
		return "ok"
	case exitUndetermined:
		return "undetermined"
	case exitPlanUnconfirmed:
		return "unconfirmed"
	case exitQueued:
		return "queued"
	case exitTimedOut:
		return "pending"
	default:
		return "failed"
	}
}

// resultStatus is the status err produces. An error that asks for exit 0 (a
// confirmation answered no) wrote nothing and failed nothing: declined.
func resultStatus(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) && coded.ExitCode() == 0 {
		return "declined"
	}
	return statusForExit(exitcode.Of(err))
}

// maxResultReason bounds the reason: the full error is printed right above.
const maxResultReason = 160

// reasonOf summarizes the error in one bounded line. A first line ending
// in a colon introduces a detail; keep the next nonblank line too instead
// of ending the summary just before the cause. The full error stays above.
func reasonOf(err error) string {
	if err == nil {
		return ""
	}
	header, rest, _ := strings.Cut(err.Error(), "\n")
	msg := strings.TrimSpace(header)
	if msg == "" || strings.HasSuffix(msg, ":") {
		for line := range strings.SplitSeq(rest, "\n") {
			detail := strings.TrimSpace(line)
			if detail != "" {
				msg = strings.TrimSpace(msg + " " + detail)
				if len([]rune(msg)) > maxResultReason && strings.HasSuffix(strings.TrimSpace(header), ":") {
					// Prioritize the cause when the introductory line uses
					// up the budget. The full summary still appears above.
					msg = detail
				}
				break
			}
		}
	}
	if r := []rune(msg); len(r) > maxResultReason {
		msg = string(r[:maxResultReason-1]) + "…"
	}
	return msg
}

// fixOf is the remedy an error names on its own line: a line that starts
// with "fix:" or "Fix:" (cliutil.UserErr's shape and the deploy errors'),
// or "" when it names none.
func fixOf(err error) string {
	if err == nil {
		return ""
	}
	for _, line := range strings.Split(err.Error(), "\n") {
		line = strings.TrimSpace(line)
		for _, p := range []string{"fix: ", "Fix: "} {
			if rest, ok := strings.CutPrefix(line, p); ok && strings.TrimSpace(rest) != "" {
				return strings.TrimSuffix(strings.TrimSpace(rest), ".")
			}
		}
	}
	return ""
}

// resultError is an error with the RESULT line appended, so the line is the
// last thing the host prints. It keeps the wrapped error's identity and exit
// code.
type resultError struct {
	err  error
	line string
}

func (e *resultError) Error() string { return strings.TrimRight(e.err.Error(), "\n") + "\n" + e.line }
func (e *resultError) Unwrap() error { return e.err }

// withResultLine appends r's RESULT line to err. A nil err stays nil: success
// prints its line itself, as the last thing the command writes.
func withResultLine(err error, r commandResult) error {
	if err == nil {
		return nil
	}
	var already *resultError
	if errors.As(err, &already) {
		return err
	}
	return &resultError{err: err, line: r.Line()}
}
