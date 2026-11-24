// Copyright (c) 2025 Reliant Labs

/**
 * Which app shell this page load mounts: Reliant, the forge-branded shell, or
 * the public forge demo.
 *
 * The reliant-web static site serves `app.reliantlabs.io` (Reliant) and the
 * forge host (`forge.reliantlabs.io`, runtime config FORGE_APP_HOST): the
 * document's HOSTNAME picks the shell, so the forge pages are the same bytes
 * in both and ship with the same release. The public demo
 * (`demo.reliantlabs.io`) is a build of its own (demo.html, the forge-demo
 * frontend), which is always the demo shell (`__WEB_TARGET__`); this bundle
 * mounts the demo shell only through the dev override below.
 *
 *   forge    the app's one route tree (routes.tsx), as the forge BRAND:
 *            the same routes, guards and pages as Reliant (U6, one shell),
 *            branded "Forge by Reliant", telemetry service `forge-web`. What
 *            the brand changes is listed at the top of routes.tsx; ask
 *            isForgeShell() for it.
 *   demo     the public forge demo (demoShellRoutes.tsx): ONE route,
 *            `/forge/demo`, on sample data, read-only, the forge tour started
 *            on every visit. No sign-in, no account, no control-plane call.
 *            Telemetry service `reliant-forge-demo`.
 *   reliant  everything else, unchanged.
 *
 * A dev override exists so a shell can be looked at without DNS:
 * `?shell=forge`, `?shell=demo` (or `?shell=reliant`) on any page load picks a
 * shell and remembers it in localStorage; `?shell=` (empty) or `?shell=auto`
 * forgets it.
 * The desktop app is always Reliant: the forge shell is a website.
 *
 * The forge host needs DNS, TLS, CORS and auth-redirect configuration outside
 * this repo before it works: dev-docs/forge-shell-rollout.md has the order,
 * the post-deploy checks and the rollback.
 *
 * Resolved ONCE per page load (getShell memoises): the shell decides which
 * router is built (the demo's or the app's) and the app's brand, and neither
 * can be swapped under a mounted app.
 */

import { loadConfig } from "./config_gen";

export type AppShell = "reliant" | "forge" | "demo";

const SHELLS: readonly AppShell[] = ["reliant", "forge", "demo"];
const isShell = (value: string | null): value is AppShell => (SHELLS as readonly (string | null)[]).includes(value);

/** The forge app host when the runtime config names none. */
export const DEFAULT_FORGE_APP_HOST = "forge.reliantlabs.io";

/** The public demo's host: its own build (demo.html), where the app's /forge/demo hands off to. */
export const DEFAULT_DEMO_APP_HOST = "demo.reliantlabs.io";

/** localStorage key of the dev override. */
export const SHELL_OVERRIDE_KEY = "reliant.shell";

/** The search parameter of the dev override. */
export const SHELL_PARAM = "shell";

/**
 * A configured host, reduced to a bare lower-case hostname. Accepts
 * `forge.example.com`, `https://forge.example.com/` and `forge.example.com:443`.
 */
export function normalizeHost(value: string | undefined | null): string {
  const raw = (value ?? "").trim().toLowerCase();
  if (!raw) return "";
  try {
    return new URL(raw.includes("://") ? raw : `https://${raw}`).hostname;
  } catch {
    return "";
  }
}

/** The forge app's hostname: FORGE_APP_HOST, else the default. */
export function forgeAppHost(): string {
  let configured = "";
  try {
    configured = loadConfig().FORGE_APP_HOST;
  } catch {
    // An invalid config document is configGuard's to report; fall back.
  }
  return normalizeHost(configured) || DEFAULT_FORGE_APP_HOST;
}

/** The forge host's public origin, for a link into the app from outside it (the public demo's). */
export function forgeAppOrigin(): string {
  return `https://${forgeAppHost()}`;
}

/**
 * The public demo's origin, for a link to it from the app (the app's
 * /forge/demo leaves for it). The DEMO_APP_HOST config key that once
 * overrode the host is removed; the demo's own build lives on one host.
 */
export function demoAppOrigin(): string {
  return `https://${DEFAULT_DEMO_APP_HOST}`;
}

export interface ShellInputs {
  hostname: string;
  search: string;
  /** The forge app host (forgeAppHost()). */
  forgeHost: string;
  /** True in the desktop app. */
  electron: boolean;
  storage: Pick<Storage, "getItem" | "setItem" | "removeItem"> | null;
}

/**
 * The shell for these inputs. Pure apart from the override it records in
 * `storage` (that is the point of the override: it outlives the query string,
 * which the router and sign-in redirects drop).
 */
export function resolveShell({ hostname, search, forgeHost, electron, storage }: ShellInputs): AppShell {
  if (electron) return "reliant";

  let param: string | null = null;
  try {
    param = new URLSearchParams(search).get(SHELL_PARAM);
  } catch {
    param = null;
  }
  if (param !== null) {
    const value = param.trim().toLowerCase();
    try {
      if (isShell(value)) storage?.setItem(SHELL_OVERRIDE_KEY, value);
      else if (value === "" || value === "auto") storage?.removeItem(SHELL_OVERRIDE_KEY);
    } catch {
      // Storage refused: the override still applies to this load.
    }
    if (isShell(value)) return value;
  }

  let stored: string | null = null;
  try {
    stored = storage?.getItem(SHELL_OVERRIDE_KEY) ?? null;
  } catch {
    stored = null;
  }
  if (isShell(stored)) return stored;

  return normalizeHost(hostname) === forgeHost ? "forge" : "reliant";
}

let resolved: AppShell | null = null;

/** This page load's shell. */
export function getShell(): AppShell {
  if (resolved) return resolved;
  // The forge demo's own bundle (demo.html, vite.config.ts) is the demo shell
  // on every host. The app's build defines "app", so this folds away there.
  if (__WEB_TARGET__ === "demo") return (resolved = "demo");
  if (typeof window === "undefined") return "reliant";
  let storage: Storage | null = null;
  try {
    storage = window.localStorage;
  } catch {
    storage = null;
  }
  resolved = resolveShell({
    hostname: window.location.hostname,
    search: window.location.search,
    forgeHost: forgeAppHost(),
    electron: !!window.electronAPI || !!window.RELIANT_CONFIG?.isElectron,
    storage,
  });
  return resolved;
}

/**
 * True when this page load is forge-branded: the forge host (the app's route
 * tree as the forge brand) or the demo. The brand check: the brand mark, the
 * tour's shell, the landing journey, the home of a person who has not
 * onboarded. Since the forge host mounts the one route tree (U6) it HAS
 * daemons, projects and onboarding, so a caller that used this to mean "no
 * daemon here" is a dead branch on that host (U6 removes them).
 */
export function isForgeShell(): boolean {
  return getShell() !== "reliant";
}

/** True on the public demo host: one route, sample data, the tour on every load. */
export function isDemoShell(): boolean {
  return getShell() === "demo";
}

/** Test seam: forget the memoised shell (and optionally pin one). */
export function resetShellForTests(shell: AppShell | null = null): void {
  resolved = shell;
}
