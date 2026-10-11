/**
 * Single shared interceptor chain for every Connect transport in the app.
 *
 * Before this module existed, three places built transports independently:
 *   - `api/grpc-client.ts::getTransport`           (reliant api-server)
 *   - `api/grpc-client.ts::getControlPlaneTransport` (daemon-registry/token, cloud mode)
 *   - `services/controlPlane/client.ts`              (control-plane DaemonService etc.)
 *
 * They drifted: the third only attached an auth + upgrade interceptor, missing
 * timeout / tracing / Sentry error-logging / 401 sign-out / daemon-last-seen.
 * The user-visible symptom of that drift was the project picker's "Resume
 * daemon" call surfacing as a raw toast instead of the UpgradeRequiredModal
 * (the fix added `upgradeInterceptor` to the third transport — but the rest
 * of the drift was still there). This factory makes the drift mechanically
 * impossible: every transport calls `buildInterceptors(...)`.
 *
 * The chain order is:
 *   timeout → auth → daemon-last-seen → tracing → error-logging → upgrade-modal → machine-wake → 401-signout
 * (daemon-last-seen only on reliant api-server transports — see TransportBackend).
 *
 * Why this order:
 *   - timeout outermost so the full request lifecycle (incl. retries through
 *     inner interceptors) is bounded;
 *   - auth attaches the bearer before any header injection (tracing);
 *   - daemon-last-seen header is independent of auth ordering but cheap to
 *     leave just after auth;
 *   - tracing wraps the actual `next(req)` so spans capture network time;
 *   - errorInterceptor logs/reports to Sentry — wraps tracing so it sees the
 *     real failure (tracing would otherwise mark OK and rethrow);
 *   - upgradeInterceptor opens the modal on ResourceExhausted + reason header
 *     before propagating;
 *   - machineWakeInterceptor records a machine the server woke for the request
 *     (DaemonWaking detail) so every machine wait says "Waking up…", then
 *     propagates;
 *   - unauthInterceptor (401 → one refresh, else one sign-out) lives innermost so it doesn't gobble
 *     the timeout's DeadlineExceeded or the upgrade's ResourceExhausted.
 *
 * Setting `withAuth: false` skips both `authInterceptor` AND
 * `unauthInterceptor` (a 401 with no session would loop). All other
 * interceptors stay on so the unauth transport still gets timeouts, tracing,
 * Sentry error reporting, and the upgrade modal — drift gone.
 */

import { ConnectError, Code } from "@connectrpc/connect";
import type { Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
  trace,
  context,
  SpanStatusCode,
  SpanKind,
  propagation,
  type Span,
} from "@opentelemetry/api";
import * as Sentry from "@sentry/react";
import { logger } from "../lib/logger";
import { getAuthTokenProvider } from "./authProvider";
import { upgradeInterceptor } from "./upgradeInterceptor";
import { machineWakeInterceptor } from "./machineWakeInterceptor";
import { createDeadlineInterceptor } from "./rpcDeadline";
import { classifyRpcFailure } from "./rpcErrorPolicy";
import { noteVersionSkew } from "./versionSkew";
import { pageActivity } from "../lib/pageActivity";
import {
  DEFAULT_GRPC_TIMEOUT_MS,
  FILE_OPERATION_TIMEOUT_MS,
  CHAT_OPERATION_TIMEOUT_MS,
  MCP_OPERATION_TIMEOUT_MS,
  UPLOAD_TIMEOUT_MS,
  WORKTREE_OPERATION_TIMEOUT_MS,
  OAUTH_TIMEOUT_MS,
  OAUTH_EXCHANGE_TIMEOUT_MS,
  PROVIDER_VALIDATION_TIMEOUT_MS,
} from "../lib/constants";

// Re-export the upgrade interceptor through this module so tests / callers
// have one obvious place to look.
export { upgradeInterceptor };

// ─── Daemon last-seen header ─────────────────────────────────────────
// Module-level state set by globalUpdatesStore to avoid circular imports.
let _daemonLastSeen: number | null = null;
/** Called by globalUpdatesStore when a DAEMON_HEARTBEAT event arrives. */
export function setDaemonLastSeen(unixSeconds: number): void {
  _daemonLastSeen = unixSeconds;
}

// ─── Current base URL (for error logging context) ────────────────────
let _currentBaseURL: string | null = null;
export function setCurrentBaseURL(url: string | null): void {
  _currentBaseURL = url;
}

// Attaches x-daemon-last-seen header so the server can skip the
// IsDaemonOnline DB query when the daemon was recently seen.
//
// Only the reliant api-server reads it (internal/grpc/interceptors/auth.go),
// so only its transports carry it — see TransportBackend. Its CORS allow-list
// must name it; internal/grpc/cors_web_client_test.go enforces that.
const daemonLastSeenInterceptor: Interceptor = (next) => async (req) => {
  if (_daemonLastSeen !== null) {
    req.header.set("x-daemon-last-seen", String(_daemonLastSeen));
  }
  return await next(req);
};

// Auth interceptor to add a bearer token to requests.
//
// The token source is pluggable — see `api/authProvider.ts`. The default
// provider reproduces the historical inline behavior (API key from
// localStorage first, then the Supabase session, with Supabase imported
// lazily to break the `supabase → devAuth (grpc-unauth) → transport` cycle).
// Native shells and embedders swap in their own provider at bootstrap.
const authInterceptor: Interceptor = (next) => async (req) => {
  const token = await getAuthTokenProvider().getToken();

  if (token) {
    req.header.set("Authorization", `Bearer ${token}`);
  } else {
    logger.warn("[gRPC Client] No auth token available for request:", {
      method: req.method.name,
      isElectron: typeof window !== "undefined" ? !!window.electronAPI : false,
    });
  }

  return await next(req);
};

// Methods that need longer timeouts (0 = no timeout, handled by streaming layer)
const LONG_TIMEOUT_METHODS: Record<string, number> = {
  // File operations - may involve large files
  ReadFile: FILE_OPERATION_TIMEOUT_MS,
  WriteFile: FILE_OPERATION_TIMEOUT_MS,
  ListFiles: FILE_OPERATION_TIMEOUT_MS,
  // Chat operations that involve workflows - initial setup can take time
  StartChat: CHAT_OPERATION_TIMEOUT_MS,
  SendMessage: CHAT_OPERATION_TIMEOUT_MS,
  // MCP operations - external process startup can be slow
  StartServer: MCP_OPERATION_TIMEOUT_MS,
  InstallServer: MCP_OPERATION_TIMEOUT_MS,
  RestartServer: MCP_OPERATION_TIMEOUT_MS,
  UpdateServerConfig: MCP_OPERATION_TIMEOUT_MS,
  UninstallServer: MCP_OPERATION_TIMEOUT_MS,
  CallTool: MCP_OPERATION_TIMEOUT_MS,
  // Attachment uploads - depends on file size
  Upload: UPLOAD_TIMEOUT_MS,
  // Worktree operations - involve git commands and copied files that can exceed 30s
  CreateWorktree: WORKTREE_OPERATION_TIMEOUT_MS,
  DeleteWorktree: WORKTREE_OPERATION_TIMEOUT_MS,
  ArchiveWorktree: WORKTREE_OPERATION_TIMEOUT_MS,
  UnarchiveWorktree: WORKTREE_OPERATION_TIMEOUT_MS,
  ImportWorktree: WORKTREE_OPERATION_TIMEOUT_MS,
  DiscoverWorktrees: WORKTREE_OPERATION_TIMEOUT_MS,
  RecreateWorktree: WORKTREE_OPERATION_TIMEOUT_MS,
  GetWorktreeChanges: WORKTREE_OPERATION_TIMEOUT_MS,
  GetWorktreeGitStatus: WORKTREE_OPERATION_TIMEOUT_MS,
  GetWorktreeCommits: WORKTREE_OPERATION_TIMEOUT_MS,
  StageFiles: WORKTREE_OPERATION_TIMEOUT_MS,
  UnstageFiles: WORKTREE_OPERATION_TIMEOUT_MS,
  CommitWorktree: WORKTREE_OPERATION_TIMEOUT_MS,
  PushWorktree: WORKTREE_OPERATION_TIMEOUT_MS,
  PullWorktree: WORKTREE_OPERATION_TIMEOUT_MS,
  GetWorktreePR: WORKTREE_OPERATION_TIMEOUT_MS,
  CreateWorktreePR: WORKTREE_OPERATION_TIMEOUT_MS,
  RevertFiles: WORKTREE_OPERATION_TIMEOUT_MS,
  // OAuth flows — no timeout, user can take as long as needed (cancelled via AbortController)
  StartOAuthFlow: OAUTH_TIMEOUT_MS,
  // StartOAuthSignIn blocks on the SAME thing: the backend opens the system
  // browser and waits for the user to finish signing in with the provider. It
  // was missing here, so it inherited the 10s default and aborted while the
  // user was still on the provider's consent screen.
  StartOAuthSignIn: OAUTH_TIMEOUT_MS,
  // OAuth token exchange - external network call
  CompleteClaudeOAuth: OAUTH_EXCHANGE_TIMEOUT_MS,
  CompleteCodexOAuth: OAUTH_EXCHANGE_TIMEOUT_MS,
  // Provider API key validation - external network call
  ValidateProviderAPIKey: PROVIDER_VALIDATION_TIMEOUT_MS,
  UpdateProviderAPIKey: PROVIDER_VALIDATION_TIMEOUT_MS,
  // Control-plane CloneRepo: NATS request/reply to the daemon with a 60s
  // server-side timeout (gitcredential.svc.Clone), then a real `git clone`
  // subprocess on large repos. WORKTREE_OPERATION_TIMEOUT_MS gives headroom
  // above that 60s floor rather than introducing a third "git op" constant.
  CloneRepo: WORKTREE_OPERATION_TIMEOUT_MS,

  // Streaming methods should not have client-side timeout
  // These are server-streaming RPCs that manage their own lifecycle via AbortController
  StreamUserUpdates: 0,
  StreamProcessOutput: 0,
};

/**
 * Budgets keyed by FULLY-QUALIFIED procedure (`<service typeName>/<method>`).
 *
 * Separate from LONG_TIMEOUT_METHODS because that map is keyed by the bare
 * method name, and forge's names are not unique: `PlanDeploy` is also a
 * control-plane DeployService read that should stay on the short default.
 *
 * WHY FORGE NEEDS THESE. Each ForgeService call runs a `forge` subprocess on the
 * user's daemon with its own 15–110s budget (internal/grpc/services/forge.go).
 * Without an entry here this transport aborted them at 10s — `forge env status`
 * on control-plane takes about 5s cold and more under load — and the
 * api-server, seeing the request cancelled, answered with an empty UNREACHABLE
 * reply that the UI then rendered as "this project has no forge.yaml". These
 * mirror the server's ForgeRPCDeadlines: dispatch budget plus the 5s headroom.
 */
const FORGE = "reliant.v1.ForgeService";
const PROCEDURE_TIMEOUTS: Record<string, number> = {
  [`${FORGE}/GetTopology`]: 115_000,
  [`${FORGE}/VerifyEnv`]: 80_000,
  [`${FORGE}/ListSecrets`]: 35_000,
  [`${FORGE}/GetAudit`]: 65_000,
  [`${FORGE}/GetEnvStatus`]: 50_000,
  [`${FORGE}/GetEnvShape`]: 50_000,
  [`${FORGE}/PlanPromote`]: 25_000,
  [`${FORGE}/ApplyPromote`]: 50_000,
  [`${FORGE}/PlanDeploy`]: 105_000,
  [`${FORGE}/StartDeployPlan`]: 115_000,
  [`${FORGE}/ListCheckouts`]: 105_000,
  [`${FORGE}/DiffEnv`]: 105_000,
  [`${FORGE}/StartDeploy`]: 115_000,
  [`${FORGE}/GetDeployStatus`]: 20_000,
};

/** The client-side budget for one RPC. Exported for the transport tests. */
export function timeoutForProcedure(serviceTypeName: string, methodName: string): number {
  return (
    PROCEDURE_TIMEOUTS[`${serviceTypeName}/${methodName}`] ??
    LONG_TIMEOUT_METHODS[methodName] ??
    DEFAULT_GRPC_TIMEOUT_MS
  );
}

// ─── Deadlines (active time) and stranded-request recovery ───────────
// See rpcDeadline.ts. In short: a call's budget is measured in time the page
// was visible and running, a read stranded across a sleep / network change /
// dead connection is replaced with a fresh request instead of waiting out its
// timer, and a fast read whose answer was lost is replaced at STALL_RETRY_MS.
//
// During the 2026-07-09 incident a hung daemon command (worktree.git_changes)
// left GetWorktreeChanges pending and every later unary RPC queued behind it
// with nothing in the console until the first timeout. The timeout handler
// therefore prints a snapshot of every in-flight unary RPC, so a single
// console line identifies the wedge.

export { formatInFlight } from "./rpcDeadline";

// Rate-limit rpc-timeout Sentry events: a wedged connection times out many
// queued RPCs in a burst, and each event would carry the same diagnostic
// snapshot — one event per minute captures the incident without flooding.
// This warning is the ONE report of a client timeout; errorInterceptor does
// not also capture each timed-out call as an exception (ELECTRON-8X was one
// incident reported thirteen times).
const RPC_TIMEOUT_REPORT_INTERVAL_MS = 60_000;
let _lastRpcTimeoutReportAt = 0;

function reportRpcTimeout(
  method: string,
  timeoutMs: number,
  diagnostics: string,
): void {
  logger.error(
    `[gRPC Client] ${method} timed out after ${timeoutMs}ms of active time — ${diagnostics}`,
  );
  const now = Date.now();
  if (now - _lastRpcTimeoutReportAt < RPC_TIMEOUT_REPORT_INTERVAL_MS) return;
  _lastRpcTimeoutReportAt = now;
  // captureMessage on an uninitialized SDK is a safe no-op, and initSentry()
  // is skipped in dev (see lib/sentry.ts), so this only reports from packaged
  // builds where Sentry is actually running.
  Sentry.captureMessage(
    `rpc-timeout: ${method} after ${timeoutMs}ms (${diagnostics})`,
    "warning",
  );
}

const deadline = createDeadlineInterceptor({
  timeoutFor: timeoutForProcedure,
  activity: pageActivity,
  onTimeout: reportRpcTimeout,
  onReplace: (method, why, diagnostics) => {
    logger.warn(
      `[gRPC Client] ${method} replaced with a fresh request (${why}) — ${diagnostics}`,
    );
  },
});

/** Snapshot of currently in-flight unary RPCs, oldest first. */
export function describeInFlight(): string {
  return deadline.describeInFlight();
}

const timeoutInterceptor: Interceptor = deadline.interceptor;

// OTel tracing interceptor — creates a span per RPC and injects W3C
// traceparent/tracestate headers.
const tracingInterceptor: Interceptor = (next) => async (req) => {
  const tracer = trace.getTracer("reliant-frontend");
  const spanName = `grpc.${req.service.typeName}/${req.method.name}`;

  return tracer.startActiveSpan(
    spanName,
    { kind: SpanKind.CLIENT },
    async (span: Span) => {
      try {
        // Inject W3C trace context into request headers
        const carrier: Record<string, string> = {};
        propagation.inject(context.active(), carrier);
        for (const [key, value] of Object.entries(carrier)) {
          req.header.set(key, value);
        }

        span.setAttribute("rpc.system", "connect");
        span.setAttribute("rpc.service", req.service.typeName);
        span.setAttribute("rpc.method", req.method.name);

        const result = await next(req);
        span.setStatus({ code: SpanStatusCode.OK });
        return result;
      } catch (error) {
        span.setStatus({
          code: SpanStatusCode.ERROR,
          message: error instanceof Error ? error.message : String(error),
        });
        span.recordException(
          error instanceof Error ? error : new Error(String(error)),
        );
        throw error;
      } finally {
        span.end();
      }
    },
  );
};

// Warn when a unary RPC takes at least this long. Streams are excluded —
// their duration is the lifetime of the subscription, not a latency.
//
// No method is exempted: a survey of 40k logged durations found none that is
// slow BY DESIGN (no unary long-polls). The slowest, GetWorkflowExecutions,
// exceeded this on 127 of 148 calls with a p50 of 1.9s — that is a real
// latency problem worth surfacing, not a poll to filter out. If a genuine
// unary long-poll is added later, exempt it here by name and say why.
const SLOW_REQUEST_THRESHOLD_MS = 1000;

// Error logging interceptor. What counts as a reportable failure is decided in
// rpcErrorPolicy.ts; this only acts on the verdict.
const errorInterceptor: Interceptor = (next) => async (req) => {
  const startTime = Date.now();
  try {
    const result = await next(req);
    const duration = Date.now() - startTime;
    if (!req.stream && duration >= SLOW_REQUEST_THRESHOLD_MS) {
      logger.warn("[gRPC Client] Slow request", {
        service: req.service.typeName,
        method: req.method.name,
        durationMs: duration,
      });
    }
    return result;
  } catch (error) {
    const duration = Date.now() - startTime;
    const kind = classifyRpcFailure(error, {
      serviceTypeName: req.service.typeName,
      signalAborted: req.signal.aborted,
    });
    const context = {
      service: req.service.typeName,
      method: req.method.name,
      durationMs: duration,
      errorMessage: error instanceof Error ? error.message : String(error),
      errorCode: (error as { code?: unknown })?.code,
    };

    if (kind === "aborted") {
      // Not a failure: the abort is the cause, and its owner (the caller, a
      // superseding reconnect, the deadline) already knows. Surface it as the
      // cancellation it is — connect-web can throw a bare string here.
      logger.debug("[gRPC Client] Request aborted", context);
      throw error instanceof ConnectError ? error : ConnectError.from(req.signal.reason ?? error);
    }
    if (kind === "account-required") {
      logger.info("[gRPC Client] Account required", context);
      throw error;
    }
    if (kind === "machine-wait") {
      logger.info("[gRPC Client] Machine not serving yet", context);
      throw error;
    }

    logger.error("[gRPC Client] Request failed:", {
      ...context,
      baseUrl: _currentBaseURL,
      isElectron: typeof window !== "undefined" ? !!window.electronAPI : false,
      protocol:
        typeof window !== "undefined" ? window.location.protocol : undefined,
      error,
      errorName: (error as { name?: unknown })?.name,
      errorCause: (error as { cause?: unknown })?.cause,
    });

    if (kind === "version-skew") {
      noteVersionSkew(req.service.typeName, req.method.name);
    } else if (kind === "report") {
      Sentry.captureException(error, {
        tags: {
          grpc_service: req.service.typeName,
          grpc_method: req.method.name,
          grpc_code:
            error instanceof ConnectError ? Code[error.code] : "unknown",
        },
        extra: {
          baseUrl: _currentBaseURL,
          durationMs: duration,
        },
      });
    }

    throw error;
  }
};

// Recovery from a token the backend rejected.
//
// When a token expires (or was minted by a different Supabase project, or the
// user was deleted, or signing keys rotated), EVERY request in flight carries
// it, so a burst of 401s arrives together: measured in a dev log, ~75 failures
// each of ListModels, ListSettings, ListProjects, DeleteSetting and
// GetPrivacySettings, plus `SettingsSync failed after retries` x76. Each was
// handled on its own, and the guard against concurrent sign-outs was checked
// BEFORE an await and set after it, so every 401 in the burst got through it.
//
// Now the burst is one event:
//   - The first rejected-token 401 starts ONE recovery; every other 401 that
//     arrives while it runs awaits the same promise instead of deciding alone.
//   - Recovery refreshes the session once. If that yields a token different
//     from the rejected one, each waiting request is retried once with it —
//     an expired access token is the common case and the user never notices.
//   - Only if the refresh fails is the user signed out, exactly once, and
//     sent to /auth.
//
// The outcome is remembered per rejected token for a short window, so a 401
// that lands just after recovery finished (a request that was already on the
// wire) joins its result instead of starting a second recovery.
//
// Safety guards kept from the previous handler:
// - Only acts when a session/API key is believed active (no sign-in→sign-out
//   loops on the auth screen).
// - Skipped on /auth so a 401 there doesn't redirect to itself.
// - The redirect guard re-arms on a timer: if Electron's will-navigate handler
//   cancels the navigation, the page survives, and a latched flag would wedge
//   it while an unlatched one would loop. A bounded retry does neither.
const SIGN_OUT_RETRY_MS = 10_000;
const RECOVERY_REUSE_MS = 5_000;

type RecoveryOutcome =
  | { kind: "refreshed"; token: string }
  | { kind: "signed-out" }
  | { kind: "no-session" };

interface Recovery {
  rejectedToken: string;
  outcome: Promise<RecoveryOutcome>;
  settledAt?: number;
}

let _recovery: Recovery | null = null;

const AUTH_HEADER = "Authorization";

function bearer(token: string): string {
  return `Bearer ${token}`;
}

/**
 * Recover from a rejected credential: refresh once, else sign out once.
 * Concurrent callers presenting the same rejected credential share one
 * recovery. Synchronous up to the point the recovery is recorded, so two 401s
 * in the same tick cannot both start one.
 */
function recoverFromRejectedToken(
  req: { service: { typeName: string }; method: { name: string } },
  error: ConnectError,
  presented: string,
): Promise<RecoveryOutcome> {
  const current = _recovery;
  if (current) {
    const fresh =
      current.settledAt === undefined ||
      Date.now() - current.settledAt < RECOVERY_REUSE_MS;
    // Join the recovery in flight, or one that just finished for this same
    // credential. A DIFFERENT credential rejected after recovery settled is a
    // new event (e.g. the refreshed token was itself rejected).
    if (current.settledAt === undefined || (fresh && current.rejectedToken === presented)) {
      return current.outcome;
    }
  }

  const recovery: Recovery = {
    rejectedToken: presented,
    outcome: runRecovery(req, error, presented),
  };
  _recovery = recovery;
  void recovery.outcome.finally(() => {
    recovery.settledAt = Date.now();
  });
  return recovery.outcome;
}

async function runRecovery(
  req: { service: { typeName: string }; method: { name: string } },
  error: ConnectError,
  presented: string,
): Promise<RecoveryOutcome> {
  const provider = getAuthTokenProvider();
  // Deliberately `hasSession()` rather than `getToken()`: this only needs to
  // know whether a session is believed active.
  if (!(await provider.hasSession())) return { kind: "no-session" };

  const refreshed = await provider.refresh().catch(() => null);
  if (refreshed && bearer(refreshed) !== presented) {
    logger.warn("[gRPC Client] token rejected by backend; refreshed the session", {
      service: req.service.typeName,
      method: req.method.name,
    });
    return { kind: "refreshed", token: refreshed };
  }

  logger.warn(
    "[gRPC Client] token rejected by backend and the session could not be refreshed; signing out",
    {
      service: req.service.typeName,
      method: req.method.name,
      message: error.message,
    },
  );
  Sentry.captureMessage("Auto sign-out on 401 with active session", {
    level: "warning",
    tags: {
      grpc_service: req.service.typeName,
      grpc_method: req.method.name,
    },
  });

  try {
    const { useAuthStore } = await import("../store/authStore");
    await useAuthStore.getState().signOut();
  } catch (signOutErr) {
    logger.error("[gRPC Client] Auto sign-out failed", signOutErr);
  }

  // Hard redirect clears React Query caches and any in-flight retries that
  // would otherwise keep firing 401s against the dead session.
  if (
    typeof window !== "undefined" &&
    !window.location.pathname.startsWith("/auth")
  ) {
    window.location.href = "/auth";
    // If the navigation lands, this page (and timer) are gone. If something
    // cancels it, forget this recovery after a while so a later 401 can try
    // again, at a bounded rate.
    window.setTimeout(() => {
      _recovery = null;
    }, SIGN_OUT_RETRY_MS);
  }
  return { kind: "signed-out" };
}

/** Test seam: forget any recovery in progress or remembered. */
export function resetAuthRecoveryForTests(): void {
  _recovery = null;
}

// A 401 proves our SESSION is bad only if we actually presented a credential.
//
// When no Authorization header made it onto the request, the server's auth
// interceptor rejects it before any handler runs and reports exactly
// "missing authorization token" (internal/grpc/interceptors/auth.go:182 — the
// empty-header branch; a token that is expired or malformed takes a different
// branch and says "invalid or expired token"). So a bare 401 on a tokenless
// request is a CLIENT-side attach miss, not a backend rejection, and tearing
// down the session over it signs out a user whose credentials are fine.
//
// That miss is real and transient. Measured 2026-08-26: three ListDaemons went
// out with no token at 16:10:45–52 while the session was live and 25 seconds
// before any sign-out — `getToken()` resolved null (Supabase mid-refresh /
// daemon restart) while `hasSession()` still said true, which is precisely the
// window the old handler read as proof of a bad token.
//
// Retrying is safe here *because* no token was sent: the request was refused
// at the auth interceptor before reaching a handler, so even a mutation like
// UpdateSetting had no server-side effect that a retry could duplicate.
const unauthInterceptor: Interceptor = (next) => async (req) => {
  try {
    return await next(req);
  } catch (error) {
    if (
      !(error instanceof ConnectError) ||
      error.code !== Code.Unauthenticated
    ) {
      throw error;
    }

    const presented = req.header.get(AUTH_HEADER);

    // The auth interceptor logs nothing on the happy path (it ran per-RPC and
    // dominated the dev log), so this is the only place that records whether a
    // token was attached. Warn, not info: `createLogFunction` no-ops info in
    // packaged builds, which is exactly where these incidents get reported.
    // Fires only on a 401, so it costs nothing on the happy path.
    logger.warn("[gRPC Client] 401 received", {
      service: req.service.typeName,
      method: req.method.name,
      tokenAttached: !!presented,
      tokenLength: presented?.length ?? 0,
      message: error.message,
    });

    if (presented) {
      const outcome = await recoverFromRejectedToken(req, error, presented);
      // Streams are not replayed: their owner reconnects, and the reconnect
      // picks up the refreshed token through authInterceptor.
      if (outcome.kind !== "refreshed" || req.stream) throw error;
      req.header.set(AUTH_HEADER, bearer(outcome.token));
      return await next(req);
    }

    // Nothing was presented. Re-resolve through the provider and retry once;
    // if the token is still unavailable there is nothing to retry with.
    const refreshed = await getAuthTokenProvider()
      .getToken()
      .catch(() => null);
    if (!refreshed || req.stream) throw error;

    logger.warn(
      "[gRPC Client] retrying tokenless request with a freshly resolved token",
      { service: req.service.typeName, method: req.method.name },
    );
    req.header.set(AUTH_HEADER, bearer(refreshed));
    try {
      return await next(req);
    } catch (retryError) {
      // The retry DID present a credential, so a 401 now is a genuine
      // rejection and must still sign the user out.
      if (
        retryError instanceof ConnectError &&
        retryError.code === Code.Unauthenticated
      ) {
        await recoverFromRejectedToken(req, retryError, bearer(refreshed));
      }
      throw retryError;
    }
  }
};

/**
 * Which server a transport dials.
 *
 * It decides the one backend-specific header: x-daemon-last-seen, a hint only
 * the reliant api-server reads. In a production build the web app calls both
 * servers CROSS-ORIGIN (api.reliantapi.com and admin.reliantapi.com), so every
 * header it sends must be in that server's CORS preflight allow-list or the
 * browser refuses to send the RPC at all. The control-plane admin-server does
 * not read this header and does not allow it, so sending it there blocked
 * every controlplane.v1 RPC once a machine heartbeated.
 */
export type TransportBackend = "reliant-api" | "control-plane";

/**
 * Build the canonical Connect interceptor chain.
 *
 * `withAuth: true` (default) attaches the bearer-token interceptor and the
 * 401-auto-signout interceptor — every authenticated transport in the app
 * gets the full stack. `withAuth: false` is for the pre-auth DevAuth
 * bootstrap transport in `grpc-unauth.ts`; it skips both auth-coupled
 * interceptors but still gets timeouts, tracing, Sentry error logging, and
 * the upgrade-modal interceptor so a quota-exhausted DevAuth call still
 * pops the modal.
 *
 * `backend` (default `"reliant-api"`) names the server the transport dials;
 * `"control-plane"` omits the daemon-last-seen header. See TransportBackend.
 *
 * Order is intentional — see the module-level comment. Do NOT reorder
 * without thinking through the timeout/upgrade/unauth interaction.
 */
export function buildInterceptors(
  options: { withAuth?: boolean; backend?: TransportBackend } = {},
): Interceptor[] {
  const { withAuth = true, backend = "reliant-api" } = options;

  const chain: Interceptor[] = [
    timeoutInterceptor,
    ...(withAuth ? [authInterceptor] : []),
    ...(backend === "reliant-api" ? [daemonLastSeenInterceptor] : []),
    tracingInterceptor,
    errorInterceptor,
    upgradeInterceptor,
    machineWakeInterceptor,
    ...(withAuth ? [unauthInterceptor] : []),
  ];
  return chain;
}

/**
 * Test-only re-export of the createConnectTransport binding so the transport
 * factory tests can mock it via vi.mock without having to also mock its
 * direct usage inside grpc-client.ts / grpc-unauth.ts / controlPlane/client.ts.
 *
 * Production callers should use createConnectTransport from @connectrpc/connect-web
 * directly + buildInterceptors() — not this re-export.
 */
export { createConnectTransport };
