// The decision logic of check-gate-health.mjs, kept free of `gh` calls so
// gate-health-lib.test.mjs can pin it.

/** A job outcome that means "this gate produced no pass signal". */
export const BAD_CONCLUSIONS = new Set(["failure", "cancelled", "timed_out", "skipped"]);

/**
 * The consecutive bad streak of one job, counted from its newest result.
 *
 * results: [{ conclusion, event }], newest first. `event` is the run's
 * triggering event (`pull_request`, `push`, `schedule`, ...).
 *
 * A single success anywhere clears the streak: one green run proves the gate
 * can still pass, which is the property being monitored.
 *
 * A SKIP ON A PULL REQUEST IS NO EVIDENCE EITHER WAY. pr-ci.yml path-filters
 * its PR jobs (a web-only PR skips the Go lanes, a Go-only PR skips the web
 * lanes), so a run of skipped PR results is the gate working as designed, not
 * going dark. Those results neither extend nor clear the streak. Runs on main
 * and the nightly never path-filter, so a skip THERE still counts as bad: that
 * is how a job whose `if:` broke and stopped running everywhere is caught.
 */
export function badStreak(results) {
  let streak = 0;
  for (const r of results) {
    if (r.conclusion === "skipped" && r.event === "pull_request") continue;
    if (!BAD_CONCLUSIONS.has(r.conclusion)) break;
    streak++;
  }
  return streak;
}
