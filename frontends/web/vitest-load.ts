// How much longer than vitest's idle-box 5s a test may take on this host
// (vite.config.ts, "Vitest sizing"). The factor is read once, when vitest
// loads its config, so the sample must already show the load the run will
// meet:
//  - the busiest of the 1-, 5- and 15-minute load averages, not the 1-minute
//    alone: on a shared machine whose load sustains for hours, a quiet minute
//    at start-up gave a 5s budget to a run that then met load 2-3x the core
//    count (reliant merge gate, 2026-11-25: MobileNewChat 649ms alone, timed
//    out at 5000ms; orgInterceptor 1.0s alone, 18.9s in the gate);
//  - under a `forge test capture` lane (the merge gate's web step and the
//    release gate's web lane), at least 3x: those gates start vitest beside
//    typecheck, vet and the Go tests, a load the averages cannot show yet
//    because it starts with vitest.
// Scaling a timeout never weakens an assertion: a test that passes takes the
// time it takes, and only a hang waits longer to fail.
export const MAX_LOAD_FACTOR = 6;
export const GATE_LANE_MIN_FACTOR = 3;

export function timeoutLoadFactor(loadavg: readonly number[], cores: number, inGateLane: boolean): number {
  const peak = Math.max(0, ...loadavg.filter((n) => Number.isFinite(n)));
  const byLoad = Math.ceil(peak / Math.max(1, cores));
  const floor = inGateLane ? GATE_LANE_MIN_FACTOR : 1;
  return Math.min(MAX_LOAD_FACTOR, Math.max(floor, byLoad));
}
