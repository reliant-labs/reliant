/**
 * The escalation policy, pinned.
 *
 * The behaviour that matters here is mostly about what we DON'T say: we never
 * report failure the control plane hasn't declared, and we never describe a
 * suspended machine as if it were booting. Those are the two ways the old
 * per-surface copy misled people, so they get explicit tests.
 */

import { describe, expect, it } from "vitest";

import {
  DaemonLifecyclePhase,
  DaemonStatus,
} from "@/gen/controlplane/v1/public/shared_pb";
import type { Daemon } from "@/services/controlPlane/daemon";
import {
  classifyDaemonWait,
  daemonWaitSummary,
  DAEMON_WAIT_SLOW_MS,
  DAEMON_WAIT_STUCK_MS,
  MACHINE_NOUN,
} from "../daemon-wait";

function daemon(partial: Partial<Daemon>): Daemon {
  return partial as Daemon;
}

describe("classifyDaemonWait", () => {
  describe("a cloud machine that is still coming up", () => {
    it("reads as a calm wait early on", () => {
      const state = classifyDaemonWait({ elapsedMs: 0, isCloud: true });

      expect(state.tone).toBe("waiting");
      expect(state.shouldRetry).toBe(true);
      // Nothing to act on yet — offering buttons this early is noise.
      expect(state.showRetry).toBe(false);
      expect(state.showManage).toBe(false);
    });

    it("admits it is slow once past the threshold, without claiming failure", () => {
      const state = classifyDaemonWait({
        elapsedMs: DAEMON_WAIT_SLOW_MS,
        isCloud: true,
      });

      expect(state.tone).toBe("slow");
      expect(state.shouldRetry).toBe(true);
    });

    it("surfaces the exits when the wait runs long — but still does not fail", () => {
      const state = classifyDaemonWait({
        elapsedMs: DAEMON_WAIT_STUCK_MS,
        isCloud: true,
      });

      // This is the regression that matters. The old code turned 60s into
      // "Couldn't connect to your environment." for a machine that was
      // usually still on its way. We escalate and offer help instead.
      expect(state.tone).not.toBe("failed");
      expect(state.shouldRetry).toBe(true);
      expect(state.showRetry).toBe(true);
      expect(state.showManage).toBe(true);
    });

    it("never invents a failure, no matter how long the wait runs", () => {
      const state = classifyDaemonWait({
        elapsedMs: 60 * 60 * 1000,
        isCloud: true,
      });

      expect(state.tone).not.toBe("failed");
      expect(state.shouldRetry).toBe(true);
    });
  });

  describe("when the control plane reports a real problem", () => {
    it("reports failure and stops retrying on FAILED", () => {
      const state = classifyDaemonWait({
        daemon: daemon({ status: DaemonStatus.FAILED }),
        elapsedMs: 0,
        isCloud: true,
      });

      expect(state.tone).toBe("failed");
      expect(state.shouldRetry).toBe(false);
      expect(state.showRetry).toBe(true);
    });

    it("passes the backend's own reason through verbatim", () => {
      const state = classifyDaemonWait({
        daemon: daemon({
          status: DaemonStatus.FAILED,
          lastStatusMessage: "image pull failed: manifest unknown",
        }),
        elapsedMs: 0,
        isCloud: true,
      });

      // The whole point of plumbing this through: the actionable detail is
      // the backend's, not ours.
      expect(state.reason).toBe("image pull failed: manifest unknown");
    });

    it("treats whitespace-only status messages as absent", () => {
      const state = classifyDaemonWait({
        daemon: daemon({ status: DaemonStatus.FAILED, lastStatusMessage: "   " }),
        elapsedMs: 0,
        isCloud: true,
      });

      expect(state.reason).toBeNull();
      // With no reason, our own fallback copy has to carry the explanation.
      expect(state.detail).not.toBeNull();
    });

    it("describes a suspended machine as stopped, not starting", () => {
      const state = classifyDaemonWait({
        daemon: daemon({ status: DaemonStatus.SUSPENDED }),
        elapsedMs: 0,
        isCloud: true,
      });

      // Nothing is in flight, so a spinner would be a lie and retrying alone
      // will never wake it — the user needs an affordance.
      expect(state.shouldRetry).toBe(false);
      expect(state.showRetry).toBe(true);
      expect(state.title.toLowerCase()).toContain("suspended");
    });

    it("keeps retrying a disconnected machine, which may come back on its own", () => {
      const state = classifyDaemonWait({
        daemon: daemon({ status: DaemonStatus.DISCONNECTED }),
        elapsedMs: 0,
        isCloud: true,
      });

      expect(state.shouldRetry).toBe(true);
      expect(state.title.toLowerCase()).toContain("disconnected");
    });
  });

  describe("self-hosted", () => {
    it("does not promise provisioning that isn't happening", () => {
      const state = classifyDaemonWait({ elapsedMs: 0, isCloud: false });

      // Nothing is being provisioned — a human has to start it.
      expect(state.detail?.toLowerCase()).not.toContain("usually takes");
      expect(state.shouldRetry).toBe(true);
    });

    it("points at the command once the wait drags", () => {
      const state = classifyDaemonWait({
        elapsedMs: DAEMON_WAIT_SLOW_MS,
        isCloud: false,
      });

      expect(state.detail).toContain("reliant daemon start");
      expect(state.showRetry).toBe(true);
    });
  });

  it("uses the same noun everywhere", () => {
    const cases = [
      classifyDaemonWait({ elapsedMs: 0, isCloud: true }),
      classifyDaemonWait({ elapsedMs: 0, isCloud: false }),
      classifyDaemonWait({
        daemon: daemon({ status: DaemonStatus.FAILED }),
        elapsedMs: 0,
        isCloud: true,
      }),
    ];

    // The bug being prevented: five surfaces calling one thing a daemon, an
    // environment, a workspace, and a machine.
    for (const state of cases) {
      expect(state.title.toLowerCase()).toContain("machine");
      expect(state.title.toLowerCase()).not.toContain("daemon");
      expect(state.title.toLowerCase()).not.toContain("environment");
    }
  });
});

describe("the reported phase drives what the wait says", () => {
  it("names the repository clone, the step most likely to look like a hang", () => {
    const state = classifyDaemonWait({
      daemon: daemon({ lifecyclePhase: DaemonLifecyclePhase.CLONING }),
      elapsedMs: 0,
      isCloud: true,
    });

    expect(state.title).toBe("Cloning your repository");
  });

  it("distinguishes getting compute from cloning", () => {
    const provisioning = classifyDaemonWait({
      daemon: daemon({ lifecyclePhase: DaemonLifecyclePhase.PROVISIONING }),
      elapsedMs: 0,
      isCloud: true,
    });
    const cloning = classifyDaemonWait({
      daemon: daemon({ lifecyclePhase: DaemonLifecyclePhase.CLONING }),
      elapsedMs: 0,
      isCloud: true,
    });

    // The whole point of the plumbing: these two are DAEMON_STATUS_PENDING
    // alike, and used to be indistinguishable to a waiting user.
    expect(provisioning.title).not.toBe(cloning.title);
  });

  it("explains that a large repo is legitimately slow once the wait drags", () => {
    const state = classifyDaemonWait({
      daemon: daemon({ lifecyclePhase: DaemonLifecyclePhase.CLONING }),
      elapsedMs: DAEMON_WAIT_SLOW_MS,
      isCloud: true,
    });

    // Reassurance, specifically so a slow clone doesn't read as a failure.
    expect(state.detail?.toLowerCase()).toContain("large repositories");
    expect(state.tone).not.toBe("failed");
  });

  it("keeps the phase in the copy even when the wait runs long", () => {
    const state = classifyDaemonWait({
      daemon: daemon({ lifecyclePhase: DaemonLifecyclePhase.CLONING }),
      elapsedMs: DAEMON_WAIT_STUCK_MS,
      isCloud: true,
    });

    // Escalation must not throw away the detail that explains the wait.
    expect(state.title).toBe("Cloning your repository");
    expect(state.showManage).toBe(true);
    expect(state.tone).not.toBe("failed");
  });

  it("falls back to generic copy when no phase is reported", () => {
    const withoutPhase = classifyDaemonWait({ elapsedMs: 0, isCloud: true });
    const unspecified = classifyDaemonWait({
      daemon: daemon({ lifecyclePhase: DaemonLifecyclePhase.UNSPECIFIED }),
      elapsedMs: 0,
      isCloud: true,
    });

    // Self-hosted machines and pre-migration rows report nothing; both must
    // still produce sensible copy rather than an empty or wrong step.
    expect(withoutPhase.title).toBe(`Starting your ${MACHINE_NOUN}`);
    expect(unspecified.title).toBe(withoutPhase.title);
  });

  it("lets a terminal status outrank the phase", () => {
    const state = classifyDaemonWait({
      daemon: daemon({
        status: DaemonStatus.FAILED,
        lifecyclePhase: DaemonLifecyclePhase.CLONING,
      }),
      elapsedMs: 0,
      isCloud: true,
    });

    // A machine the backend calls FAILED is not "cloning", whatever phase was
    // last recorded — status is the more certain signal.
    expect(state.tone).toBe("failed");
    expect(state.shouldRetry).toBe(false);
  });
});

describe("daemonWaitSummary", () => {
  it("appends the backend reason when there is one", () => {
    const state = classifyDaemonWait({
      daemon: daemon({
        status: DaemonStatus.FAILED,
        lastStatusMessage: "quota exceeded",
      }),
      elapsedMs: 0,
      isCloud: true,
    });

    expect(daemonWaitSummary(state)).toContain("quota exceeded");
  });

  it("is just the title when there is no reason", () => {
    const state = classifyDaemonWait({ elapsedMs: 0, isCloud: true });
    expect(daemonWaitSummary(state)).toBe(state.title);
  });
});
