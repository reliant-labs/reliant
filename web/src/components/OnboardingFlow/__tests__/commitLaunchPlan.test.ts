/**
 * The commit point's four correctness properties.
 *
 * Each test drives a real failure mode rather than restating the
 * implementation. The distinction matters: an idempotency test that calls a
 * function twice and asserts the result is equal passes against code with no
 * idempotency at all, because two identical runs produce two identical results.
 * The assertions here are on the SIDE EFFECTS — how many times the billable
 * call was reached — which is the only thing that can distinguish the two.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  __resetCommitsForTest,
  commitLaunchPlan,
  deriveCommitStatus,
  ensureCommitKey,
  ONBOARDING_DAEMON_NAME,
  retryCommit,
  type CommitDeps,
  type CommitTask,
} from "../commitLaunchPlan";
import type { LaunchPlan } from "../types";

/** controlplane.v1.DaemonStatus */
const ACTIVE = 2;
const SUSPENDED = 3;

function makeDeps(overrides: Partial<CommitDeps> = {}) {
  const deps = {
    grantAiAccess: vi.fn(async () => ({ synced: true })),
    getComputeEligibility: vi.fn(async () => ({
      eligible: true,
      reason: null as string | null,
    })),
    listDaemons: vi.fn(async () => ({
      daemons: [] as Array<{ id: string; status: number }>,
    })),
    createDaemon: vi.fn(async () => "daemon-new"),
    resumeDaemon: vi.fn(async () => undefined),
    // Never actually wait. The eligibility poll is 15 × 2s of real time
    // otherwise, and a test that sleeps is a test people stop running.
    sleep: vi.fn(async () => undefined),
    ...overrides,
  };
  return deps satisfies CommitDeps;
}

const CLOUD_CREDITS_PLAN: Partial<LaunchPlan> = {
  compute: "cloud_paid",
  modelProvider: "reliant_credits",
  commitKey: "key-1",
};

function taskNamed(tasks: CommitTask[], name: CommitTask["name"]): CommitTask {
  const task = tasks.find((t) => t.name === name);
  if (!task) throw new Error(`no task named ${name}`);
  return task;
}

beforeEach(() => {
  __resetCommitsForTest();
});

describe("commitLaunchPlan — idempotency", () => {
  // THE PROPERTY: a webhook can arrive late or twice, a user can double-click,
  // and a reload can re-enter the commit. None may produce a second machine.
  //
  // Asserted on the CALL COUNT of the billable mutation. Asserting that two
  // results are equal would pass against code with no idempotency whatsoever.
  it("provisions once across concurrent commits with the same key", async () => {
    const deps = makeDeps();

    const [a, b, c] = await Promise.all([
      commitLaunchPlan(CLOUD_CREDITS_PLAN, deps),
      commitLaunchPlan(CLOUD_CREDITS_PLAN, deps),
      commitLaunchPlan(CLOUD_CREDITS_PLAN, deps),
    ]);

    expect(deps.createDaemon).toHaveBeenCalledTimes(1);
    expect(deps.grantAiAccess).toHaveBeenCalledTimes(1);
    // All three callers get the same commit, not three independent ones.
    expect(a).toBe(b);
    expect(b).toBe(c);
  });

  it("provisions once when the second commit arrives after the first settled", async () => {
    const deps = makeDeps();

    await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);
    await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);

    expect(deps.createDaemon).toHaveBeenCalledTimes(1);
  });

  // Guards the guard. If `createDaemon` were unreachable for some unrelated
  // reason, every "called once" assertion above would still pass at zero. This
  // proves a DIFFERENT key does reach it, so the deduplication above is the
  // key doing work rather than the call being dead.
  it("a different commit key is a different commit (so the checks above can fail)", async () => {
    const deps = makeDeps();

    await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);
    await commitLaunchPlan({ ...CLOUD_CREDITS_PLAN, commitKey: "key-2" }, deps);

    expect(deps.createDaemon).toHaveBeenCalledTimes(2);
  });

  it("uses the fixed onboarding daemon name, which is the server's own idempotency key", async () => {
    const deps = makeDeps();
    await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);

    expect(deps.createDaemon).toHaveBeenCalledWith(
      expect.objectContaining({ name: ONBOARDING_DAEMON_NAME }),
    );
  });

  it("refuses to commit without a key rather than provisioning unprotected", async () => {
    const deps = makeDeps();
    await expect(
      commitLaunchPlan({ compute: "cloud_paid" }, deps),
    ).rejects.toThrow(/commitKey/);
    expect(deps.createDaemon).not.toHaveBeenCalled();
  });
});

describe("commitLaunchPlan — ordering", () => {
  // THE PROPERTY: AI credit is granted BEFORE compute is provisioned.
  //
  // Not cosmetic. The compute path waits on a webhook-driven entitlement and
  // can sit pending for tens of seconds; the AI grant is a fast local call.
  // Running compute first would delay or strand a grant that had no dependency
  // on it.
  it("grants AI access before it provisions compute", async () => {
    const order: string[] = [];
    const deps = makeDeps({
      grantAiAccess: vi.fn(async () => {
        order.push("grant");
        return { synced: true };
      }),
      createDaemon: vi.fn(async () => {
        order.push("provision");
        return "daemon-new";
      }),
    });

    await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);

    expect(order).toEqual(["grant", "provision"]);
  });

  // A user who has just paid must never be told to upgrade. Eligibility is
  // webhook-driven, so CreateDaemon fired before the subscription row lands
  // returns an entitlement denial — which the global interceptor renders as
  // "please upgrade". The commit waits for the server to agree instead.
  it("waits for entitlement before calling CreateDaemon", async () => {
    const eligibility = vi
      .fn()
      .mockResolvedValueOnce({ eligible: false, reason: "no subscription" })
      .mockResolvedValueOnce({ eligible: false, reason: "no subscription" })
      .mockResolvedValue({ eligible: true, reason: null });
    const deps = makeDeps({ getComputeEligibility: eligibility });

    const result = await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);

    expect(eligibility).toHaveBeenCalledTimes(3);
    expect(deps.createDaemon).toHaveBeenCalledTimes(1);
    expect(result.status).toBe("complete");
  });

  it("reports pending — not failed — when entitlement never lands", async () => {
    const deps = makeDeps({
      getComputeEligibility: vi.fn(async () => ({
        eligible: false,
        reason: "waiting on payment confirmation",
      })),
    });

    const result = await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);

    // Never called: calling into a known denial is what produced "you just
    // paid — please upgrade".
    expect(deps.createDaemon).not.toHaveBeenCalled();
    const task = taskNamed(result.tasks, "provision_daemon");
    expect(task.status).toBe("pending");
    expect(task.detail).toContain("waiting on payment confirmation");
  });
});

describe("commitLaunchPlan — partial failure", () => {
  // THE PROPERTY: AI granted but compute failed must not strand the user.
  // They paid and got something; blocking them in the wizard to punish a
  // provisioning failure is the worst available response.
  it("reports partial, with the server's own reason, when compute fails after AI succeeds", async () => {
    const deps = makeDeps({
      createDaemon: vi.fn(async () => {
        throw new Error("daemon size not allowed on your plan");
      }),
    });

    const result = await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);

    expect(result.status).toBe("partial");
    expect(taskNamed(result.tasks, "grant_ai_access").status).toBe("complete");
    const failed = taskNamed(result.tasks, "provision_daemon");
    expect(failed.status).toBe("failed");
    // The SERVER's reason, not a generic message: "size not allowed" and
    // "minutes exhausted" need different actions from the user.
    expect(failed.detail).toBe("daemon size not allowed on your plan");
  });

  it("reports partial when compute succeeds but the AI grant fails", async () => {
    const deps = makeDeps({
      grantAiAccess: vi.fn(async () => {
        throw new Error("could not issue a managed key");
      }),
    });

    const result = await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);

    expect(result.status).toBe("partial");
    expect(taskNamed(result.tasks, "provision_daemon").status).toBe("complete");
    expect(taskNamed(result.tasks, "grant_ai_access").status).toBe("failed");
  });

  it("never throws — a failing task comes back as data", async () => {
    const deps = makeDeps({
      grantAiAccess: vi.fn(async () => {
        throw new Error("boom");
      }),
      createDaemon: vi.fn(async () => {
        throw new Error("bang");
      }),
    });

    // If this threw, each caller's catch block would decide whether onboarding
    // continues — which is how a paid-up user ends up trapped in the wizard.
    const result = await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);
    expect(result.status).toBe("failed");
  });

  // THE PROPERTY: a billable call must not be retried silently, forever.
  it("does not re-attempt a failed billable call on its own", async () => {
    const deps = makeDeps({
      createDaemon: vi.fn(async () => {
        throw new Error("provisioning unavailable");
      }),
    });

    await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);
    await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);
    await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);

    expect(deps.createDaemon).toHaveBeenCalledTimes(1);
  });

  it("retries only when a user explicitly asks", async () => {
    const deps = makeDeps({
      createDaemon: vi
        .fn()
        .mockRejectedValueOnce(new Error("provisioning unavailable"))
        .mockResolvedValue("daemon-new"),
    });

    const first = await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);
    expect(first.status).toBe("partial");

    retryCommit("key-1");
    const second = await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);

    expect(second.status).toBe("complete");
    expect(deps.createDaemon).toHaveBeenCalledTimes(2);
  });
});

describe("commitLaunchPlan — what each plan actually commits", () => {
  // The free path: local compute with your own key. Nothing is created, and
  // the commit still reports success rather than a vacuous partial.
  it("commits a local + own-key plan without touching a single mutation", async () => {
    const deps = makeDeps();

    const result = await commitLaunchPlan(
      { compute: "local_daemon", modelProvider: "anthropic", commitKey: "k" },
      deps,
    );

    expect(result.status).toBe("complete");
    expect(deps.grantAiAccess).not.toHaveBeenCalled();
    expect(deps.createDaemon).not.toHaveBeenCalled();
    expect(deps.getComputeEligibility).not.toHaveBeenCalled();
    expect(result.tasks.every((t) => t.status === "skipped")).toBe(true);
  });

  it("resumes an existing machine rather than creating a second one", async () => {
    const deps = makeDeps({
      listDaemons: vi.fn(async () => ({
        daemons: [{ id: "d-old", status: SUSPENDED }],
      })),
    });

    const result = await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);

    // CreateDaemon is not a wake-up for a suspended workspace — calling it
    // again leaves the daemon suspended and the user without a machine.
    expect(deps.resumeDaemon).toHaveBeenCalledWith("d-old");
    expect(deps.createDaemon).not.toHaveBeenCalled();
    expect(result.daemonId).toBe("d-old");
  });

  it("does nothing when a machine is already running", async () => {
    const deps = makeDeps({
      listDaemons: vi.fn(async () => ({
        daemons: [{ id: "d-live", status: ACTIVE }],
      })),
    });

    const result = await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);

    expect(deps.createDaemon).not.toHaveBeenCalled();
    expect(deps.resumeDaemon).not.toHaveBeenCalled();
    expect(result.daemonId).toBe("d-live");
    expect(result.status).toBe("complete");
  });

  it("hands the gate the daemon id it should poll", async () => {
    const deps = makeDeps({ createDaemon: vi.fn(async () => "d-fresh") });
    const result = await commitLaunchPlan(CLOUD_CREDITS_PLAN, deps);
    expect(result.daemonId).toBe("d-fresh");
  });
});

describe("deriveCommitStatus", () => {
  const task = (
    name: CommitTask["name"],
    status: CommitTask["status"],
  ): CommitTask => ({ name, status, detail: "" });

  it("treats skipped work as neither success nor failure", () => {
    expect(
      deriveCommitStatus([
        task("grant_ai_access", "skipped"),
        task("provision_daemon", "skipped"),
      ]),
    ).toBe("complete");
  });

  it("is partial when some attempted work succeeded and some did not", () => {
    expect(
      deriveCommitStatus([
        task("grant_ai_access", "complete"),
        task("provision_daemon", "failed"),
      ]),
    ).toBe("partial");
  });

  // Pending is unfinished, not successful. A commit still waiting on a webhook
  // must not read as complete, or the gate stops waiting.
  it("counts pending as unfinished", () => {
    expect(
      deriveCommitStatus([
        task("grant_ai_access", "complete"),
        task("provision_daemon", "pending"),
      ]),
    ).toBe("partial");
    expect(
      deriveCommitStatus([
        task("grant_ai_access", "skipped"),
        task("provision_daemon", "pending"),
      ]),
    ).toBe("failed");
  });
});

describe("ensureCommitKey", () => {
  it("mints a key into the plan once, then reuses it", async () => {
    const updatePlan = vi.fn(async () => {});

    const minted = await ensureCommitKey({}, updatePlan);
    expect(minted).toBeTruthy();
    expect(updatePlan).toHaveBeenCalledWith({ commitKey: minted });

    // Second call with a plan that already has one must not write a new key —
    // a fresh key is a fresh commit, which is a second machine.
    const reused = await ensureCommitKey({ commitKey: minted }, updatePlan);
    expect(reused).toBe(minted);
    expect(updatePlan).toHaveBeenCalledTimes(1);
  });
});
