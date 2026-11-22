package cli

import (
	"fmt"
)

// upResult is a `forge env up` run that came up, as its RESULT line: the env,
// the URL to open first (a frontend's, else a host service's), and the next
// command.
func upResult(env string, rows []upServiceRow, detached bool) commandResult {
	r := commandResult{Status: "ok", Env: env, URL: upPrimaryURL(rows)}
	switch {
	case upHasHostService(rows):
		// Latency and errors per RPC, with no instrumentation: the stack's
		// own telemetry, readable the moment it served a request.
		r.Next = fmt.Sprintf("%s obs endpoints %s", Name(), env)
	case detached:
		r.Next = fmt.Sprintf("%s env status %s", Name(), env)
	default:
		r.Next = "open the url; Ctrl-C stops the stack"
	}
	return r
}

// upPrimaryURL is the URL a person opens first: the first frontend with one,
// else the first host service with one.
func upPrimaryURL(rows []upServiceRow) string {
	for _, kind := range []string{"frontend", "host"} {
		for _, r := range rows {
			if r.Kind == kind && r.URL != "" {
				return r.URL
			}
		}
	}
	return ""
}

func upHasHostService(rows []upServiceRow) bool {
	for _, r := range rows {
		if r.Kind == "host" && r.Port > 0 {
			return true
		}
	}
	return false
}

// upResultOnError appends a failed run's RESULT line to its error: the stage
// is the first phase that ended in an error (up_timings.go), when one did.
func upResultOnError(err error, env, stage string) error {
	if err == nil {
		return nil
	}
	r := commandResult{Status: resultStatus(err), Env: env, Reason: reasonOf(err)}
	if r.Status == "failed" || r.Status == "undetermined" {
		r.Stage = stage
	}
	r.Next = fixOf(err)
	if r.Next == "" {
		r.Next = fmt.Sprintf("fix the error above, then %s env up %s", Name(), env)
	}
	return withResultLine(err, r)
}
