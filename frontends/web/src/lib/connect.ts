/**
 * Every Connect transport in the app, and the ONE place that decides which
 * transport an RPC uses.
 *
 * forge's generated hooks (`src/hooks/*-service-hooks_gen.ts`) call
 * `connectClient(<Service>)`, and so does every hand-written wrapper in
 * `src/api/*-grpc.ts` and every control-plane service module (through
 * `services/controlPlane/client.ts`). This module owns what those calls
 * reach:
 *
 *   - the ROUTE table: which backend each procedure dials (`routeFor`);
 *   - each route's base URL (`reliantApiBaseUrl`, `controlPlaneBaseUrl`);
 *   - each route's Connect transport, built lazily and rebuilt when its base
 *     URL changes (packaged Electron learns its backend URL asynchronously,
 *     from preload, after the bundle has already loaded);
 *   - the test seam (`setTransport`).
 *
 * The interceptor chain every route runs is `buildInterceptors` in
 * `api/transport.ts`: forge's runtime interceptors (error normalization, auth)
 * composed with reliant's own (deadline, daemon-last-seen, tracing, error
 * reporting, upgrade modal, machine wake, 401 recovery). Routes differ only
 * in base URL, wire format and the two chain options — never in which
 * interceptors they get, which is how the transports used to drift apart.
 *
 * ── The routes ─────────────────────────────────────────────────────────
 *
 *   control-plane   `controlplane.v1.*` — control-plane's admin-server.
 *   reliant-unauth  the pre-session sign-in procedures (UNAUTH_PROCEDURES).
 *   reliant-stream  `reliant.v1.StreamingService` — binary wire format.
 *   reliant-api     everything else — reliant's api-server, JSON.
 *
 * Routing is by the procedure's fully-qualified name, so a client resolves
 * its route on every call rather than once at construction. A client built
 * at module load (every generated hook file does exactly that) therefore
 * reaches the right backend even when that backend's URL arrives later.
 *
 * ── Why DaemonRegistryService and TokenService are NOT control-plane ────
 *
 * Both are `reliant.v1.*`, served by reliant's api-server, and must never be
 * routed at admin-server — even though admin-server answers the same paths.
 * `/reliant.v1.DaemonRegistryService/*` returns HTTP 200 from BOTH hosts, from
 * DIFFERENT databases: admin-server's adapter reads `controlplane.daemons`,
 * which never receives a self-hosted daemon's registration, so its answer is
 * a confident, successful, wrong "you have no daemon". admin-server neither
 * serves nor forwards `reliant.v1.TokenService` at all. Both failures only
 * show in a packaged build (dev is same-origin and the Vite proxy sends
 * `/reliant.v1.*` to reliant-api), so `lib/__tests__/connect.routing.test.ts`
 * is the guard.
 */

import { createClient, type Client, type Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import type { DescMethod, DescService } from "@bufbuild/protobuf";
import { buildInterceptors, setCurrentBaseURL, type TransportBackend } from "@/api/transport";
import { loadConfig } from "@/lib/config_gen";
import { logger } from "@/lib/logger";
import {
  API_SERVER_DEFAULT_PORT,
  buildLocalhostUrl,
  isPackagedRendererOrigin,
  isSameOriginTransport,
} from "@/lib/protocol";
import { CONTROL_PLANE_API_URL } from "@/services/controlPlane/config";

// ─── Routes ──────────────────────────────────────────────────────────

export type TransportRoute =
  | "reliant-api"
  | "reliant-stream"
  | "reliant-unauth"
  | "control-plane";

/** Every service in this proto package is served by control-plane's admin-server. */
const CONTROL_PLANE_PACKAGE = "controlplane.v1.";

/**
 * StreamUserUpdates and StreamChatUpdates carry snapshots and heartbeats, so
 * StreamingService is the one hot path kept on the binary wire format; unary
 * RPCs stay JSON for readable network inspection.
 */
const STREAMING_SERVICE = "reliant.v1.StreamingService";

/**
 * Procedures called BEFORE a session exists: the dev-mode session file and
 * the Electron-local OAuth sign-in. They skip the bearer token and the 401
 * recovery — a 401 here means "not signed in yet", not a stale token, and
 * recovering from it would loop.
 *
 * Listed by hand rather than derived from the proto's
 * `(forge.v1.method).auth_required = false`, because that option means "not
 * gated by the server's auth interceptor", NOT "send no credential":
 * TokenService.ExchangeToken and GetGitToken carry it precisely because their
 * handlers authenticate the bearer themselves (token.proto). Health, Info and
 * Version carry it too and are fine either way, so they keep the authed route.
 */
const UNAUTH_PROCEDURES: ReadonlySet<string> = new Set([
  "reliant.v1.SystemService/DevAuthLoad",
  "reliant.v1.SystemService/DevAuthSave",
  "reliant.v1.SystemService/DevAuthClear",
  "reliant.v1.SystemService/StartOAuthSignIn",
]);

/** The route one procedure takes. Exported for the routing tests. */
export function routeFor(serviceTypeName: string, methodName: string): TransportRoute {
  if (serviceTypeName.startsWith(CONTROL_PLANE_PACKAGE) || serviceTypeName === "observability_billing.v1.ObservabilityBillingService") return "control-plane";
  if (UNAUTH_PROCEDURES.has(`${serviceTypeName}/${methodName}`)) return "reliant-unauth";
  if (serviceTypeName === STREAMING_SERVICE) return "reliant-stream";
  return "reliant-api";
}

// ─── Base URLs ───────────────────────────────────────────────────────

/**
 * The reliant api-server's base URL, or null while it is not known yet
 * (packaged Electron before preload has injected RELIANT_CONFIG).
 */
export function reliantApiBaseUrl(): string | null {
  // Same-origin (Vite-proxy) path — see isSameOriginTransport. Whenever the
  // renderer is served over http(s) (web-dev AND electron-dev), reliant.v1.*
  // RPCs go to the document origin and Vite's `/reliant.v1.*` proxy forwards
  // them to reliant-api. This is first-party ⇒ ZERO CORS, and it short-circuits
  // BEFORE RELIANT_CONFIG.grpcUrl / config.js's API_URL fallback below so
  // electron-dev never dials a cross-origin backend port. The packaged app
  // falls through to the daemon URL.
  if (isSameOriginTransport()) {
    return window.location.origin;
  }

  if (typeof window !== "undefined" && window.RELIANT_CONFIG?.isElectron) {
    if (window.RELIANT_CONFIG?.grpcUrl) {
      return window.RELIANT_CONFIG.grpcUrl;
    }
  }

  // Packaged desktop app whose config has not been injected yet: not ready,
  // rather than falling through to the browser defaults below, which would
  // dial a localhost port nothing is listening on.
  if (isPackagedRendererOrigin()) {
    logger.warn("[gRPC Client] Electron detected but backend config not yet available");
    return null;
  }

  // The runtime config's API origin (config.js), else the api-server's
  // default port on loopback (protocol per DISABLE_TLS).
  return loadConfig().API_URL || buildLocalhostUrl(API_SERVER_DEFAULT_PORT);
}

/** True once the reliant api-server's URL is known. */
export function isApiReady(): boolean {
  return reliantApiBaseUrl() !== null;
}

/**
 * control-plane's admin-server base URL, or null when this deployment has no
 * control plane (config.js's `CONTROL_PLANE_API_URL` unset).
 *
 * An http(s)-served renderer (web-dev AND electron-dev) uses the document
 * origin, so Vite's `/controlplane.v1.*` proxy forwards to admin-server —
 * first-party, ZERO CORS. The absolute URL stays the `hasControlPlane` gate and
 * the proxy TARGET (vite.config.ts reads it from process.env); only the
 * packaged app, which has no proxy, dials it directly.
 */
export function controlPlaneBaseUrl(): string | null {
  if (!CONTROL_PLANE_API_URL) return null;
  return isSameOriginTransport() ? window.location.origin : CONTROL_PLANE_API_URL;
}

// ─── Transports ──────────────────────────────────────────────────────

interface RouteSpec {
  baseUrl: () => string | null;
  /** Thrown when baseUrl() is null. Callers match on these strings. */
  notReady: string;
  withAuth: boolean;
  backend: TransportBackend;
  useBinaryFormat: boolean;
}

const RELIANT_NOT_READY = "gRPC client not ready - waiting for backend configuration";

const ROUTES: Record<TransportRoute, RouteSpec> = {
  "reliant-api": {
    baseUrl: reliantApiBaseUrl,
    notReady: RELIANT_NOT_READY,
    withAuth: true,
    backend: "reliant-api",
    useBinaryFormat: false,
  },
  "reliant-stream": {
    baseUrl: reliantApiBaseUrl,
    notReady: RELIANT_NOT_READY,
    withAuth: true,
    backend: "reliant-api",
    useBinaryFormat: true,
  },
  "reliant-unauth": {
    baseUrl: reliantApiBaseUrl,
    notReady: RELIANT_NOT_READY,
    withAuth: false,
    backend: "reliant-api",
    useBinaryFormat: false,
  },
  "control-plane": {
    baseUrl: controlPlaneBaseUrl,
    notReady: "Control plane API URL not configured (CONTROL_PLANE_API_URL)",
    withAuth: true,
    backend: "control-plane",
    useBinaryFormat: false,
  },
};

const _built = new Map<TransportRoute, { baseUrl: string; transport: Transport }>();

/**
 * The concrete Connect transport for one route, built on first use and
 * rebuilt when the route's base URL changes. Throws when the URL is not known.
 *
 * No `credentials: "include"` (forge's scaffold sends a session cookie):
 * reliant authenticates with a bearer token, and a credentialed cross-origin
 * request would need `Access-Control-Allow-Credentials` from both servers.
 */
export function transportFor(route: TransportRoute): Transport {
  const spec = ROUTES[route];
  const baseUrl = spec.baseUrl();
  if (baseUrl === null) {
    if (spec.backend === "reliant-api") {
      logger.error("[gRPC Client] gRPC client not ready - no backend URL available");
    }
    throw new Error(spec.notReady);
  }

  const current = _built.get(route);
  if (current && current.baseUrl === baseUrl) return current.transport;

  if (route === "reliant-api") {
    logger.info("[gRPC Client] Creating transport", {
      baseUrl,
      previousUrl: current?.baseUrl ?? null,
      isElectron: typeof window !== "undefined" && window.RELIANT_CONFIG?.isElectron,
    });
    // Mirrored into transport.ts so errorInterceptor can put it in the
    // reported/log context of a failure.
    setCurrentBaseURL(baseUrl);
  }

  const transport = createConnectTransport({
    baseUrl,
    interceptors: buildInterceptors({ withAuth: spec.withAuth, backend: spec.backend }),
    useBinaryFormat: spec.useBinaryFormat,
  });
  _built.set(route, { baseUrl, transport });
  return transport;
}

/**
 * A Transport that picks the transport to use on every call. Resolving per
 * call, not per client, is what lets a client built at module load follow a
 * base URL that changes (or first appears) later, and what lets
 * setTransport() reach clients that already exist.
 */
function routed(pick: (method: DescMethod) => Transport | Promise<Transport>): Transport {
  return {
    async unary(method, signal, timeoutMs, header, input, contextValues) {
      const target = await pick(method);
      return target.unary(method, signal, timeoutMs, header, input, contextValues);
    },
    async stream(method, signal, timeoutMs, header, input, contextValues) {
      const target = await pick(method);
      return target.stream(method, signal, timeoutMs, header, input, contextValues);
    },
  };
}

/** The real backends, routed. */
const live = routed((method) => transportFor(routeFor(method.parent.typeName, method.name)));

// ─── Mock mode and the test seam ─────────────────────────────────────

// VITE_MOCK_API (forge's mock mode):
//   "true"   — every RPC hits the generated mock transport (scenarios +
//              fixtures); no backend is contacted.
//   "hybrid" — a scenario's handlers overlay the real backends; RPCs it does
//              not match are forwarded to them when it sets passthrough.
//   unset    — the real backends.
//
// The mock module is imported dynamically: it resolves the `?scenario=` URL
// parameter at module init, and a production build — where Vite replaces this
// variable with undefined and drops the branch — must not carry it at all.
const MOCK_MODE = import.meta.env.VITE_MOCK_API as string | undefined;

let _mock: Promise<Transport> | null = null;
function mockTransport(): Promise<Transport> {
  _mock ??= import("@/lib/mock-transport_gen").then(({ createMockTransport }) =>
    MOCK_MODE === "hybrid" ? createMockTransport(live) : createMockTransport(),
  );
  return _mock;
}

let _override: Transport | null = null;

/**
 * Test seam: send EVERY route to `t` (null restores the real routing).
 * `renderWithTransport` in src/lib/test-utils and the generated hook tests
 * call it; production code never does.
 */
export function setTransport(t: Transport | null): void {
  _override = t;
}

/**
 * The app's transport: every route, behind the test seam and mock mode.
 * `createClient(Service, transport)` is what connectClient() does.
 */
export const transport: Transport = routed((method) => {
  if (_override) return _override;
  if (MOCK_MODE === "true" || MOCK_MODE === "hybrid") return mockTransport();
  return transportFor(routeFor(method.parent.typeName, method.name));
});

const _clients = new WeakMap<DescService, Client<DescService>>();

/**
 * A typed Connect client for one service, on the routed transport.
 *
 *   const client = connectClient(QuestionService);
 *   const res = await client.listPendingQuestions({});
 *
 * Memoized per descriptor. That is safe because the client holds no
 * transport of its own: each call resolves its route, base URL and the test
 * seam afresh.
 */
export function connectClient<S extends DescService>(service: S): Client<S> {
  let client = _clients.get(service) as Client<S> | undefined;
  if (!client) {
    client = createClient(service, transport);
    _clients.set(service, client as Client<DescService>);
  }
  return client;
}
