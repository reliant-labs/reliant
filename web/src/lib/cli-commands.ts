/**
 * Builds copy-pasteable CLI commands with the correct environment variables
 * for the current deployment.
 *
 * In production the `reliant` CLI binary is built with the right server,
 * gateway, admin, and auth defaults compiled in (see internal/builddefaults
 * in the Go repo), so we return a bare command. In non-prod — local dev,
 * OSS self-hosted — the user needs explicit env-var overrides so the daemon
 * dials the same hosts the web frontend is using.
 */

// ---------------------------------------------------------------------------
// Environment detection
// ---------------------------------------------------------------------------

/** The API URL the web frontend was built against. */
function getServerURL(): string {
  return (
    import.meta.env.VITE_API_URL ||
    import.meta.env.VITE_GRPC_URL ||
    ""
  );
}

/**
 * The daemon-gateway URL, which is a DIFFERENT process from the api-server:
 * ToolsDaemonService (the daemon's bidi stream) is hosted only by the gateway
 * (internal/grpc/server.go), so a daemon pointed at the api-server gets a 404
 * on connect.
 *
 * Runtime wins over build time. Electron's preload publishes the gateway URL
 * BackendManager actually spawned its daemon with, which is authoritative and
 * tracks dynamically-allocated dev ports; VITE_GATEWAY_URL is a build-time
 * constant that can't. In the browser only the build-time value exists.
 */
function getGatewayURL(): string {
  if (typeof window !== "undefined" && window.RELIANT_CONFIG?.gatewayUrl) {
    return window.RELIANT_CONFIG.gatewayUrl;
  }
  return import.meta.env.VITE_GATEWAY_URL || "";
}

/**
 * True for hosts where the daemon CANNOT work out the gateway on its own.
 *
 * The daemon derives its gateway from the server URL when none is given. For a
 * cloud host that derivation is correct (api.example.com -> gateway.example.com),
 * but for loopback it deliberately declines to guess a port and returns the
 * server URL unchanged (deriveGatewayURL, cmd/reliant/commands/connection.go).
 * So on localhost an omitted gateway is not "the daemon will figure it out",
 * it is a command that always fails.
 */
function needsExplicitGateway(server: string): boolean {
  if (!server) return false;
  try {
    const { hostname } = new URL(server);
    return hostname === "localhost" || hostname === "127.0.0.1" || hostname === "::1";
  } catch {
    return false;
  }
}

/** Stand-in emitted when a gateway is required but not known at render time. */
export const GATEWAY_URL_PLACEHOLDER = "<your-daemon-gateway-url>";

/**
 * True when the daemon needs explicit env-var overrides to dial the same
 * backend the web frontend uses.
 *
 * VITE_CLI_DEFAULTS_BAKED IS THE ONLY INPUT, and it comes from exactly one
 * place: control-plane's deploy/kcl/lib/env.k (reliant_web_vite_env), which
 * sets it for prod and withholds it from every other environment. It means the
 * `reliant` binary shipped alongside this build was compiled with matching
 * server / gateway / auth defaults baked into internal/builddefaults via
 * -X ldflags, so the command needs no overrides.
 *
 * Everything without the flag — OSS self-hosted builds, a local dev stack —
 * is non-managed and gets explicit overrides so the daemon dials the right
 * hosts. No hosted hostname is hardcoded here.
 *
 * DO NOT ADD A SECOND CONDITION. This used to short-circuit on
 * `import.meta.env.DEV` before reading the flag, which made Vite's dev/prod
 * mode a competing authority over KCL's. `forge env up prod --target
 * reliant-web` serves the PROD KCL env through `npm run dev`, so that surface
 * rendered prod's hostnames and prod's publishable key behind five
 * RELIANT_*= overrides — for a binary that already knew all of them. Whether
 * the CLI is baked is a fact about the RELEASE, which only KCL knows; it is
 * not observable from the renderer's build mode.
 */
function isNonProd(): boolean {
  return import.meta.env.VITE_CLI_DEFAULTS_BAKED !== "true";
}

// ---------------------------------------------------------------------------
// Env-var prefix builder
// ---------------------------------------------------------------------------

/**
 * Returns a shell env-var prefix the user can paste verbatim in front of
 * `reliant daemon start`, e.g.:
 *   RELIANT_SERVER_URL=… RELIANT_GATEWAY_URL=…
 *
 * Returns "" when the CLI's defaults are baked (see isNonProd).
 *
 * THESE ARE THE ONLY TWO VARIABLES THE DAEMON READS:
 *
 *   RELIANT_SERVER_URL   ← VITE_API_URL || VITE_GRPC_URL
 *   RELIANT_GATEWAY_URL  ← RELIANT_CONFIG.gatewayUrl || VITE_GATEWAY_URL
 *
 * both via cmd/reliant/commands/connection.go, and they are the same two
 * electron/src/backend-manager.js puts in the daemon's environment when it
 * spawns one itself.
 *
 * Three variables used to be emitted here and are deliberately gone. Putting a
 * variable in a copy-pasteable command asserts that it matters; when it does
 * not, the reader has no way to tell, and it spreads:
 *
 *   RELIANT_API_BASE_URL  the managed-LLM proxy origin, read only by
 *                         ResolveReliantBaseURL — whose callers all sit in the
 *                         api-server and the temporal worker, never the
 *                         daemon. Cloud envs get it from KCL on those
 *                         workloads (deploy/kcl/lib/env.k).
 *   RELIANT_AUTH_URL      OAuth provider config, read by internal/auth/oauth.go
 *   RELIANT_AUTH_KEY      for the interactive login flow. `daemon start
 *                         --token` takes a PAT on stdin and never runs it.
 *
 * Only keys with a populated value are emitted.
 */
function envPrefix(): string {
  if (!isNonProd()) return "";

  const parts: string[] = [];

  const server = getServerURL();
  if (server) {
    parts.push(`RELIANT_SERVER_URL=${server}`);
  }

  // Emit the gateway whenever we know it, and emit a visible placeholder when
  // the daemon can't derive it. Silently dropping the line is the one option
  // that produces a copy-pasteable command that cannot work.
  const gateway = getGatewayURL();
  if (gateway) {
    parts.push(`RELIANT_GATEWAY_URL=${gateway}`);
  } else if (needsExplicitGateway(server)) {
    parts.push(`RELIANT_GATEWAY_URL=${GATEWAY_URL_PLACEHOLDER}`);
  }

  return parts.length > 0 ? parts.join(" ") + " " : "";
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/** `reliant daemon start --token` with env overrides when needed. */
export function daemonStartCommand(): string {
  return `${envPrefix()}reliant daemon start --token`;
}

/**
 * True when the rendered command still contains a placeholder the user must
 * replace by hand. The UI should say so rather than presenting the command as
 * ready to paste.
 */
export function daemonStartCommandNeedsEditing(): boolean {
  return daemonStartCommand().includes(GATEWAY_URL_PLACEHOLDER);
}

/**
 * `reliant auth serve` — the localhost OAuth bridge.
 *
 * The server itself contacts no backend: it binds a loopback port, opens the
 * browser, catches the redirect, and hands the code back to the tab that asked
 * (cmd/reliant/commands/auth_serve.go). The token exchange happens over the
 * BROWSER's authenticated connection, not here.
 *
 * ── Why it still carries an env prefix in dev ─────────────────────────
 *
 * The binary defaults to PRODUCTION (builddefaults.ServerURL =
 * https://api.reliantapi.com). A dev web app talking to a dev backend must say
 * so explicitly, and the only correct way to do that is on the command line —
 * which means the UI has to SHOW those variables rather than print a bare
 * command that silently resolves to prod.
 *
 * `RELIANT_WEB_ORIGIN` is specific to this command: the helper CORS-checks its
 * caller against an allowlist, and a dev server on a per-worktree port is not
 * in the built-in list. Without it the browser's request is rejected as
 * "origin not allowed".
 *
 * In a production build this returns the bare command, because the compiled
 * defaults are already correct.
 */
export function authServeCommand(): string {
  if (!isNonProd()) return "reliant auth serve";

  const parts: string[] = [];

  // The web app's own origin, so the helper's CORS allowlist accepts it. Read
  // from the live location rather than a build constant: the dev port is
  // allocated per worktree (.dev-ports.sh) and is not knowable at build time.
  if (typeof window !== "undefined" && window.location?.origin) {
    parts.push(`RELIANT_WEB_ORIGIN=${window.location.origin}`);
  }

  return parts.length > 0
    ? `${parts.join(" ")} reliant auth serve`
    : "reliant auth serve";
}

/**
 * Homebrew cask that installs the Reliant desktop app. The desktop app
 * ships the `reliant` CLI and installs it on PATH on first launch — there
 * is no separate CLI-only Homebrew formula.
 */
export const HOMEBREW_CASK_INSTALL =
  "brew install --cask reliant-labs/reliant/reliant";
