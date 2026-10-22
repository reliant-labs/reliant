import { createConnectTransport } from "@connectrpc/connect-web";
import { createClient, type Transport } from "@connectrpc/connect";
import { buildRuntimeInterceptors } from "@reliantlabs/forge-web-runtime";
import type { DescService } from "@bufbuild/protobuf";
import { createMockTransport } from "@/lib/mock-transport_gen";
import { apiBase } from "./api-base";
import { DEV_API_URL } from "./apiurl_gen";

// The API URL is read from VITE_API_URL (Vite exposes import.meta.env vars
// prefixed with VITE_ to client code). In dev it falls back to DEV_API_URL
// — regenerated on every `forge generate` (src/lib/apiurl_gen.ts) so it
// tracks the port the backend actually binds. In a deployable build the
// per-env VITE_API_URL is injected from KCL (forge.Frontend config /
// env_vars), so this floor is dev-only.
//
// Under the dev server, apiBase() answers with this page's own origin
// instead (`/_api`, which vite.config.ts proxies to the API), so the dev loop
// works from any host the dev server is opened on — see src/lib/api-base.ts.
function apiBaseUrl(): string {
  return apiBase((import.meta.env.VITE_API_URL as string | undefined) ?? DEV_API_URL);
}


// The forge frontend runtime owns the interceptor stack: brand headers, W3C
// traceparent propagation (so backend spans join the same trace — always on,
// no collector required), and typed error normalization. It ships as
// @reliantlabs/forge-web-runtime. Retries are not in the chain: React Query
// (src/lib/query-client.ts) is the single retry owner, so a mutation is
// never re-sent.
//
// NO TOKEN GETTER, and nothing here sets an Authorization header. Sign-in is
// native: the browser POSTs credentials to this app's own API, the server
// runs the whole OIDC flow, and the session comes back as an HttpOnly
// cookie. This bundle cannot read that cookie — a token no script can reach
// is a token an XSS cannot steal — so there is no token to attach. The
// cookie travels on the fetch below instead. See src/lib/auth/native-login.ts.
const interceptors = buildRuntimeInterceptors({});

// Send the session cookie with every RPC.
//
// "include", not the browser's "same-origin" default: whenever the API is on
// another origin — a deployed API_URL naming its own host, or the dev loop
// with FORGE_DEV_API_PROXY=off, where the API binds its own port — the
// default silently DROPS the cookie: the calls go out unauthenticated, the
// backend answers 401, and nothing in the console explains why. The server
// pairs this with cors_allow_credentials. Through the dev proxy the requests
// are same-origin and this costs nothing. native-login.ts makes the
// identical choice for the same reason.
const fetchWithSession: typeof globalThis.fetch = (input, init) =>
  fetch(input, { ...init, credentials: "include" });

function buildTransport(): Transport {
  // VITE_MOCK_API has three modes:
  //   "true"   — pure mock. All RPCs hit the mock transport (scenarios +
  //              entity fixtures). No backend contacted. Use for offline
  //              UI dev and CI tests.
  //   "hybrid" — scenarios overlay a real backend. RPCs matched by a
  //              scenario handler return the stubbed response; everything
  //              else (when the active scenario has passthrough:true) is
  //              forwarded to the real Connect transport. Use for LLM /
  //              Chrome MCP debugging where you want to stub one endpoint
  //              and exercise the rest of the stack for real.
  //   anything else (default) — real backend, real auth.
  //
  // The mock transport is generated at src/lib/mock-transport_gen.ts by the
  // frontend scaffold, and re-rendered with entity fixtures by every
  // `forge generate` — so this static import resolves from the moment the
  // frontend exists, which Vite's ESM pipeline requires (the older
  // try/require pattern was broken in browser ESM and silently kept mock
  // mode off).
  const mode = import.meta.env.VITE_MOCK_API;
  const baseUrl = apiBaseUrl();
  if (mode === "true") {
    return createMockTransport();
  }
  if (mode === "hybrid") {
    const real = createConnectTransport({
      baseUrl,
      interceptors,
      fetch: fetchWithSession,
    });
    return createMockTransport(real);
  }

  return createConnectTransport({
    baseUrl,
    interceptors,
    fetch: fetchWithSession,
  });
}

// Active transport. Module-level so the per-call connectClient() helper
// resolves the current transport (not the one captured at module load) —
// tests swap this via setTransport() in src/lib/test-utils to drive
// page-level integration tests against a mock without rebuilding the
// generated hook modules.
let _transport: Transport = buildTransport();

export function setTransport(t: Transport): void {
  _transport = t;
}

// Re-export the active transport as a Proxy-backed live binding. Direct
// access (e.g. `import { transport } from "@/lib/connect"`) always sees
// the current value, even after a test calls setTransport().
export const transport = new Proxy({} as Transport, {
  get(_target, prop, receiver) {
    return Reflect.get(_transport as object, prop, receiver);
  },
});

/**
 * connectClient creates a typed Connect client for a given service descriptor.
 *
 * Usage:
 *   import { UserService } from "../gen/user/v1/user_pb";
 *
 *   const client = connectClient(UserService);
 *   const user = await client.getUser({ id: "1" });
 *
 * Resolves the active transport on every call via the live binding so
 * tests can swap it via setTransport() between cases. *
 * Every generated hook calls connectClient(<Service>), so this function is
 * the ONE place that decides which transport each service uses. To send
 * some services to another backend (a second API, a control plane, an
 * unauthenticated endpoint), route on the descriptor's fully-qualified name
 * here rather than wrapping the generated hooks:
 *
 *   const billing = createConnectTransport({ baseUrl: BILLING_URL, interceptors });
 *   const routes: Record<string, Transport> = {
 *     "billing.v1.InvoiceService": billing,
 *   };
 *   export function connectClient<S extends DescService>(service: S) {
 *     return createClient(service, routes[service.typeName] ?? transport);
 *   }
 *
 * Keep the default branch on `transport` (the live binding) so
 * setTransport() in tests still reaches every service. An app adopting
 * forge with a transport layer of its own points this at that layer the
 * same way: forge only ever calls connectClient().
 */
export function connectClient<S extends DescService>(service: S) {
  return createClient(service, transport);
}
