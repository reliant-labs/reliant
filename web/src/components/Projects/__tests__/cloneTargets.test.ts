import { describe, expect, it } from "vitest";
import {
  cloneAvailability,
  failureReason,
  isCloneableDaemon,
  pickCloneTarget,
} from "../cloneTargets";
import {
  DAEMON_STATUS_ACTIVE,
  DAEMON_STATUS_FAILED,
  DAEMON_STATUS_PENDING,
  DAEMON_STATUS_SUSPENDED,
  type Daemon as CloudDaemon,
} from "../../../services/controlPlane/daemon";

// The picker used to hide "Clone repo" unless a daemon was ACTIVE. A user
// whose only machine had FAILED therefore had no way to add a project from
// any surface in the app — the dead end these tests pin shut. See
// docs/findings/add-github-project-2026-09-30.md.

function daemon(overrides: Partial<CloudDaemon> & { status: number }): CloudDaemon {
  return {
    id: "daemon-1",
    name: "machine",
    status: overrides.status,
    lastStatusMessage: "",
    ...overrides,
  } as CloudDaemon;
}

describe("cloneAvailability", () => {
  it("offers an active machine as an immediate target", () => {
    const active = daemon({ id: "d-active", status: DAEMON_STATUS_ACTIVE });
    const result = cloneAvailability([active]);

    expect(result).toMatchObject({ kind: "ready", immediate: true });
    if (result.kind === "ready") expect(result.target.id).toBe("d-active");
  });

  it("allows cloning onto a machine that is still starting", () => {
    // The clone is durably queued and drained when the daemon connects, so a
    // PENDING machine is a valid target — blocking here would strand a user
    // who just created their first machine.
    const result = cloneAvailability([daemon({ id: "d-pending", status: DAEMON_STATUS_PENDING })]);

    expect(result.kind).toBe("ready");
    if (result.kind === "ready") {
      expect(result.target.id).toBe("d-pending");
      expect(result.immediate).toBe(false);
    }
  });

  it("allows cloning onto a suspended machine", () => {
    const result = cloneAvailability([
      daemon({ id: "d-susp", status: DAEMON_STATUS_SUSPENDED }),
    ]);

    expect(result.kind).toBe("ready");
    if (result.kind === "ready") expect(result.target.id).toBe("d-susp");
  });

  it("blocks with a reason when every machine has failed", () => {
    const result = cloneAvailability([daemon({ id: "d-failed", status: DAEMON_STATUS_FAILED })]);

    expect(result.kind).toBe("blocked");
    if (result.kind === "blocked") {
      // The user must be told WHY and what to do, not just shown a
      // disabled control with no explanation.
      expect(result.reason).toMatch(/failed to start/i);
      expect(result.reason).toMatch(/delete/i);
    }
  });

  it("blocks with a distinct reason when the account has no machines", () => {
    const result = cloneAvailability([]);

    expect(result.kind).toBe("blocked");
    if (result.kind === "blocked") expect(result.reason).toMatch(/create a machine/i);
  });

  it("prefers an active machine over one that is still starting", () => {
    const result = cloneAvailability([
      daemon({ id: "d-pending", status: DAEMON_STATUS_PENDING }),
      daemon({ id: "d-active", status: DAEMON_STATUS_ACTIVE }),
    ]);

    if (result.kind !== "ready") throw new Error("expected a ready target");
    expect(result.target.id).toBe("d-active");
    expect(result.immediate).toBe(true);
  });

  it("ignores failed machines when a usable one exists", () => {
    const result = cloneAvailability([
      daemon({ id: "d-failed", status: DAEMON_STATUS_FAILED }),
      daemon({ id: "d-pending", status: DAEMON_STATUS_PENDING }),
    ]);

    if (result.kind !== "ready") throw new Error("expected a ready target");
    expect(result.target.id).toBe("d-pending");
  });
});

describe("isCloneableDaemon", () => {
  it("rejects a failed machine — nothing will ever drain its queue", () => {
    expect(isCloneableDaemon(daemon({ status: DAEMON_STATUS_FAILED }))).toBe(false);
  });

  it("accepts active, pending and suspended machines", () => {
    for (const status of [DAEMON_STATUS_ACTIVE, DAEMON_STATUS_PENDING, DAEMON_STATUS_SUSPENDED]) {
      expect(isCloneableDaemon(daemon({ status }))).toBe(true);
    }
  });
});

describe("pickCloneTarget", () => {
  it("returns null when every machine has failed", () => {
    expect(pickCloneTarget([daemon({ status: DAEMON_STATUS_FAILED })])).toBeNull();
  });
});

describe("failureReason", () => {
  it("surfaces the orchestrator's message when there is one", () => {
    expect(
      failureReason(
        daemon({ status: DAEMON_STATUS_FAILED, lastStatusMessage: "exceeded storage quota" }),
      ),
    ).toBe("exceeded storage quota");
  });

  it("returns null rather than blank copy when no reason was supplied", () => {
    // A blank line pretending to be a reason is worse than none: it reads as
    // a rendering bug and tells the user nothing.
    expect(failureReason(daemon({ status: DAEMON_STATUS_FAILED, lastStatusMessage: "" }))).toBeNull();
    expect(
      failureReason(daemon({ status: DAEMON_STATUS_FAILED, lastStatusMessage: "   " })),
    ).toBeNull();
  });
});
