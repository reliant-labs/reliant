/**
 * Tests for the ComputeStep auto-skip detection helper.
 *
 * The "where do you want to run your daemon?" prompt is bootstrap-only —
 * once a daemon is registered for the user (local OR managed), we treat
 * the prompt as answered and advance. The cloud-dev `make dev-electron`
 * flow pairs a local tools-daemon with the Electron app: between the
 * local-daemon-registers-with-the-control-plane moment and the lifecycle
 * status flipping to ACTIVE, the row briefly sits at IDLE. We must skip in
 * BOTH states so the user doesn't see the prompt flash and pre-empt the
 * auto-advance by clicking through.
 *
 * Stale rows that are DISCONNECTED (or UNSPECIFIED, defensively) are NOT
 * treated as usable — those signal a daemon the user genuinely needs to
 * re-decide about, and auto-skipping would leave them stuck with no live
 * daemon at the next step.
 */
import { describe, expect, it } from "vitest";
import type { DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { hasUsableDaemonForOnboarding } from "../steps/ComputeStep";

// DaemonInfo is a proto-generated message; tests build partial instances by
// casting through `unknown` so we only spell out the fields under test.
function makeDaemon(partial: Partial<DaemonInfo>): DaemonInfo {
  return partial as unknown as DaemonInfo;
}

describe("hasUsableDaemonForOnboarding", () => {
  it("returns false for an empty list (fresh signup, no daemons yet)", () => {
    expect(hasUsableDaemonForOnboarding([])).toBe(false);
  });

  it("returns true for a single ACTIVE local daemon (the dev-electron case)", () => {
    // make dev-electron's paired local daemon, post-NATS-connect-event.
    // Without this skip, the user would see the bootstrap "where" prompt
    // immediately after sign-in even though their daemon is already live.
    expect(
      hasUsableDaemonForOnboarding([
        makeDaemon({
          daemonId: "local-1",
          daemonType: "self_hosted",
          status: DaemonStatus.ACTIVE,
        }),
      ]),
    ).toBe(true);
  });

  it("returns true for a single ACTIVE managed (cloud) daemon", () => {
    expect(
      hasUsableDaemonForOnboarding([
        makeDaemon({
          daemonId: "managed-1",
          daemonType: "managed",
          status: DaemonStatus.ACTIVE,
        }),
      ]),
    ).toBe(true);
  });

  it("returns true for an IDLE daemon — the gap between register and ACTIVE", () => {
    // The control-plane shim maps controlplane.v1 PENDING/SUSPENDED →
    // reliant.v1 IDLE. A managed daemon mid-provision or a local daemon
    // whose NATS event hasn't been consumed yet sits here.
    expect(
      hasUsableDaemonForOnboarding([
        makeDaemon({
          daemonId: "idle-1",
          daemonType: "self_hosted",
          status: DaemonStatus.IDLE,
        }),
      ]),
    ).toBe(true);
  });

  it("returns false when the only daemon is DISCONNECTED — stale row", () => {
    // A stale row from a prior session, with no live daemon attached. The
    // user needs to see the "where" prompt so they can start a fresh one
    // (or pick cloud), rather than silently advancing to a broken state.
    expect(
      hasUsableDaemonForOnboarding([
        makeDaemon({
          daemonId: "stale-1",
          daemonType: "self_hosted",
          status: DaemonStatus.DISCONNECTED,
        }),
      ]),
    ).toBe(false);
  });

  it("returns false when the only daemon is UNSPECIFIED — defensive", () => {
    // A daemon row whose status didn't get mapped (e.g. controlplane.v1
    // UNSPECIFIED falling through the shim's switch). Treat as not-usable
    // so we don't auto-skip into a broken setup.
    expect(
      hasUsableDaemonForOnboarding([
        makeDaemon({
          daemonId: "weird-1",
          status: DaemonStatus.UNSPECIFIED,
        }),
      ]),
    ).toBe(false);
  });

  it("returns true if ANY daemon in the list is ACTIVE / IDLE", () => {
    // Mixed list: one stale + one live. The user clearly has a usable
    // daemon — the post-onboarding picker handles disambiguating which.
    expect(
      hasUsableDaemonForOnboarding([
        makeDaemon({
          daemonId: "stale-1",
          status: DaemonStatus.DISCONNECTED,
        }),
        makeDaemon({
          daemonId: "live-1",
          status: DaemonStatus.ACTIVE,
        }),
      ]),
    ).toBe(true);
  });
});
