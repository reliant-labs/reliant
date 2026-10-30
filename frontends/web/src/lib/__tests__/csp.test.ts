/**
 * The Content-Security-Policy the build injects.
 *
 * These assertions are about the SHIPPED artifact, not about source style: a
 * CSP that is subtly too tight breaks a payment form or the editor in
 * production, and one that is too loose is decoration. Both failures are
 * invisible in review, which is why they are pinned here.
 */

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, describe, it, expect } from "vitest";
import {
  buildCSP,
  inlineScripts,
  runtimeConnectScript,
  sha256,
  withRuntimeConnectPolicy,
} from "../../../vite-plugin-csp";

/** The prod endpoint set, as control-plane's KCL renders it into config.js. */
const PROD_CONFIG = {
  API_URL: "https://api.reliantapi.com",
  CONTROL_PLANE_API_URL: "https://admin.reliantapi.com",
  GATEWAY_URL: "https://gateway.reliantapi.com",
  SUPABASE_URL: "https://dash.reliantlabs.io",
  APP_URL: "https://app.reliantlabs.io",
  OTEL_ENDPOINT: "/_otel",
};

/** Pull one directive's value out of a policy string. */
function directive(policy: string, name: string): string {
  const found = policy
    .split(";")
    .map((part) => part.trim())
    .find((part) => part.startsWith(`${name} `));
  return found ? found.slice(name.length + 1) : "";
}

/**
 * Run the SHIPPED bootstrap against a config.js document and return the
 * policy it appended. Executing the real string, rather than a TS twin of its
 * logic, is what makes this a test of the artifact.
 */
function runtimePolicy(config: Record<string, unknown> | undefined): string {
  window.__FORGE_CONFIG__ = config;
  new Function(runtimeConnectScript())();
  const metas = document.head.querySelectorAll('meta[http-equiv="Content-Security-Policy"]');
  return metas[metas.length - 1]?.getAttribute("content") ?? "";
}

afterEach(() => {
  document.head
    .querySelectorAll('meta[http-equiv="Content-Security-Policy"]')
    .forEach((m) => m.remove());
  window.__FORGE_CONFIG__ = {};
});

describe("runtime connect-src (from config.js)", () => {
  it("allows every backend the runtime config names", () => {
    const connect = directive(runtimePolicy(PROD_CONFIG), "connect-src");

    // Each of these is a distinct outage if missing: the API, the control
    // plane (billing/coupons), the daemon gateway, and auth.
    expect(connect).toContain("https://api.reliantapi.com");
    expect(connect).toContain("https://admin.reliantapi.com");
    expect(connect).toContain("https://gateway.reliantapi.com");
    expect(connect).toContain("https://dash.reliantlabs.io");
  });

  it("derives allowed origins from the document rather than hardcoding them", () => {
    // The endpoint table lives in control-plane's KCL. If this policy carried
    // its own copy, a new environment would silently ship a CSP naming prod.
    const staging = directive(
      runtimePolicy({ API_URL: "https://api.staging.example.com" }),
      "connect-src",
    );

    expect(staging).toContain("https://api.staging.example.com");
    expect(staging).not.toContain("reliantapi.com");
  });

  it("allows the websocket upgrade for each https backend", () => {
    // connect-src matches on scheme, so the terminal's wss:// connection is
    // NOT covered by the https:// entry for the same host.
    expect(directive(runtimePolicy(PROD_CONFIG), "connect-src")).toContain(
      "wss://api.reliantapi.com",
    );
  });

  it("allows loopback on any port for the packaged daemon and auth serve", () => {
    const connect = directive(runtimePolicy(PROD_CONFIG), "connect-src");

    // The desktop daemon's port is chosen at launch, so this cannot be pinned.
    expect(connect).toContain("http://127.0.0.1:*");
    expect(connect).toContain("ws://127.0.0.1:*");
  });

  it("allows a cross-origin telemetry ingest, as the desktop app's keyed one is", () => {
    // The packaged renderer posts to https://<ingest host>/k/<key>; only the
    // origin belongs in the directive, never the key in its path.
    const connect = directive(
      runtimePolicy({ ...PROD_CONFIG, OTEL_ENDPOINT: "https://ingest.example.dev/k/rbk_x" }),
      "connect-src",
    );

    expect(connect).toContain("https://ingest.example.dev");
    expect(connect).not.toContain("rbk_x");
  });

  it("allows no Sentry ingest: the web app no longer reports to Sentry", () => {
    const connect = directive(runtimePolicy(PROD_CONFIG), "connect-src");

    expect(connect).not.toContain("sentry.io");
  });

  it("narrows to 'self' and the fixed hosts when config.js did not load", () => {
    const connect = directive(runtimePolicy(undefined), "connect-src");

    expect(connect.startsWith("'self' ")).toBe(true);
    expect(connect).not.toContain("*.reliantapi.com");
    expect(connect).not.toMatch(/(^| )\*( |$)/);
    expect(connect).toContain("http://127.0.0.1:*");
  });

  it("tolerates an absent, relative or unparseable value instead of emitting junk", () => {
    // OTEL_ENDPOINT is normally the same-origin "/_otel", which 'self' covers.
    // An empty entry would produce "connect-src 'self'  https://…" or, worse,
    // a bare "undefined" token that invalidates the directive.
    const policy = runtimePolicy({
      API_URL: "not-a-url",
      SUPABASE_URL: "",
      OTEL_ENDPOINT: "/_otel",
      GATEWAY_URL: 42,
      APP_URL: "app://bundle",
    });

    expect(policy).not.toContain("undefined");
    expect(policy).not.toContain("null");
    expect(policy).not.toContain("  ");
    expect(policy).not.toContain("_otel");
  });
});

describe("buildCSP (the build-time policy)", () => {
  it("names no endpoint: the build does not know its environment", () => {
    const connect = directive(buildCSP([]), "connect-src");

    // The ceiling the runtime policy narrows; never a hostname.
    expect(connect).toBe("'self' * ws: wss:");
  });

  it("allows Stripe.js and the iframes it mounts", () => {
    const policy = buildCSP([]);

    expect(directive(policy, "script-src")).toContain("https://js.stripe.com");
    expect(directive(policy, "frame-src")).toContain("https://js.stripe.com");
    expect(directive(policy, "frame-src")).toContain("https://hooks.stripe.com");
  });

  it("allows the Monaco CDN as script, style and worker source", () => {
    const policy = buildCSP([]);

    expect(directive(policy, "script-src")).toContain("https://cdn.jsdelivr.net");
    expect(directive(policy, "style-src")).toContain("https://cdn.jsdelivr.net");
    // Monaco spawns its language workers from generated blob URLs.
    expect(directive(policy, "worker-src")).toContain("blob:");
  });

  it("keeps script-src strict: no unsafe-inline, no unsafe-eval", () => {
    // This is the whole point of the policy. Monaco's AMD loader probes
    // `new Function` in a try/catch and falls back to importScripts, so
    // unsafe-eval is NOT needed — do not add it to "fix" the editor.
    const scriptSrc = directive(buildCSP([]), "script-src");

    expect(scriptSrc).not.toContain("'unsafe-inline'");
    expect(scriptSrc).not.toContain("'unsafe-eval'");
  });

  it("denies plugins and pins base-uri", () => {
    const policy = buildCSP([]);

    expect(directive(policy, "object-src")).toBe("'none'");
    // Blocks an injected <base> from re-pointing every relative asset URL.
    expect(directive(policy, "base-uri")).toBe("'self'");
  });

  it("omits directives a meta tag cannot enforce", () => {
    // frame-ancestors and report-uri are ignored in a meta tag. Emitting them
    // would imply a protection that is not there; clickjacking is covered by
    // X-Frame-Options on the server, and app:// cannot be framed at all.
    const policy = buildCSP([]);

    expect(policy).not.toContain("frame-ancestors");
    expect(policy).not.toContain("report-uri");
  });
});

describe("withRuntimeConnectPolicy", () => {
  const CONFIG_TAG = '<script src="/config.js"></script>';

  it("puts the bootstrap right after config.js and authorises it by hash", () => {
    const html = withRuntimeConnectPolicy(
      `<head>${CONFIG_TAG}<script type="module" src="/assets/index.js"></script></head>`,
    );

    expect(html.indexOf(CONFIG_TAG)).toBeLessThan(html.indexOf(runtimeConnectScript()));
    expect(html.indexOf(runtimeConnectScript())).toBeLessThan(html.indexOf("/assets/index.js"));
    const policy = buildCSP(inlineScripts(html).map(sha256));
    expect(directive(policy, "script-src")).toContain(`'${sha256(runtimeConnectScript())}'`);
  });

  it("refuses a document that does not load config.js", () => {
    expect(() => withRuntimeConnectPolicy("<head></head>")).toThrow(/config\.js/);
  });

  it("accepts this app's index.html", () => {
    // %BASE_URL% is "/" by the time the post hook runs.
    const source = readFileSync(resolve(__dirname, "../../../index.html"), "utf8").replace(
      /%BASE_URL%/g,
      "/",
    );
    expect(() => withRuntimeConnectPolicy(source)).not.toThrow();
  });
});

describe("inline script hashing", () => {
  it("hashes the inline theme bootstrap so it runs without unsafe-inline", () => {
    const html = `<html><head><script>var a = 1;</script></head></html>`;
    const bodies = inlineScripts(html);

    expect(bodies).toEqual(["var a = 1;"]);

    const policy = buildCSP(bodies.map(sha256));
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
