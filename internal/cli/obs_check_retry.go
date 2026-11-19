package cli

// What `forge obs check` does when the GetReleaseHealth read itself fails.
//
// A read the control plane refused because an allowance is spent judged
// nothing: the org's hourly query quota (ResourceExhausted, reason
// obs_query_quota, with Retry-After until the quota refills at the top of the
// hour), or a throttle on this caller (in_flight_limit, rate_limited, a bare
// 429 with Retry-After). Reporting it as exit 2, "not proven good", made a gate
// settle a release that nobody had looked at (v1.21.0-rc.6 on 2026-11-17,
// refused because an issue-triage burst had spent the org's agent-lane quota).
// So under --wait a refusal is waited out until --timeout, and a refusal the
// wait cannot outlast, or one without --wait, is exit 8 with reason_code
// quota_exhausted or rate_limited: check again later.
//
// A transient fault (Unavailable, DeadlineExceeded, a 502/503/504, a
// transport failure) is retried under --wait with a backoff, because the
// control plane rolling out its own release is exactly when a release check
// runs. One that outlasts the wait keeps its exit 2: a control plane that
// stayed unreachable for the whole budget is "could not look". Without --wait
// it is exit 2 at once, as before.
//
// Every other failure keeps its mapping (MapCloudError).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
)

// The reason codes of a check that judged nothing because its read was
// refused. They share reason_code with releasehealth's INCONCLUSIVE codes, but
// come with exit 8, never a verdict.
const (
	checkReasonQuotaExhausted = "quota_exhausted"
	checkReasonRateLimited    = "rate_limited"
)

const (
	// obsCheckMaxRefusalWait caps one wait on a refusal. A quota refills at
	// the top of the hour, so its Retry-After can be most of an hour; asking
	// again every few minutes notices a quota raised or reset early.
	obsCheckMaxRefusalWait = 5 * time.Minute
	// obsCheckRefusalWait is the wait on a refusal that says nothing of when
	// to ask again.
	obsCheckRefusalWait = time.Minute
	// obsCheckMaxFaultWait caps the backoff on a transient fault.
	obsCheckMaxFaultWait = 2 * time.Minute
	// obsCheckFinalReadMargin is the part of --timeout kept for the final
	// read after a transient fault: the backoff stops this long before the
	// deadline, so the last read is made inside it.
	obsCheckFinalReadMargin = 10 * time.Second
)

// obsCheckJitter is added to each wait on a failed read, so checks refused by
// one quota do not all ask again at the instant it refills. A seam: tests
// return 0.
var obsCheckJitter = func(d time.Duration) time.Duration {
	span := min(d/10, 30*time.Second)
	if span <= 0 {
		return 0
	}
	return rand.N(span)
}

// checkRetry is a failed GetReleaseHealth read that a later read can get past.
type checkRetry struct {
	// code is the document's reason_code for a refusal (quota_exhausted,
	// rate_limited); "" for a transient fault.
	code string
	// wait is how long until another read can succeed: the refusal's
	// Retry-After or the quota's refill, else a default or a backoff.
	wait time.Duration
	// at is when the refused allowance refills; zero when unsaid.
	at time.Time
	// why says what failed, for the progress line and the error.
	why string
}

// checkRetryOf classifies err, a failed GetReleaseHealth read at now; faults
// is how many transient faults in a row this one makes. ok is false for a
// failure no later read can change.
func checkRetryOf(err error, now time.Time, faults int) (r checkRetry, ok bool) {
	f := obsbackend.FailureOf(err)
	if f.Code == cloud.CodeResourceExhausted || f.HTTPStatus == http.StatusTooManyRequests {
		switch {
		case f.Class == obsbackend.ClassQuota && (f.RetryAfter > 0 || !f.ResetsAt.IsZero() || f.Reason != ""):
			// A ResourceExhausted that says neither its reason nor when to
			// retry is not known to be a quota: it keeps exit 2.
			r.code, r.why = checkReasonQuotaExhausted, "the control plane's query quota is exhausted"
		case f.Class == obsbackend.ClassInFlight, f.Class == obsbackend.ClassRateLimited,
			f.Code == "" && f.RetryAfter > 0:
			r.code, r.why = checkReasonRateLimited, "the control plane is throttling this caller"
		default:
			return r, false
		}
		if f.Reason != "" {
			r.why += " (" + f.Reason + ")"
		}
		r.wait, r.at = f.RetryAfter, f.ResetsAt
		if r.wait <= 0 && !r.at.IsZero() {
			r.wait = r.at.Sub(now)
		}
		if r.at.IsZero() && f.RetryAfter > 0 {
			r.at = now.Add(f.RetryAfter)
		}
		if r.wait <= 0 {
			r.wait = obsCheckRefusalWait
		}
		return r, true
	}
	switch {
	case f.Code == cloud.CodeUnavailable, f.Code == cloud.CodeDeadlineExceeded:
		r.why = "the control plane answered " + f.Code
	case f.Code == "" && (f.HTTPStatus == http.StatusBadGateway || f.HTTPStatus == http.StatusServiceUnavailable || f.HTTPStatus == http.StatusGatewayTimeout):
		r.why = fmt.Sprintf("the control plane answered HTTP %d", f.HTTPStatus)
	case f.Code == "" && f.HTTPStatus == 0 && transportFailure(err):
		r.why = "the control plane could not be reached"
	default:
		return r, false
	}
	r.wait = f.RetryAfter
	if r.wait <= 0 {
		r.wait = min(obsCheckPollEvery<<min(max(faults-1, 0), 4), obsCheckMaxFaultWait)
	}
	return r, true
}

// transportFailure says err is the request failing on the wire (a dial, a
// reset connection, a body cut short), not a failure forge made itself before
// sending: only the first can go away by asking again.
func transportFailure(err error) bool {
	var ue *url.Error
	var ne net.Error
	return errors.As(err, &ue) || errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF)
}

// sleepFor is how long to sleep before asking again: the wait, capped,
// jittered, and never past until (retryUntil).
func (r checkRetry) sleepFor(now, until time.Time) time.Duration {
	limit := obsCheckMaxFaultWait
	if r.code != "" {
		limit = obsCheckMaxRefusalWait
	}
	d := min(r.wait, limit)
	d += obsCheckJitter(d)
	return max(min(d, until.Sub(now)), 0)
}

// checkPoll is what every GetReleaseHealth read of one check shares: the
// reader, the env it judges, the flags and the --timeout deadline.
type checkPoll struct {
	d        obsDeps
	reader   obsbackend.HealthReader
	info     obsbackend.Info
	env      string
	o        obsCheckOpts
	deadline time.Time
}

// read is one poll's GetReleaseHealth read. A refused or failed read judged
// nothing: under --wait it is asked again while checkRetryOf says a later read
// can succeed before the deadline. It returns the result, or the document and
// error obs check exits with: exit 8 for a refusal it cannot outlast, the
// failure's own status otherwise.
func (p checkPoll) read(ctx context.Context, req obsbackend.HealthRequest, doc obsCheckDoc) (obsbackend.HealthResult, obsCheckDoc, error) {
	d, o, deadline := p.d, p.o, p.deadline
	bad := func(err error) (obsbackend.HealthResult, obsCheckDoc, error) {
		doc.Suggested = obsbackend.SuggestedOf(err)
		return obsbackend.HealthResult{}, doc, err
	}
	faults := 0 // transient faults in a row, for the backoff
	for {
		req.Now = d.Now()
		res, err := p.reader.ReleaseHealth(ctx, req)
		if err == nil {
			return res, doc, nil
		}
		retry, ok := checkRetryOf(err, d.Now(), faults+1)
		if !ok || ctx.Err() != nil {
			return bad(err)
		}
		until, ok := p.retryUntil(retry, d.Now())
		if !ok && retry.code == "" {
			return bad(err)
		}
		if !ok {
			doc, err = refusedCheck(doc, p.info, p.env, o, retry, err, d.Now().Sub(deadline.Add(-o.timeout)))
			return obsbackend.HealthResult{}, doc, err
		}
		if retry.code == "" {
			faults++
		}
		sleep := retry.sleepFor(d.Now(), until)
		fmt.Fprintf(d.Stderr, "%s; asking again in %s\n", retry.why, sleep.Round(time.Second))
		if err := obsCheckSleep(ctx, sleep); err != nil {
			return bad(obsbackend.Undetermined("interrupted while waiting").Cause(err))
		}
	}
}

// retryUntil says whether a failed read at now is asked again, and the latest
// moment the sleep before it may end. The two failures spend --timeout
// differently:
//   - a refusal (a spent quota, a throttle) is asked again only when its
//     allowance refills before the deadline; a read before the refill would be
//     refused again, so one that refills later is exit 8 at once.
//   - a transient fault may clear at any moment, so it uses all of --timeout:
//     a backoff longer than the time left is cut short, and the final read is
//     made obsCheckFinalReadMargin before the deadline. Only a fault on that
//     final read ends the wait (exit 2).
func (p checkPoll) retryUntil(r checkRetry, now time.Time) (time.Time, bool) {
	if !p.o.wait {
		return time.Time{}, false
	}
	if r.code != "" {
		return p.deadline, now.Add(r.wait).Before(p.deadline)
	}
	last := p.deadline.Add(-obsCheckFinalReadMargin)
	return last, now.Before(last)
}

// refusedCheck is the document and exit-8 error of a check whose read was
// refused and not outlasted. Nothing was judged, so the document has no
// verdict; reason_code says why, retry_at when to ask again.
func refusedCheck(doc obsCheckDoc, info obsbackend.Info, env string, o obsCheckOpts, r checkRetry, cause error, waited time.Duration) (obsCheckDoc, error) {
	doc.Scope.Env, doc.Scope.Backend, doc.Scope.EnvID = env, info.Backend, info.EnvID
	doc.ReasonCode = r.code
	doc.Reason = r.why + "; nothing was judged"
	if !r.at.IsZero() {
		at := r.at.UTC()
		doc.RetryAt = &at
		doc.Reason += ", check again after " + at.Format(time.RFC3339)
	}
	if o.wait {
		doc.Waited = waited.Round(time.Second).String()
	}
	args := []string{"forge", "obs", "check", env}
	switch {
	case o.promotion != "":
		args = append(args, "--promotion", o.promotion)
	case o.release != "":
		args = append(args, "--release", o.release)
	}
	doc.Suggested = obsout.Suggest(obsout.Command(append(args, "--wait", "--json")...))
	msg := doc.Reason
	if o.wait {
		msg = fmt.Sprintf("%s (--wait stopped after %s: --timeout %s ends before another read can succeed)", msg, doc.Waited, o.timeout)
	}
	return doc, exitCodeError{code: exitTimedOut, msg: fmt.Sprintf("%s: %v", msg, cause)}
}
