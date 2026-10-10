/**
 * Deadlines for unary RPCs, measured in time the user actually waited, and
 * recovery from requests stranded on a dead connection.
 *
 * ── What the prod evidence says ────────────────────────────────────────────
 *
 * Sentry (ELECTRON-8X, B0, B2, B5, B6, BA, 2026-10-07/08) recorded client
 * timeouts on reads the server answers in milliseconds — ListChats p99 0.23s,
 * ListDaemons p99 0.08s, GetChat p99 0.31s — and the api-server log shows the
 * server answering most of those very requests in 10–300ms. The answers were
 * lost between the server and the page:
 *
 *   - B2: an iOS tab was frozen with ListChats in flight; the server answered
 *     in 14ms; the page resumed ten minutes later and waited out its timer.
 *   - B0: an iOS page lost the ListMessages response (served in 319ms) on a
 *     connection that died under it; the retry eleven seconds later took 326ms.
 *   - BA: fourteen reads, all stuck behind one dead HTTP/2 connection after a
 *     laptop slept, all timing out at once.
 *
 * Every request multiplexed onto a dead HTTP/2 connection hangs together, and
 * the only thing that ended the wait was a 10s timer — which read wall-clock
 * time, so a request that spanned a phone lock was "619s old" the moment the
 * page woke.
 *
 * ── What this does instead ─────────────────────────────────────────────────
 *
 *  1. The budget is ACTIVE time (lib/pageActivity): a request is timed only
 *     while the page is visible and running.
 *  2. When the page resumes (visible after an absence, back online, thawed, or
 *     a wall-clock jump from a sleep), a read that was in flight across the
 *     absence is replaced at once with a fresh request instead of waiting.
 *  3. A read the server reliably answers in well under a second, that has had
 *     no answer after STALL_RETRY_MS, is replaced once. That turns "lost
 *     response, wait 10s, retry" into "lost response, retry at 4s".
 *
 * Replacement is limited to reads: a request that may have changed something
 * on the server is never sent twice. Writes keep their full active-time budget.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import type { Interceptor, UnaryRequest, UnaryResponse } from "@connectrpc/connect";
import type { PageActivity, ResumeEvent } from "../lib/pageActivity";

/** A read with no answer after this long is replaced (see FAST_READS). */
export const STALL_RETRY_MS = 4_000;

/** At most one replacement per call: two answers lost in a row is an outage. */
export const MAX_ATTEMPTS = 2;

/**
 * Reads the server answers fast enough that silence means a lost response,
 * not a slow one. Measured from the prod api-server `rpc completed` log over
 * 24h (2026-10-07/08); the slowest of these never exceeded 1.4s (ListMessages)
 * and most stay under 0.8s, so STALL_RETRY_MS leaves ~3x headroom.
 *
 * Deliberately absent: reads the server is genuinely slow on (ListWorkflows,
 * MCP ListServers, ListPresetsForWorkflow — p99 2–10s), where replacing the
 * request would only restart the wait; and reads served by the user's machine
 * (GetFileTree, ListProcesses), whose latency is the machine's, not ours.
 */
const FAST_READS: ReadonlySet<string> = new Set([
  "reliant.v1.DaemonRegistryService/ListDaemons",
  "reliant.v1.ChatService/ListChats",
  "reliant.v1.ChatService/ListArchivedChats",
  "reliant.v1.ChatService/GetChat",
  "reliant.v1.ChatService/ListMessages",
  "reliant.v1.ChatService/GetWorkflowExecutions",
  "reliant.v1.ChatService/ListQueuedAgentMessages",
  "reliant.v1.ChatService/GetThreadWorkflowInputs",
  "reliant.v1.ApprovalService/ListApprovalsByChat",
  "reliant.v1.QuestionService/GetPendingQuestion",
  "reliant.v1.PlanService/GetPlanByChatId",
  "reliant.v1.TaskService/ListTasks",
  "reliant.v1.RunService/ListRuns",
  "reliant.v1.InboxService/ListInbox",
  "reliant.v1.WorkflowService/GetWorkflow",
  "reliant.v1.ProjectService/ListProjects",
  "reliant.v1.SettingsService/GetProviderStatuses",
  "reliant.v1.SettingsService/ListSettings",
  "reliant.v1.SettingsService/GetPrivacySettings",
  "reliant.v1.SettingsService/GetPreferences",
  "reliant.v1.SettingsService/GetShortcuts",
  "reliant.v1.SettingsService/GetConfigHealth",
]);

/**
 * Method names that are reads by this API's naming convention. None of the
 * reliant.v1 protos declare `idempotency_level`, so the declared level is
 * honoured when present and the name is the fallback.
 */
const READ_METHOD_NAME = /^(Get|List|Search)[A-Z]/;

// google.protobuf.MethodOptions.IdempotencyLevel. Spelled out rather than
// imported: the well-known-types barrel is large, and the transport is on
// every page's critical path.
const IDEMPOTENCY_NO_SIDE_EFFECTS = 1;
const IDEMPOTENCY_IDEMPOTENT = 2;

interface MethodLike {
  name: string;
  idempotency?: number;
}

/** True when sending this call twice cannot change anything on the server. */
export function isReplaceableRead(method: MethodLike): boolean {
  if (
    method.idempotency === IDEMPOTENCY_NO_SIDE_EFFECTS ||
    method.idempotency === IDEMPOTENCY_IDEMPOTENT
  ) {
    return true;
  }
  return READ_METHOD_NAME.test(method.name);
}

/** The stall deadline for a procedure, or undefined when it has none. */
export function stallDeadlineFor(serviceTypeName: string, methodName: string): number | undefined {
  return FAST_READS.has(`${serviceTypeName}/${methodName}`) ? STALL_RETRY_MS : undefined;
}

// ─── Client-initiated aborts ────────────────────────────────────────────────
// Errors this module aborts a request with. The error-reporting interceptor
// inside the chain sees them as the failure of the attempt it wrapped; they are
// not failures of the server, and the one real one (a timeout) is reported here.

const clientAborts = new WeakSet<object>();

function clientAbort(message: string, code: Code): ConnectError {
  const error = new ConnectError(message, code);
  clientAborts.add(error);
  return error;
}

/** True for an abort reason this module created. */
export function isClientInitiatedAbort(reason: unknown): boolean {
  return typeof reason === "object" && reason !== null && clientAborts.has(reason);
}

// ─── In-flight registry (starvation diagnostics) ────────────────────────────

export interface InFlightEntry {
  method: string;
  /** Wall-clock start. */
  startedAt: number;
  /** Active-clock start, when known. */
  startedActive?: number;
}

const IN_FLIGHT_DESCRIBE_CAP = 8;
/** Show the wall age beside the active age once they differ by this much. */
const WALL_AGE_NOTE_MS = 5_000;

/**
 * Pure formatter for the in-flight diagnostic line, e.g.
 * "7 in flight, oldest: GetWorktreeChanges 43s [GetWorktreeChanges:43s, ListApprovalsByChat:9s, +1 more]"
 *
 * Ages are active time when `activeNow` is given; an entry whose wall age is
 * far larger (it spanned a sleep) shows both, e.g. "ListChats:10s (619s wall)",
 * so a frozen tab is never again mistaken for a ten-minute request.
 */
export function formatInFlight(
  entries: ReadonlyArray<InFlightEntry>,
  now: number,
  activeNow?: number,
): string {
  if (entries.length === 0) return "0 in flight";
  const oldestFirst = [...entries].sort((a, b) => a.startedAt - b.startedAt);
  const age = (e: InFlightEntry) => {
    const wallMs = now - e.startedAt;
    if (activeNow === undefined || e.startedActive === undefined) {
      return `${Math.round(wallMs / 1000)}s`;
    }
    const activeMs = activeNow - e.startedActive;
    const active = `${Math.round(activeMs / 1000)}s`;
    return wallMs - activeMs >= WALL_AGE_NOTE_MS
      ? `${active} (${Math.round(wallMs / 1000)}s wall)`
      : active;
  };
  const shown = oldestFirst
    .slice(0, IN_FLIGHT_DESCRIBE_CAP)
    .map((e) => `${e.method}:${age(e)}`);
  const overflow =
    oldestFirst.length > IN_FLIGHT_DESCRIBE_CAP
      ? `, +${oldestFirst.length - IN_FLIGHT_DESCRIBE_CAP} more`
      : "";
  const oldest = oldestFirst[0];
  return `${oldestFirst.length} in flight, oldest: ${oldest.method} ${age(oldest)} [${shown.join(", ")}${overflow}]`;
}

// ─── The interceptor ────────────────────────────────────────────────────────

export interface DeadlineInterceptorOptions {
  /** Active-time budget for a call; 0 means "no client deadline". */
  timeoutFor: (serviceTypeName: string, methodName: string) => number;
  activity: () => PageActivity;
  /** Called once per call whose budget ran out, before it is aborted. */
  onTimeout?: (method: string, budgetMs: number, diagnostics: string) => void;
  /** Called when a stranded or stalled read is replaced. */
  onReplace?: (method: string, why: ReplaceReason, diagnostics: string) => void;
}

export type ReplaceReason = "stalled" | `resumed:${ResumeEvent["reason"]}`;

type AttemptOutcome =
  | { kind: "done"; response: UnaryResponse }
  | { kind: "replace"; why: ReplaceReason };

export interface DeadlineInterceptor {
  interceptor: Interceptor;
  describeInFlight(): string;
}

export function createDeadlineInterceptor(options: DeadlineInterceptorOptions): DeadlineInterceptor {
  let nextId = 0;
  const inFlight = new Map<number, InFlightEntry>();

  const describeInFlight = (): string =>
    formatInFlight([...inFlight.values()], Date.now(), options.activity().activeNow());

  const interceptor: Interceptor = (next) => async (req) => {
    const methodName = req.method.name;
    const budgetMs = options.timeoutFor(req.service.typeName, methodName);
    if (budgetMs === 0) return next(req);
    if (req.stream) return raceStreamHeaders(next, req, budgetMs, methodName);

    if (req.signal.aborted) throw ConnectError.from(req.signal.reason);

    const activity = options.activity();
    const replaceable = isReplaceableRead(req.method);
    const startedActive = activity.activeNow();
    const id = nextId++;
    inFlight.set(id, { method: methodName, startedAt: Date.now(), startedActive });

    try {
      for (let attempt = 1; ; attempt++) {
        const canReplace = replaceable && attempt < MAX_ATTEMPTS;
        const outcome = await runAttempt(next as UnaryNext, req, {
          activity,
          budgetMs,
          startedActive,
          stallMs: canReplace ? stallDeadlineFor(req.service.typeName, methodName) : undefined,
          canReplace,
          onTimeout: () => {
            // Snapshot BEFORE aborting so the line still includes this call and
            // everything queued behind the same connection.
            const diagnostics = describeInFlight();
            options.onTimeout?.(methodName, budgetMs, `attempt ${attempt}; ${diagnostics}`);
          },
        });
        if (outcome.kind === "done") return outcome.response;
        options.onReplace?.(methodName, outcome.why, describeInFlight());
      }
    } finally {
      inFlight.delete(id);
    }
  };

  return { interceptor, describeInFlight };
}

type UnaryNext = (req: UnaryRequest) => Promise<UnaryResponse>;

interface AttemptOptions {
  activity: PageActivity;
  budgetMs: number;
  startedActive: number;
  stallMs: number | undefined;
  canReplace: boolean;
  onTimeout: () => void;
}

/**
 * One request on the wire. Resolves with the response, resolves "replace" when
 * this attempt was abandoned for a fresh one, and rejects with the call's
 * failure (including its timeout).
 */
function runAttempt(
  next: UnaryNext,
  req: UnaryRequest,
  opts: AttemptOptions,
): Promise<AttemptOutcome> {
  const { activity } = opts;
  const controller = new AbortController();
  const attemptStartedAt = Date.now();
  const attemptStartedActive = activity.activeNow();

  return new Promise<AttemptOutcome>((resolve, reject) => {
    let settled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;

    const cleanup = () => {
      settled = true;
      if (timer !== null) clearTimeout(timer);
      timer = null;
      req.signal.removeEventListener("abort", onUpstreamAbort);
      unsubscribeResume();
    };

    const abandon = (why: ReplaceReason) => {
      if (settled) return;
      cleanup();
      controller.abort(
        clientAbort(`${req.method.name} replaced with a fresh request (${why})`, Code.Canceled),
      );
      resolve({ kind: "replace", why });
    };

    const onUpstreamAbort = () => {
      if (settled) return;
      cleanup();
      controller.abort(req.signal.reason);
      reject(ConnectError.from(req.signal.reason));
    };

    // A request that was on the wire before the page went away rode a
    // connection that may be gone; its answer may never come.
    const unsubscribeResume = activity.onResume((event) => {
      if (!opts.canReplace) return;
      const absenceBegan = event.at - event.inactiveMs;
      if (attemptStartedAt > absenceBegan) return;
      abandon(`resumed:${event.reason}`);
    });

    // The deadline is checked on the active clock, so a timer that fires late
    // (or "on time" after a sleep) re-arms for whatever active time is left.
    let stallMs = opts.stallMs;
    const arm = () => {
      const active = activity.activeNow();
      const budgetLeft = opts.budgetMs - (active - opts.startedActive);
      const stallLeft = stallMs === undefined ? Infinity : stallMs - (active - attemptStartedActive);
      if (budgetLeft <= 0) {
        opts.onTimeout();
        const error = clientAbort(
          `${req.method.name} timed out after ${opts.budgetMs}ms`,
          Code.DeadlineExceeded,
        );
        cleanup();
        controller.abort(error);
        reject(error);
        return;
      }
      if (stallLeft <= 0) {
        if (opts.canReplace) {
          abandon("stalled");
          return;
        }
        stallMs = undefined;
      }
      timer = setTimeout(arm, Math.max(1, Math.min(budgetLeft, stallLeft)));
    };

    if (req.signal.aborted) {
      onUpstreamAbort();
      return;
    }
    req.signal.addEventListener("abort", onUpstreamAbort);
    arm();

    next({ ...req, signal: controller.signal }).then(
      (response) => {
        if (settled) return;
        cleanup();
        resolve({ kind: "done", response });
      },
      (error: unknown) => {
        if (settled) return;
        cleanup();
        reject(error);
      },
    );
  });
}

/**
 * Streams are bounded only until their response headers arrive; after that
 * their lifetime is the subscription's, which its owner manages. A stream
 * whose headers never come is aborted, not merely abandoned.
 */
async function raceStreamHeaders<T>(
  next: (req: never) => Promise<T>,
  req: { signal: AbortSignal },
  budgetMs: number,
  methodName: string,
): Promise<T> {
  const controller = new AbortController();
  const onUpstreamAbort = () => controller.abort(req.signal.reason);
  if (req.signal.aborted) onUpstreamAbort();
  else req.signal.addEventListener("abort", onUpstreamAbort);
  const timer = setTimeout(() => {
    controller.abort(clientAbort(`${methodName} timed out after ${budgetMs}ms`, Code.DeadlineExceeded));
  }, budgetMs);
  try {
    return await Promise.race([
      next({ ...req, signal: controller.signal } as never),
      new Promise<never>((_, reject) => {
        const fail = () => reject(ConnectError.from(controller.signal.reason));
        if (controller.signal.aborted) fail();
        else controller.signal.addEventListener("abort", fail);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}
