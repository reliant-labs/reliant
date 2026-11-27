// Package experimentstats turns an experiment's per-arm counts into a
// fixed-horizon verdict: Wilson intervals per arm, lift with a Newcombe
// interval and a two-proportion z-test per treatment, and a sample-ratio
// mismatch check. It is pure (stdlib math only, no I/O), so the control plane
// and a local reader of a Forge Changes experiment reach the same verdict from
// the same counts; pkg/obsquery's ExperimentReadoutQuery produces them.
//
// # Method
//
// The metric is binary: did a unit convert within its attribution window.
// Counts are intent-to-treat (a unit is in the arm it was assigned, whether or
// not it saw the treatment), and only units whose window is complete
// (Arm.Units) enter the effect estimate; the rest are pending, never
// non-converters.
//
//   - Per arm: the conversion rate and its Wilson score interval at 1-Alpha
//     (95% by default). Wilson stays inside [0, 1] and keeps its coverage at
//     small n and at rates near 0 or 1, where the normal (Wald) interval does
//     not.
//   - Per treatment against the control: the absolute lift (p_t - p_c), the
//     relative lift ((p_t - p_c) / p_c, undefined when p_c is 0), the pooled
//     two-proportion z-test's two-sided p-value, and Newcombe's hybrid score
//     interval for p_t - p_c (Newcombe 1998, method 10), built from the two
//     Wilson intervals, for the same small-n reasons.
//   - Several treatments are corrected for multiple comparisons with
//     Bonferroni: each comparison is tested at Alpha/k (k treatments) and its
//     difference interval is at 1-Alpha/k. Conservative, and simple to state.
//   - A treatment is better (worse) only when BOTH the p-value is below its
//     level and the difference interval excludes zero on the positive
//     (negative) side, so the verdict never contradicts a number shown beside
//     it. "Better" means a higher conversion rate.
//   - Sample-ratio mismatch: a chi-square goodness-of-fit test of every
//     arm's assigned units (window complete or not) against the planned split,
//     flagged at p < SRMAlpha (0.001). A mismatch means assignment or its
//     recording is broken (a redirect that drops one arm's events, a crash
//     before the record), so every effect is untrustworthy: VerdictSRMFailed
//     overrides every other verdict. Units assigned to an arm the plan does
//     not have fail it outright. The test is skipped while any expected count
//     is below 5, where the chi-square approximation does not hold.
//
// # Fixed horizon: no peeking
//
// This is a fixed-horizon test. Its error rates hold only when the sample size
// (MinSamplePerArm, and the run's planned duration) is chosen before launch and
// the verdict is read once, at that horizon. Reading it repeatedly and stopping
// at the first significant result inflates the false-positive rate far beyond
// Alpha (to roughly 20-30% over a few dozen looks at Alpha = 0.05). Before the
// horizon a readout is interim: below MinSamplePerArm the verdict is always
// inconclusive, and a UI shows the pre-registered horizon beside any interim
// number rather than a winner. Sequential testing (always-valid p-values) is
// not in v1.
package experimentstats
