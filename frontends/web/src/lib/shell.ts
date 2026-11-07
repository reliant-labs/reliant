// Copyright (c) 2025 Reliant Labs

/**
 * Which app shell this page load mounts: Reliant, the forge-branded shell, or
 * the public forge demo.
 *
 * One bundle, three shells. The reliant-web static site serves
 * `app.reliantlabs.io` (Reliant), the forge host (`forge.reliantlabs.io`,
 * runtime config FORGE_APP_HOST) and the demo host (`demo.reliantlabs.io`,
 * DEMO_APP_HOST): the document's HOSTNAME picks the shell, so the forge pages
 * are the same bytes in all three and ship with the same release.
 *
 *   forge    ForgeShell (forgeShellRoutes.tsx): the forge console,
 *            observability, environments, secrets, domains, releases and
 *            activity, plus claim, sites, usage & spend and a small settings
 *            area. Control-plane pages only: no daemon, no onboarding, no
 *            chats. Branded "Forge by Reliant", telemetry service `forge-web`.
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
 * router is built, and a router cannot be swapped under a mounted app.
 */

import { loadConfig } from "./config_gen";

export type AppShell = "reliant" | "forge" | "demo";

const SHELLS: readonly AppShell[] = ["reliant", "forge", "demo"];
const isShell = (value: string | null): value is AppShell => (SHELLS as readonly (string | null)[]).includes(value);

/** The forge app host when the runtime config names none. */
export const DEFAULT_FORGE_APP_HOST = "forge.reliantlabs.io";

/**
 * The demo host when the runtime config names none.
 * Deprecated: demo.reliantlabs.io is served by its own build (demo.html, the
 * `demo` env's forge-demo frontend). This bundle still mounts the demo shell
 * on DEMO_APP_HOST, so binding the domain back here stays a rollback; removed
 * in the next major.
 */
export const DEFAULT_DEMO_APP_HOST = "demo.reliantlabs.io";

/** Reliant's hosted origin when the runtime config names none (constants.ts DEFAULT_APP_URL). */
const DEFAULT_RELIANT_APP_URL = "https://app.reliantlabs.io";

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

/** The public demo's hostname: DEMO_APP_HOST, else the default. Deprecated with DEMO_APP_HOST (see DEFAULT_DEMO_APP_HOST). */
export function demoAppHost(): string {
  let configured = "";
  try {
    configured = loadConfig().DEMO_APP_HOST;
  } catch {
    // An invalid config document is configGuard's to report; fall back.
  }
  return normalizeHost(configured) || DEFAULT_DEMO_APP_HOST;
}

/** The forge console's public origin, for Reliant's "Open Forge console" link. */
export function forgeAppOrigin(): string {
  return `https://${forgeAppHost()}`;
}

/** Reliant's public origin, for the forge shell's "Open in Reliant" link. */
export function reliantAppOrigin(): string {
  let configured = "";
  try {
    configured = loadConfig().APP_URL;
  } catch {
    // Fall back below.
  }
  const origin = configured.trim().replace(/\/$/, "");
  // On the forge host a config naming the forge host itself would make the
  // link loop back here; only another origin is a way to Reliant.
  if (origin && normalizeHost(origin) !== forgeAppHost()) return origin;
  return DEFAULT_RELIANT_APP_URL;
}

export interface ShellInputs {
  hostname: string;
  search: string;
  /** The forge app host (forgeAppHost()). */
  forgeHost: string;
  /** The demo host (demoAppHost()). */
  demoHost: string;
  /** True in the desktop app. */
  electron: boolean;
  storage: Pick<Storage, "getItem" | "setItem" | "removeItem"> | null;
}

/**
 * The shell for these inputs. Pure apart from the override it records in
 * `storage` (that is the point of the override: it outlives the query string,
 * which the router and sign-in redirects drop).
 */
export function resolveShell({ hostname, search, forgeHost, demoHost, electron, storage }: ShellInputs): AppShell {
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

  const host = normalizeHost(hostname);
  if (host === demoHost) return "demo";
  return host === forgeHost ? "forge" : "reliant";
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
    demoHost: demoAppHost(),
    electron: !!window.electronAPI || !!window.RELIANT_CONFIG?.isElectron,
    storage,
  });
  return resolved;
}

/**
 * True when this page load mounts a forge-branded shell: the forge shell or
 * the demo. Neither has a daemon, Reliant's projects or its onboarding, which
 * is what every caller means by it.
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
