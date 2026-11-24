// Copyright (c) 2025 Reliant Labs

/**
 * Fixture mode's fetch boundary: what the page may still reach once every
 * Connect RPC is answered in the page (install.ts). Loaded only in fixture
 * mode. A fixture page reaches no backend, so window.fetch is wrapped and
 * every request is one of:
 *
 *   rpc        a Connect RPC URL. It THROWS: an RPC that reaches fetch has
 *              escaped the fixture transport (a client built on its own
 *              transport, say), and would otherwise dial a live backend.
 *   auth       the configured Supabase project's `/auth/v1/*`, answered here
 *              with a fake session for the pack's user (written to
 *              localStorage before the app reads it), until the session
 *              port (phase 3) replaces it.
 *   health     GET `/health`, `/healthz`, `/readyz` on the page or configured
 *              API origin: "ok", answered locally (never sent).
 *   telemetry  the same-origin dev log sink and OTLP ingest (`/_otel`,
 *              `/v1/traces|logs|metrics`), and OTLP signal paths beneath the
 *              configured OTEL_ENDPOINT, for POSTs only: 204, nothing sent.
 *   page       a same-origin GET (the bundle's assets, config.js,
 *              version.json): the real fetch.
 *   refused    everything else, cross-origin or a same-origin write. It
 *              THROWS, as a network error would.
 *
 * Every rpc and refused request is recorded (`escaped`, `outside` on
 * window.__forgeFixtures) so the harness can fail the surface that made it.
 */

import { loadConfig } from "@/lib/config_gen";

export type OutsideKind = "rpc" | "auth" | "health" | "telemetry" | "page" | "refused";

/** A Connect RPC path: `/<pkg>.v<N>.<Service>/<Method>`, as the transports build it. */
const RPC_PATH = /\/([a-z_]\w*(?:\.[a-z_]\w*)*\.v\d\w*(?:\.[a-z_]\w*)*\.[A-Z]\w*)\/([A-Z]\w*)$/;
const HEALTH_PATH = /^\/(health|healthz|readyz)$/;
const TELEMETRY_PATH = /^(?:\/__forge\/log|(?:\/_otel)?\/v1\/(?:traces|logs|metrics))$/;
const OTLP_PATH = /^\/v1\/(?:traces|logs|metrics)$/;

export interface OutsideContext {
  /** The page's origin. */
  origin: string;
  /** The configured API's origin: its GET health probe is answered locally. */
  apiOrigin?: string | null;
  /** The configured Supabase project's origin, if any. */
  supabaseOrigin: string | null;
  /** The configured telemetry ingest's origin, if any. */
  telemetryOrigin: string | null;
  /** Its path prefix, without a trailing slash (e.g. /_otel or /k/<key>). */
  telemetryPath?: string;
}

const originOf = (url: string, base: string): string | null => {
  try {
    return url ? new URL(url, base).origin : null;
  } catch {
    return null;
  }
};

/** The page's context, from its location and config.js. */
export function outsideContext(): OutsideContext {
  const origin = window.location.origin;
  const config = loadConfig();
  let telemetry: URL | null = null;
  try {
    const endpoint = config.OTEL_ENDPOINT.trim();
    if (endpoint && endpoint.toLowerCase() !== "off") telemetry = new URL(endpoint, origin);
  } catch {
    // Invalid telemetry configuration grants no network exemption.
  }
  return {
    origin,
    apiOrigin: originOf(config.API_URL, origin),
    supabaseOrigin: originOf(config.SUPABASE_URL, origin),
    telemetryOrigin: telemetry?.origin ?? null,
    telemetryPath: telemetry?.pathname.replace(/\/+$/, "") ?? "",
  };
}

/** Which of the boundary's cases one request is. Pure; see the module header. */
export function classifyOutside(url: URL, method: string, ctx: OutsideContext): OutsideKind {
  if (RPC_PATH.test(url.pathname)) return "rpc";
  if (ctx.supabaseOrigin && url.origin === ctx.supabaseOrigin && url.pathname.startsWith("/auth/v1/")) return "auth";
  if (method === "GET" && (url.origin === ctx.origin || url.origin === ctx.apiOrigin) && HEALTH_PATH.test(url.pathname)) return "health";
  const prefix = ctx.telemetryPath ?? "";
  const configuredSignal = url.origin === ctx.telemetryOrigin
    && url.pathname.startsWith(`${prefix}/`)
    && OTLP_PATH.test(url.pathname.slice(prefix.length));
  if (method === "POST" && ((url.origin === ctx.origin && TELEMETRY_PATH.test(url.pathname)) || configuredSignal)) {
    return "telemetry";
  }
  if (url.origin === ctx.origin && method === "GET") return "page";
  return "refused";
}

/** The pack's signed-in user, as the fake session names them. */
export interface OutsideUser {
  id: string;
  email: string;
  name: string;
}

export interface OutsideOptions {
  user: OutsideUser;
  /** The pack clock's instant (epoch ms): the session is issued at it and valid for a month. */
  now: number;
  /** Told of every request the boundary throws on. */
  onBlocked: (kind: "rpc" | "refused", request: string) => void;
}

/** localStorage key lib/supabase.ts keeps a per-origin session under. */
export const SESSION_STORAGE_KEY = "supabase-auth";

/** A Supabase session for `user`, issued at `now`; its token is shaped like a JWT and signed by nobody. */
export function fakeSession(user: OutsideUser, now: number) {
  const secs = Math.floor(now / 1000);
  const b64url = (o: unknown) => btoa(JSON.stringify(o)).replace(/=+$/, "").replace(/\+/g, "-").replace(/\//g, "_");
  const createdAt = "2026-09-01T12:00:00.000Z";
  const issued = new Date(now).toISOString();
  const authUser = {
    id: user.id, aud: "authenticated", role: "authenticated", email: user.email,
    email_confirmed_at: createdAt, phone: "", confirmed_at: createdAt, last_sign_in_at: issued,
    app_metadata: { provider: "email", providers: ["email"] },
    user_metadata: { full_name: user.name, name: user.name },
    identities: [], created_at: createdAt, updated_at: issued, is_anonymous: false,
  };
  const expiresIn = 86400 * 30;
  const accessToken = [
    b64url({ alg: "ES256", typ: "JWT", kid: "fixtures" }),
    b64url({ sub: user.id, email: user.email, role: "authenticated", aud: "authenticated", exp: secs + expiresIn, iat: secs, session_id: "fixture-session", is_anonymous: false }),
    "Zml4dHVyZXM",
  ].join(".");
  return { access_token: accessToken, token_type: "bearer", expires_in: expiresIn, expires_at: secs + expiresIn, refresh_token: "fixture-refresh", user: authUser };
}

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });

/**
 * Install the boundary: write the fake session, then wrap window.fetch.
 * Returns the function that restores the previous fetch (tests).
 */
export function installOutside({ user, now, onBlocked }: OutsideOptions): () => void {
  const session = fakeSession(user, now);
  try {
    localStorage.setItem(SESSION_STORAGE_KEY, JSON.stringify(session));
  } catch {
    // Storage disabled: the app shows its signed-out state, which the harness reports.
  }
  const ctx = outsideContext();
  const answerAuth = (url: URL): Response => {
    if (url.pathname === "/auth/v1/user") return json(200, session.user);
    if (url.pathname === "/auth/v1/token") return json(200, session);
    if (url.pathname === "/auth/v1/logout") return new Response(null, { status: 204 });
    if (url.pathname === "/auth/v1/settings") return json(200, { external: { email: true, github: true, google: true }, disable_signup: false });
    return json(200, {});
  };

  const realFetch = window.fetch;
  const fixtureFetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const href = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const url = new URL(href, window.location.href);
    const method = (init?.method ?? (input instanceof Request ? input.method : "GET")).toUpperCase();
    switch (classifyOutside(url, method, ctx)) {
      case "auth":
        return answerAuth(url);
      case "health":
        return json(200, { status: "ok" });
      case "telemetry":
        return new Response(null, { status: 204 });
      case "page":
        return realFetch.call(window, input, init);
      case "rpc":
        onBlocked("rpc", `${method} ${url.href}`);
        throw new TypeError(`fixture mode: an rpc escaped the fixture transport (${url.pathname})`);
      default:
        onBlocked("refused", `${method} ${url.href}`);
        throw new TypeError(`fixture mode: the network outside the page is off (${method} ${url.origin}${url.pathname})`);
    }
  };
  window.fetch = fixtureFetch as typeof window.fetch;
  return () => {
    if (window.fetch === fixtureFetch) window.fetch = realFetch;
  };
}
