/**
 * The Content-Security-Policy the build injects.
 *
 * These assertions are about the SHIPPED artifact, not about source style: a
 * CSP that is subtly too tight breaks a payment form or the editor in
 * production, and one that is too loose is decoration. Both failures are
 * invisible in review, which is why they are pinned here.
 */

import { describe, it, expect } from "vitest";
import {
  buildCSP,
  inlineScripts,
  sha256,
} from "../../../vite-plugin-csp";

/** The prod endpoint set, as control-plane's KCL renders it. */
const PROD_ENV = {
  VITE_API_URL: "https://api.reliantapi.com",
  VITE_GRPC_URL: "https://api.reliantapi.com",
  VITE_CONTROL_PLANE_API_URL: "https://admin.reliantapi.com",
  VITE_GATEWAY_URL: "https://gateway.reliantapi.com",
  VITE_SUPABASE_URL: "https://dash.reliantlabs.io",
  VITE_APP_URL: "https://app.reliantlabs.io",
  VITE_SENTRY_DSN:
    "https://c84a8011e134e0b905db7ba5f10dbc8f@o4509000353447936.ingest.us.sentry.io/4509933645856778",
} as NodeJS.ProcessEnv;

/** Pull one directive's value out of a policy string. */
function directive(policy: string, name: string): string {
  const found = policy
    .split(";")
    .map((part) => part.trim())
    .find((part) => part.startsWith(`${name} `));
  return found ? found.slice(name.length + 1) : "";
}

describe("buildCSP", () => {
  it("allows every backend the bundle was built to call", () => {
    const connect = directive(buildCSP(PROD_ENV, []), "connect-src");

    // Each of these is a distinct outage if missing: the API, the control
    // plane (billing/coupons), the daemon gateway, and auth.
    expect(connect).toContain("https://api.reliantapi.com");
    expect(connect).toContain("https://admin.reliantapi.com");
    expect(connect).toContain("https://gateway.reliantapi.com");
    expect(connect).toContain("https://dash.reliantlabs.io");
  });

  it("derives allowed origins from the build env rather than hardcoding them", () => {
    // The endpoint table lives in control-plane's KCL. If this policy carried
    // its own copy, a new environment would silently ship a CSP naming prod.
    const staging = buildCSP(
      { VITE_API_URL: "https://api.staging.example.com" } as NodeJS.ProcessEnv,
      [],
    );

    expect(directive(staging, "connect-src")).toContain(
      "https://api.staging.example.com",
    );
    expect(directive(staging, "connect-src")).not.toContain("reliantapi.com");
  });

  it("allows the websocket upgrade for each https backend", () => {
    // connect-src matches on scheme, so the terminal's wss:// connection is
    // NOT covered by the https:// entry for the same host.
    expect(directive(buildCSP(PROD_ENV, []), "connect-src")).toContain(
      "wss://api.reliantapi.com",
    );
  });

  it("allows loopback on any port for the packaged daemon and auth serve", () => {
    const connect = directive(buildCSP(PROD_ENV, []), "connect-src");

    // The desktop daemon's port is chosen at launch, so this cannot be pinned.
    expect(connect).toContain("http://127.0.0.1:*");
    expect(connect).toContain("ws://127.0.0.1:*");
  });

  it("allows Stripe.js and the iframes it mounts", () => {
    const policy = buildCSP(PROD_ENV, []);

    expect(directive(policy, "script-src")).toContain("https://js.stripe.com");
    expect(directive(policy, "frame-src")).toContain("https://js.stripe.com");
    expect(directive(policy, "frame-src")).toContain("https://hooks.stripe.com");
  });

  it("allows the Monaco CDN as script, style and worker source", () => {
    const policy = buildCSP(PROD_ENV, []);

    expect(directive(policy, "script-src")).toContain("https://cdn.jsdelivr.net");
    expect(directive(policy, "style-src")).toContain("https://cdn.jsdelivr.net");
    // Monaco spawns its language workers from generated blob URLs.
    expect(directive(policy, "worker-src")).toContain("blob:");
  });

  it("allows the Sentry ingest host derived from the DSN", () => {
    // A DSN is https://<key>@<host>/<project> — the credential must not end up
    // in the origin, or the directive silently matches nothing.
    const connect = directive(buildCSP(PROD_ENV, []), "connect-src");

    expect(connect).toContain("https://o4509000353447936.ingest.us.sentry.io");
    expect(connect).not.toContain("c84a8011e134e0b905db7ba5f10dbc8f");
  });

  it("keeps script-src strict: no unsafe-inline, no unsafe-eval", () => {
    // This is the whole point of the policy. Monaco's AMD loader probes
    // `new Function` in a try/catch and falls back to importScripts, so
    // unsafe-eval is NOT needed — do not add it to "fix" the editor.
    const scriptSrc = directive(buildCSP(PROD_ENV, []), "script-src");

    expect(scriptSrc).not.toContain("'unsafe-inline'");
    expect(scriptSrc).not.toContain("'unsafe-eval'");
  });

  it("denies plugins and pins base-uri", () => {
    const policy = buildCSP(PROD_ENV, []);

    expect(directive(policy, "object-src")).toBe("'none'");
    // Blocks an injected <base> from re-pointing every relative asset URL.
    expect(directive(policy, "base-uri")).toBe("'self'");
  });

  it("omits directives a meta tag cannot enforce", () => {
    // frame-ancestors and report-uri are ignored in a meta tag. Emitting them
    // would imply a protection that is not there; clickjacking is covered by
    // X-Frame-Options on the server, and app:// cannot be framed at all.
    const policy = buildCSP(PROD_ENV, []);

    expect(policy).not.toContain("frame-ancestors");
    expect(policy).not.toContain("report-uri");
  });

  it("tolerates an absent or unparseable endpoint instead of emitting junk", () => {
    // A local build has no VITE_SENTRY_DSN. An empty entry would produce
    // "connect-src 'self'  https://…" or, worse, a bare "undefined" token
    // that invalidates the directive.
    const policy = buildCSP(
      { VITE_API_URL: "not-a-url", VITE_SENTRY_DSN: "" } as NodeJS.ProcessEnv,
      [],
    );

    expect(policy).not.toContain("undefined");
    expect(policy).not.toContain("  ");
  });
});

describe("inline script hashing", () => {
  it("hashes the inline theme bootstrap so it runs without unsafe-inline", () => {
    const html = `<html><head><script>var a = 1;</script></head></html>`;
    const bodies = inlineScripts(html);

    expect(bodies).toEqual(["var a = 1;"]);

    const policy = buildCSP(PROD_ENV, bodies.map(sha256));
    expect(directive(policy, "script-src")).toContain(`'${sha256("var a = 1;")}'`);
  });

  it("ignores external scripts, which are authorised by origin not by hash", () => {
    const html = `<script type="module" src="/assets/index-abc.js"></script>`;

    expect(inlineScripts(html)).toEqual([]);
  });

  it("produces a hash that changes with the script body", () => {
    // The guarantee that matters: the hash is computed from the real document
    // at build time, so editing the bootstrap cannot leave a stale hash behind.
    expect(sha256("var a = 1;")).not.toBe(sha256("var a = 2;"));
  });
});
