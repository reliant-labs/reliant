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
 *   timeout → auth → daemon-last-seen → tracing → error-logging → upgrade-modal → 401-signout
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
 *   - unauthInterceptor (401 → signOut) lives innermost so it doesn't gobble
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
    logger.info("[gRPC Client] Auth token set for request:", {
      method: req.method.name,
      tokenLength: token.length,
    });
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
  CreateChat: CHAT_OPERATION_TIMEOUT_MS,
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

// ─── In-flight unary RPC registry (starvation diagnostics) ───────────
// During the 2026-07-09 incident a hung daemon command (worktree.git_changes)
// left GetWorktreeChanges pending; every later unary RPC queued behind it and
// the console showed NOTHING until the first client timeout fired — with no
// hint of what was blocking. This registry lets the timeout handler print a
// snapshot of every in-flight unary RPC so a single console line identifies
// the wedge.
let _nextInFlightId = 0;
const _inFlightUnary = new Map<number, { method: string; startedAt: number }>();

const IN_FLIGHT_DESCRIBE_CAP = 8;

/**
 * Pure formatter for the in-flight diagnostic line, e.g.
 * "7 in flight, oldest: GetWorktreeChanges 43s [GetWorktreeChanges:43s, ListApprovalsByChat:9s, +1 more]"
 * Exported for tests; production code goes through describeInFlight().
 */
export function formatInFlight(
  entries: ReadonlyArray<{ method: string; startedAt: number }>,
  now: number,
): string {
  if (entries.length === 0) return "0 in flight";
  const oldestFirst = [...entries].sort((a, b) => a.startedAt - b.startedAt);
  const age = (e: { startedAt: number }) =>
    `${Math.round((now - e.startedAt) / 1000)}s`;
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

/** Snapshot of currently in-flight unary RPCs, oldest first. */
export function describeInFlight(): string {
  return formatInFlight([..._inFlightUnary.values()], Date.now());
}

// Rate-limit rpc-timeout Sentry events: a wedged connection times out many
// queued RPCs in a burst, and each event would carry the same diagnostic
// snapshot — one event per minute captures the incident without flooding.
const RPC_TIMEOUT_REPORT_INTERVAL_MS = 60_000;
let _lastRpcTimeoutReportAt = 0;

function reportRpcTimeout(
  method: string,
  timeoutMs: number,
  diagnostics: string,
): void {
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

// Timeout interceptor to prevent requests from hanging indefinitely.
// Races the RPC against a timer since req.signal is readonly and timeoutMs
// is consumed by the transport before interceptors run.
const timeoutInterceptor: Interceptor = (next) => async (req) => {
  const methodName = req.method.name;
  const timeoutMs = LONG_TIMEOUT_METHODS[methodName] ?? DEFAULT_GRPC_TIMEOUT_MS;

  // Skip timeout for streaming methods (timeout = 0)
  if (timeoutMs === 0) {
    return next(req);
  }

  const inFlightId = _nextInFlightId++;
  _inFlightUnary.set(inFlightId, {
    method: methodName,
    startedAt: Date.now(),
  });

  const controller = new AbortController();
  const timer = setTimeout(() => {
    // Log BEFORE aborting so the snapshot still includes this RPC and every
    // other call queued behind the same wedged connection — this line alone
    // would have diagnosed the 2026-07-09 starvation from the console.
    const diagnostics = describeInFlight();
    logger.error(
      `[gRPC Client] ${methodName} timed out after ${timeoutMs}ms — ${diagnostics}`,
    );
    reportRpcTimeout(methodName, timeoutMs, diagnostics);
    controller.abort(
      new ConnectError(
        `${methodName} timed out after ${timeoutMs}ms`,
        Code.DeadlineExceeded,
      ),
    );
  }, timeoutMs);

  // Abort our timer if the request's own signal fires first
  const onUpstreamAbort = () => {
    clearTimeout(timer);
    controller.abort(req.signal.reason);
  };
  if (req.signal.aborted) {
    clearTimeout(timer);
    _inFlightUnary.delete(inFlightId);
    throw ConnectError.from(req.signal.reason);
  }
  req.signal.addEventListener("abort", onUpstreamAbort);

  try {
    return await Promise.race([
      next(req),
      new Promise<never>((_, reject) => {
        controller.signal.addEventListener("abort", () => {
          reject(ConnectError.from(controller.signal.reason));
        });
      }),
    ]);
  } finally {
    clearTimeout(timer);
    _inFlightUnary.delete(inFlightId);
    req.signal.removeEventListener("abort", onUpstreamAbort);
  }
};

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

// Connect error codes that are client-side / expected and should NOT be reported to Sentry.
const SENTRY_SKIP_CODES = new Set([
  Code.Canceled,
  Code.InvalidArgument,
  Code.NotFound,
  Code.AlreadyExists,
  Code.PermissionDenied,
  Code.Unauthenticated,
  Code.FailedPrecondition,
  Code.Aborted,
  Code.OutOfRange,
  Code.ResourceExhausted,
]);

// Error logging interceptor
const errorInterceptor: Interceptor = (next) => async (req) => {
  const startTime = Date.now();
  try {
    logger.info("[gRPC Client] Request starting:", {
      service: req.service.typeName,
      method: req.method.name,
      baseUrl: _currentBaseURL,
      isElectron: typeof window !== "undefined" ? !!window.electronAPI : false,
      protocol:
        typeof window !== "undefined" ? window.location.protocol : undefined,
      hasReliantConfig:
        typeof window !== "undefined" ? !!window.RELIANT_CONFIG : false,
      configGrpcUrl:
        typeof window !== "undefined"
          ? window.RELIANT_CONFIG?.grpcUrl
          : undefined,
    });
    const result = await next(req);
    const duration = Date.now() - startTime;
    logger.debug("[gRPC Client] Request succeeded:", {
      service: req.service.typeName,
      method: req.method.name,
      durationMs: duration,
    });
    return result;
  } catch (error) {
    const duration = Date.now() - startTime;
    // Log detailed error info
    logger.error("[gRPC Client] Request failed:", {
      service: req.service.typeName,
      method: req.method.name,
      baseUrl: _currentBaseURL,
      isElectron: typeof window !== "undefined" ? !!window.electronAPI : false,
      protocol:
        typeof window !== "undefined" ? window.location.protocol : undefined,
      durationMs: duration,
      error,
      errorMessage: error instanceof Error ? error.message : String(error),
      errorCode: (error as { code?: unknown })?.code,
      errorName: (error as { name?: unknown })?.name,
      errorCause: (error as { cause?: unknown })?.cause,
    });

    // Report non-trivial errors to Sentry
    const shouldReport =
      !(error instanceof ConnectError) || !SENTRY_SKIP_CODES.has(error.code);

    if (shouldReport) {
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

// Auto-sign-out on 401 with an active session.
//
// When the backend rejects a stored token (most often because the token was
// issued by a different Supabase project, or the user was deleted server-side,
// or signing keys rotated), we clear the local session and redirect to /auth.
// Without this, users get stuck on a loading screen with no recourse.
//
// Safety guards:
// - Only fires when a session/API key is currently set (no sign-in→sign-out loops).
// - Single-flight: a burst of parallel 401s triggers exactly one sign-out.
// - Skipped on /auth so a 401 there doesn't trigger a redirect to itself.
//
// The guard is released on a timer rather than left latched for the redirect to
// tear down. Assuming the navigation always happens is what turned a single bad
// token into an unbounded loop once Electron's will-navigate handler cancelled
// the redirect: the page survived, so nothing ever reset the flag or stopped the
// dead session from firing. Re-arming after a delay means that if the redirect
// does land the timer dies with the page, and if it is blocked we retry at a
// bounded rate instead of hammering or wedging permanently.
const SIGN_OUT_RETRY_MS = 10_000;
let _signOutInFlight = false;

function releaseSignOutGuard(delayMs = SIGN_OUT_RETRY_MS) {
  if (typeof window === "undefined") {
    _signOutInFlight = false;
    return;
  }
  window.setTimeout(() => {
    _signOutInFlight = false;
  }, delayMs);
}
const unauthInterceptor: Interceptor = (next) => async (req) => {
  try {
    return await next(req);
  } catch (error) {
    if (
      !(error instanceof ConnectError) ||
      error.code !== Code.Unauthenticated ||
      _signOutInFlight
    ) {
      throw error;
    }

    // Deliberately `hasSession()` rather than `getToken()`: this only needs to
    // know whether a session is believed active, and a burst of 401s must not
    // stampede the provider's refresh path.
    const hasSession = await getAuthTokenProvider().hasSession();
    if (!hasSession) throw error;

    _signOutInFlight = true;
    logger.warn(
      "[gRPC Client] 401 with active session — token rejected by backend; signing out",
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
      // cancels it, the guard re-arms so we neither spin nor wedge.
      releaseSignOutGuard();
    } else {
      _signOutInFlight = false;
    }

    throw error;
  }
};

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
 * Order is intentional — see the module-level comment. Do NOT reorder
 * without thinking through the timeout/upgrade/unauth interaction.
 */
export function buildInterceptors(
  options: { withAuth?: boolean } = {},
): Interceptor[] {
  const { withAuth = true } = options;

  const chain: Interceptor[] = [
    timeoutInterceptor,
    ...(withAuth ? [authInterceptor] : []),
    daemonLastSeenInterceptor,
    tracingInterceptor,
    errorInterceptor,
    upgradeInterceptor,
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
