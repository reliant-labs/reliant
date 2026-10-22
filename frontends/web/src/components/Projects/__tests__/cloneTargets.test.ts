import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  cloneAvailability,
  cloneDescription,
  cloneTargetOptions,
  describeCloneError,
  failureReason,
  isCloneTargetGone,
  isCloneableDaemon,
  pickCloneTarget,
} from "../cloneTargets";
import {
  DaemonStatus,
  type DaemonInfo as CloudDaemon,
} from "../../../gen/reliant/v1/daemon_registry_pb";

const DAEMON_STATUS_ACTIVE = DaemonStatus.ACTIVE;
const DAEMON_STATUS_FAILED = DaemonStatus.FAILED;
const DAEMON_STATUS_PENDING = DaemonStatus.PENDING;
const DAEMON_STATUS_SUSPENDED = DaemonStatus.SUSPENDED;

// The picker used to hide "Clone repo" unless a daemon was ACTIVE. A user
// whose only machine had FAILED therefore had no way to add a project from
// any surface in the app — the dead end these tests pin shut. See
// docs/findings/add-github-project-2026-09-30.md.

function daemon(overrides: Partial<CloudDaemon> & { status: number }): CloudDaemon {
  return {
    daemonId: "daemon-1",
    hosthostname: "machine",
    status: overrides.status,
    lastStatusMessage: "",
    ...overrides,
  } as CloudDaemon;
}

/** A daemon whose most recent connection is `secondsAgo` old. */
function daemonSeenAt(
  overrides: Partial<CloudDaemon> & { status: number },
  secondsAgo: number,
): CloudDaemon {
  return daemon({
    ...overrides,
    connectedAt: { seconds: BigInt(1790000000 - secondsAgo), nanos: 0 },
  } as Partial<CloudDaemon> & { status: number });
}

describe("cloneAvailability", () => {
  it("offers an active machine as an immediate target", () => {
    const active = daemon({ daemonId: "d-active", status: DAEMON_STATUS_ACTIVE });
    const result = cloneAvailability([active]);

    expect(result).toMatchObject({ kind: "ready", immediate: true });
    if (result.kind === "ready") expect(result.target.daemonId).toBe("d-active");
  });

  it("allows cloning onto a machine that is still starting", () => {
    // The clone is durably queued and drained when the daemon connects, so a
    // PENDING machine is a valid target — blocking here would strand a user
    // who just created their first machine.
    const result = cloneAvailability([daemon({ daemonId: "d-pending", status: DAEMON_STATUS_PENDING })]);

    expect(result.kind).toBe("ready");
    if (result.kind === "ready") {
      expect(result.target.daemonId).toBe("d-pending");
      expect(result.immediate).toBe(false);
    }
  });

  it("allows cloning onto a suspended machine", () => {
    const result = cloneAvailability([
      daemon({ daemonId: "d-susp", status: DAEMON_STATUS_SUSPENDED }),
    ]);

    expect(result.kind).toBe("ready");
    if (result.kind === "ready") expect(result.target.daemonId).toBe("d-susp");
  });

  it("blocks with a reason when every machine has failed", () => {
    const result = cloneAvailability([daemon({ daemonId: "d-failed", status: DAEMON_STATUS_FAILED })]);

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
      daemon({ daemonId: "d-pending", status: DAEMON_STATUS_PENDING }),
      daemon({ daemonId: "d-active", status: DAEMON_STATUS_ACTIVE }),
    ]);

    if (result.kind !== "ready") throw new Error("expected a ready target");
    expect(result.target.daemonId).toBe("d-active");
    expect(result.immediate).toBe(true);
  });

  it("ignores failed machines when a usable one exists", () => {
    const result = cloneAvailability([
      daemon({ daemonId: "d-failed", status: DAEMON_STATUS_FAILED }),
      daemon({ daemonId: "d-pending", status: DAEMON_STATUS_PENDING }),
    ]);

    if (result.kind !== "ready") throw new Error("expected a ready target");
    expect(result.target.daemonId).toBe("d-pending");
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

  it("defaults to the most recently used active machine", () => {
    // With several machines the default must be the one the user was last
    // working on, not whichever the server happened to list first — a clone
    // that silently lands on a stale machine is the mistake this prevents.
    const target = pickCloneTarget([
      daemonSeenAt({ daemonId: "d-stale", status: DAEMON_STATUS_ACTIVE }, 86400),
      daemonSeenAt({ daemonId: "d-recent", status: DAEMON_STATUS_ACTIVE }, 60),
      daemonSeenAt({ daemonId: "d-middling", status: DAEMON_STATUS_ACTIVE }, 3600),
    ]);

    expect(target?.daemonId).toBe("d-recent");
  });

  it("still prefers any active machine over a more recently used inactive one", () => {
    // Recency orders machines WITHIN a status tier; it does not promote a
    // suspended machine over a running one, because the running one clones
    // now and the suspended one clones whenever it wakes.
    const target = pickCloneTarget([
      daemonSeenAt({ daemonId: "d-susp-recent", status: DAEMON_STATUS_SUSPENDED }, 10),
      daemonSeenAt({ daemonId: "d-active-old", status: DAEMON_STATUS_ACTIVE }, 99999),
    ]);

    expect(target?.daemonId).toBe("d-active-old");
  });
});

describe("cloneTargetOptions", () => {
  it("offers every machine that can take a clone, most recent first", () => {
    const options = cloneTargetOptions([
      daemonSeenAt({ daemonId: "d-old", status: DAEMON_STATUS_ACTIVE }, 900),
      daemonSeenAt({ daemonId: "d-new", status: DAEMON_STATUS_ACTIVE }, 30),
      daemonSeenAt({ daemonId: "d-pending", status: DAEMON_STATUS_PENDING }, 5),
    ]);

    expect(options.map((o) => o.daemon.daemonId)).toEqual(["d-new", "d-old", "d-pending"]);
  });

  it("omits failed machines — the user cannot usefully choose one", () => {
    const options = cloneTargetOptions([
      daemon({ daemonId: "d-failed", status: DAEMON_STATUS_FAILED }),
      daemon({ daemonId: "d-ok", status: DAEMON_STATUS_ACTIVE }),
    ]);

    expect(options.map((o) => o.daemon.daemonId)).toEqual(["d-ok"]);
  });

  it("says whether each machine clones now or only once it is ready", () => {
    const options = cloneTargetOptions([
      daemon({ daemonId: "d-active", status: DAEMON_STATUS_ACTIVE }),
      daemon({ daemonId: "d-pending", status: DAEMON_STATUS_PENDING }),
    ]);

    expect(options.find((o) => o.daemon.daemonId === "d-active")?.immediate).toBe(true);
    expect(options.find((o) => o.daemon.daemonId === "d-pending")?.immediate).toBe(false);
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

describe("describeCloneError", () => {
  // The raw text on 2026-10-07 was `[not_found] daemon "cda8a89b-…" not found`:
  // a UUID and no way forward.
  it("turns a not_found from the clone dispatch into something the user can act on", () => {
    const err = new ConnectError(`daemon "cda8a89b-d15c-455d-a506-4c40335e4278" not found`, Code.NotFound);
    const message = describeCloneError(err, "default");
    expect(message).toContain("default no longer exists");
    expect(message).toMatch(/choose another machine/i);
    expect(message).not.toContain("cda8a89b");
    expect(isCloneTargetGone(err)).toBe(true);
  });

  it("passes the control plane's own message through for other refusals", () => {
    const err = new ConnectError(`no git credential found for provider "github"`, Code.FailedPrecondition);
    expect(describeCloneError(err, "default")).toBe(`no git credential found for provider "github"`);
    expect(isCloneTargetGone(err)).toBe(false);
  });
});

describe("cloneDescription", () => {
  it("names the machine and never promises the clone will happen", () => {
    const target = daemon({ daemonId: "d1", status: DAEMON_STATUS_SUSPENDED, name: "default", daemonType: "managed" });
    const queued = cloneDescription({
      cloneState: { kind: "ready", target, immediate: false },
      hasGitHubCredential: true,
    });
    expect(queued).toContain("default");
    expect(queued).toMatch(/queue/i);
    expect(queued).not.toMatch(/will clone when|it'll clone when/i);
  });
});
