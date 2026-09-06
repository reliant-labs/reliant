/**
 * The onboarding commit point.
 *
 * Onboarding steps RECORD INTENT into the URL-backed `LaunchPlan`. Exactly one
 * operation acts on that intent, and this is it. The rule it exists to enforce:
 *
 *   > A call that CREATES, CANCELS or CHARGES may be issued only at a commit
 *   > point — a direct user action, or a webhook confirming money moved. Never
 *   > from a `useEffect` observing a state change, and never as a side effect of
 *   > preparing an option the user has not yet taken.
 *
 * Before this existed, `ComputeStep` fired a billable `CreateDaemon` from an
 * effect watching cloud eligibility. A coupon redemption flipping a server fact
 * was enough to provision a machine, which is speculative execution of a call
 * that spends money. That effect is gone; provisioning happens here.
 *
 * ── Why this is orchestrated client-side rather than a `CommitLaunchPlan` RPC ──
 *
 * The target design names a server-side `reliant.v1.OnboardingService/
 * CommitLaunchPlan` as the eventual home, and that is the right end state: a
 * server-owned commit survives a closed tab. It is deliberately NOT what this
 * module is, for one verifiable reason — reliant's Go server has no
 * control-plane client at all (`internal/` imports nothing from
 * `controlplane/v1/public`). Every daemon RPC in the product is issued by the
 * browser directly against the control plane. A server-side commit therefore
 * means building an outbound control-plane client, a `commits` table, and a
 * `GetCommitStatus` polling RPC before a single line of the actual fix lands.
 *
 * The defect being fixed is *when* the call fires, not *who* fires it. Moving
 * the call from an effect to a single explicit commit function fixes that
 * completely, client-side, today.
 *
 * The seam is drawn so the server version can subsume this without touching a
 * caller: {@link commitLaunchPlan} takes a plan and returns a
 * {@link CommitResult} of named tasks, which is the shape `CommitLaunchPlanResponse`
 * would carry. Replacing the body with one RPC + polling leaves the signature,
 * the idempotency contract and the gate rendering it unchanged.
 *
 * ── The four properties, and where each is enforced ──
 *
 * 1. IDEMPOTENT — keyed on `plan.commitKey`. A repeat call (double click,
 *    reload, a webhook landing twice) returns the in-flight promise or the
 *    settled result; it does not act twice. `CreateDaemon` is additionally
 *    idempotent by name server-side, which is why {@link ONBOARDING_DAEMON_NAME}
 *    is fixed — belt and braces on the one call that had it.
 * 2. ORDERED — AI access is granted before compute is provisioned, sequentially.
 *    Compute waits on entitlement; the grant does not wait on compute.
 * 3. PARTIAL FAILURE IS VISIBLE AND RECOVERABLE — a failing task never throws
 *    out of here. It comes back as a task with the server's own reason, and the
 *    overall status is `partial` when something else did succeed. The caller
 *    completes onboarding and shows the failure rather than trapping a user who
 *    has already paid.
 * 4. NO AUTOMATIC RETRY OF A BILLABLE CALL — a settled failure stays settled.
 *    Re-invoking with the same key returns the cached failure. Only
 *    {@link retryCommit}, wired to an explicit Retry button, clears it.
 */
import { isCloudCompute } from "./types";
import type { LaunchPlan } from "./types";

/**
 * The daemon name onboarding always uses.
 *
 * Load-bearing, not cosmetic: the control plane's `CreateDaemon` is idempotent
 * BY NAME (it refreshes the existing row rather than erroring), so a second
 * commit with the same name cannot mint a second machine. Any caller that
 * re-runs CreateDaemon for this user during onboarding must use this exact
 * name — `GitHubConnectStep` re-runs it to attach the picked repo.
 */
export const ONBOARDING_DAEMON_NAME = "onboarding-daemon";

/** controlplane.v1.DaemonType.MANAGED */
const DAEMON_TYPE_MANAGED = 1;
/** controlplane.v1.DaemonSize.SMALL */
const DAEMON_SIZE_SMALL = 1;

/** controlplane.v1.DaemonStatus.ACTIVE / .PENDING */
const DAEMON_STATUS_ACTIVE = 2;
const DAEMON_STATUS_PENDING = 1;

export type CommitTaskName = "grant_ai_access" | "provision_daemon";

export type CommitTaskStatus =
  /** Nothing to do for this plan — e.g. compute on the user's own machine. */
  | "skipped"
  /** Not finished, and not failed: waiting on a server fact (entitlement). */
  | "pending"
  | "complete"
  | "failed";

export interface CommitTask {
  name: CommitTaskName;
  status: CommitTaskStatus;
  /** Human-readable and safe to show. Carries the SERVER's reason on failure. */
  detail: string;
  /** Set by `provision_daemon` so the gate polls the right row. */
  daemonId?: string;
}

export type CommitStatus = "complete" | "partial" | "failed";

export interface CommitResult {
  commitKey: string;
  status: CommitStatus;
  tasks: CommitTask[];
  /** The daemon the caller should wait on, when one was provisioned. */
  daemonId?: string;
}

/**
 * Everything the commit touches, injected.
 *
 * Not for testability alone: it is the list of side effects this module is
 * allowed to have, written down. A call that is not in this interface cannot be
 * made from the commit point, which is the property the whole design rests on.
 */
export interface CommitDeps {
  grantAiAccess: () => Promise<{ synced: boolean }>;
  getComputeEligibility: () => Promise<{
    eligible: boolean;
    reason?: string | null;
  }>;
  listDaemons: () => Promise<
    { daemons: Array<{ id: string; status: number }> }
  >;
  createDaemon: (args: {
    name: string;
    daemonType: number;
    size: number;
    gitRepo: string;
    gitBranch: string;
  }) => Promise<string>;
  resumeDaemon: (daemonId: string) => Promise<void>;
  /** Injected so tests do not sleep. */
  sleep: (ms: number) => Promise<void>;
}

/**
 * How long the commit will wait for entitlement to become true before reporting
 * `pending` rather than failing.
 *
 * Compute eligibility is webhook-driven: `handleComputeCheckoutCompleted` must
 * write the subscription row before `GetCurrentUserComputeEligibility` reports
 * eligible. Calling `CreateDaemon` ahead of that returns an entitlement denial,
 * which the global upgrade interceptor turns into "please upgrade" — shown to a
 * user who has just paid. So we wait rather than assume.
 */
const ELIGIBILITY_POLL_ATTEMPTS = 15;
const ELIGIBILITY_POLL_INTERVAL_MS = 2_000;

function defaultDeps(): CommitDeps {
  return {
    grantAiAccess: async () => {
      const { onboardingService } = await import(
        "@/services/controlPlane/onboarding"
      );
      return onboardingService.provisionManagedKey();
    },
    getComputeEligibility: async () => {
      const [{ getComputeEligibility }, { computeIneligibleCopy }] =
        await Promise.all([
          import("@/services/controlPlane/billing"),
          import("@/hooks/useOnboardingQueries"),
        ]);
      const result = await getComputeEligibility();
      return {
        eligible: result.eligible,
        // Rendered through the same table the compute step uses, so the user
        // is not told two different things about one server fact.
        reason: result.eligible ? null : computeIneligibleCopy(result.reason),
      };
    },
    listDaemons: async () => {
      const { listDaemons } = await import("@/services/controlPlane/daemon");
      return listDaemons();
    },
    createDaemon: async (args) => {
      const { createDaemon } = await import("@/services/controlPlane/daemon");
      return createDaemon(args);
    },
    resumeDaemon: async (daemonId) => {
      const { resumeDaemon } = await import("@/services/controlPlane/daemon");
      return resumeDaemon(daemonId);
    },
    sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  };
}

/**
 * In-flight and settled commits, keyed by `commitKey`.
 *
 * Module-level on purpose. The key survives a reload because it lives in the
 * URL plan, but the map does not — which is exactly the split we want: within a
 * session a double click or a re-render cannot commit twice, and across a
 * reload the server-side name idempotency of `CreateDaemon` is what stops a
 * second machine.
 */
const commits = new Map<string, Promise<CommitResult>>();

/** Test seam: forget every recorded commit. */
export function __resetCommitsForTest(): void {
  commits.clear();
}

/**
 * Discard a settled commit so the next call re-runs it.
 *
 * The ONLY way a failed commit is retried. A commit that retried itself would
 * be a billable call firing without a user asking for it — the same class of
 * defect as provisioning from an effect, just on a timer.
 */
export function retryCommit(commitKey: string): void {
  commits.delete(commitKey);
}

function messageOf(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback;
}

/** Grant AI access. Runs FIRST — it is fast and often a no-op. */
async function grantAiAccess(
  plan: Partial<LaunchPlan>,
  deps: CommitDeps,
): Promise<CommitTask> {
  if (plan.modelProvider !== "reliant_credits") {
    return {
      name: "grant_ai_access",
      status: "skipped",
      detail: "Using your own model provider.",
    };
  }
  try {
    const { synced } = await deps.grantAiAccess();
    return {
      name: "grant_ai_access",
      status: synced ? "complete" : "failed",
      detail: synced
        ? "AI access ready."
        : "We couldn't finish setting up Reliant's models.",
    };
  } catch (err) {
    return {
      name: "grant_ai_access",
      status: "failed",
      detail: messageOf(err, "We couldn't finish setting up Reliant's models."),
    };
  }
}

/**
 * Wait for the server to agree the user may run a machine.
 *
 * Returns the last reason when it never does, so the caller reports what the
 * server said rather than a generic timeout.
 */
async function awaitComputeEligibility(
  deps: CommitDeps,
): Promise<{ eligible: boolean; reason: string }> {
  let reason = "";
  for (let attempt = 0; attempt < ELIGIBILITY_POLL_ATTEMPTS; attempt++) {
    if (attempt > 0) await deps.sleep(ELIGIBILITY_POLL_INTERVAL_MS);
    try {
      const result = await deps.getComputeEligibility();
      if (result.eligible) return { eligible: true, reason: "" };
      reason = result.reason ?? "";
    } catch (err) {
      reason = messageOf(err, "");
    }
  }
  return { eligible: false, reason };
}

/**
 * Provision the hosted machine. Runs AFTER the AI grant, and only for a cloud
 * plan.
 *
 * Eligibility is a PREDICTION of the daemon service's own gate — it skips the
 * per-size and minute-exhaustion checks — so being eligible is not permission.
 * The daemon service refusing is a normal outcome on the happy timeline, and it
 * is reported with the server's own reason rather than a generic error.
 */
async function provisionDaemon(
  plan: Partial<LaunchPlan>,
  deps: CommitDeps,
): Promise<CommitTask> {
  if (!isCloudCompute(plan.compute)) {
    return {
      name: "provision_daemon",
      status: "skipped",
      detail: "Running on your own computer.",
    };
  }

  const { eligible, reason } = await awaitComputeEligibility(deps);
  if (!eligible) {
    // PENDING, not failed. A webhook that has not landed yet is a wait, and
    // calling CreateDaemon into a denial would tell a paying user to upgrade.
    return {
      name: "provision_daemon",
      status: "pending",
      detail: reason || "Waiting for payment confirmation.",
    };
  }

  try {
    const { daemons } = await deps.listDaemons();

    const active = daemons.find((d) => d.status === DAEMON_STATUS_ACTIVE);
    if (active) {
      return {
        name: "provision_daemon",
        status: "complete",
        detail: "Your machine is already running.",
        daemonId: active.id,
      };
    }

    const existing =
      daemons.find((d) => d.status === DAEMON_STATUS_PENDING) ?? daemons[0];
    if (existing) {
      // A machine that exists but is not active is resumed, not re-created:
      // CreateDaemon is not a wake-up for a suspended workspace, so calling it
      // again would leave a suspended daemon suspended.
      await deps.resumeDaemon(existing.id);
      return {
        name: "provision_daemon",
        status: "complete",
        detail: "Starting your machine…",
        daemonId: existing.id,
      };
    }

    const daemonId = await deps.createDaemon({
      name: ONBOARDING_DAEMON_NAME,
      daemonType: DAEMON_TYPE_MANAGED,
      size: DAEMON_SIZE_SMALL,
      gitRepo: "",
      gitBranch: "main",
    });
    return {
      name: "provision_daemon",
      status: "complete",
      detail: "Starting your machine…",
      daemonId: daemonId || undefined,
    };
  } catch (err) {
    return {
      name: "provision_daemon",
      status: "failed",
      detail: messageOf(err, "We couldn't start your machine."),
    };
  }
}

/** `complete` when nothing that was attempted failed; `failed` when nothing
 *  that was attempted succeeded; `partial` otherwise. Skipped work is not an
 *  attempt, so a local + own-key plan commits `complete` having done nothing. */
export function deriveCommitStatus(tasks: CommitTask[]): CommitStatus {
  const attempted = tasks.filter((t) => t.status !== "skipped");
  if (attempted.length === 0) return "complete";
  const unfinished = attempted.filter((t) => t.status !== "complete");
  if (unfinished.length === 0) return "complete";
  const succeeded = attempted.some((t) => t.status === "complete");
  return succeeded ? "partial" : "failed";
}

/**
 * Fulfil a recorded launch plan. The one place onboarding creates anything.
 *
 * Never throws: every failure comes back as a task. A commit that threw would
 * put the decision "does onboarding continue?" back in each caller's catch
 * block, which is how a paid-up user ends up stranded in the wizard.
 */
export function commitLaunchPlan(
  plan: Partial<LaunchPlan>,
  overrides?: Partial<CommitDeps>,
): Promise<CommitResult> {
  const commitKey = plan.commitKey;
  if (!commitKey) {
    // Not defensive noise: without a key there is no idempotency, so a retry
    // would provision twice. Callers get the key from the plan, and
    // `ensureCommitKey` puts it there before the first commit is possible.
    return Promise.reject(
      new Error("commitLaunchPlan requires plan.commitKey"),
    );
  }

  const inFlight = commits.get(commitKey);
  if (inFlight) return inFlight;

  const deps: CommitDeps = { ...defaultDeps(), ...overrides };

  const run = (async (): Promise<CommitResult> => {
    // Sequential and in this order. AI credit lands fast and has no dependency;
    // compute waits on a webhook-driven entitlement. Running them together
    // would provision before the grant on the one timeline where the order
    // matters.
    const aiTask = await grantAiAccess(plan, deps);
    const daemonTask = await provisionDaemon(plan, deps);
    const tasks = [aiTask, daemonTask];
    return {
      commitKey,
      status: deriveCommitStatus(tasks),
      tasks,
      daemonId: daemonTask.daemonId,
    };
  })();

  commits.set(commitKey, run);
  return run;
}

/**
 * The plan's commit key, minting one into the URL if it has none.
 *
 * Stable for one onboarding run and stored in the plan, so it survives a reload
 * mid-provision — which is what makes "reload during provisioning" resume
 * rather than provision again.
 */
export async function ensureCommitKey(
  plan: Partial<LaunchPlan>,
  updatePlan: (updates: Partial<LaunchPlan>) => Promise<void> | void,
): Promise<string> {
  if (plan.commitKey) return plan.commitKey;
  const commitKey = newCommitKey();
  await updatePlan({ commitKey });
  return commitKey;
}

function newCommitKey(): string {
  const cryptoObj = globalThis.crypto;
  if (cryptoObj?.randomUUID) return cryptoObj.randomUUID();
  return `commit-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}
