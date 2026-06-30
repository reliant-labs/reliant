/**
 * Builds copy-pasteable CLI commands with the correct environment variables
 * for the current deployment.
 *
 * In production the `reliant` CLI binary is built with the right server,
 * gateway, admin, and auth defaults compiled in (see internal/builddefaults
 * in the Go repo), so we return a bare command. In non-prod — staging,
 * preprod, local dev, OSS self-hosted — the user needs explicit env-var
 * overrides so the daemon dials the same hosts the web frontend is using.
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
 * True when the daemon needs explicit env-var overrides to dial the same
 * backend the web frontend uses.
 *
 * A build is treated as "managed" (CLI defaults already correct, emit a bare
 * command) ONLY when the deploy pipeline opts in by setting
 * VITE_CLI_DEFAULTS_BAKED=true at build time. That flag means the `reliant`
 * CLI binary shipped alongside this build was compiled with matching server /
 * gateway / admin / auth defaults.
 *
 * Everything else — `vite dev`, OSS self-hosted builds, any build without the
 * opt-in flag — is non-managed: the user gets explicit env overrides so the
 * daemon dials the right hosts. This is the neutral/self-host default; no
 * hosted hostname is hardcoded here.
 */
function isNonProd(): boolean {
  if (import.meta.env.DEV) return true;
  if (import.meta.env.VITE_CLI_DEFAULTS_BAKED === "true") return false;
  return true;
}

// ---------------------------------------------------------------------------
// Env-var prefix builder
// ---------------------------------------------------------------------------

/**
 * Returns a shell env-var prefix string the user can paste verbatim, e.g.:
 *   RELIANT_SERVER_URL=… RELIANT_GATEWAY_URL=… RELIANT_API_BASE_URL=… \
 *   RELIANT_AUTH_URL=… RELIANT_AUTH_KEY=…
 *
 * Returns "" in production (CLI defaults already correct).
 *
 * Mapping of daemon env vars → Vite-injected build vars:
 *
 *   RELIANT_SERVER_URL    ← VITE_API_URL || VITE_GRPC_URL  (root.go:36)
 *   RELIANT_GATEWAY_URL   ← VITE_GATEWAY_URL               (root.go:37, falls back to server URL when unset)
 *   RELIANT_API_BASE_URL  ← VITE_CONTROL_PLANE_API_URL     (admin-server LLM proxy; internal/llm/drivers/reliant_base_url.go)
 *   RELIANT_AUTH_URL      ← VITE_SUPABASE_URL              (internal/auth/oauth.go:27)
 *   RELIANT_AUTH_KEY      ← VITE_SUPABASE_ANON_KEY         (oauth.go:31)
 *
 * Only keys with a populated value are emitted. Order matches the daemon's
 * own config-loading order for readability.
 */
function envPrefix(): string {
  if (!isNonProd()) return "";

  const parts: string[] = [];

  const server = getServerURL();
  if (server) {
    parts.push(`RELIANT_SERVER_URL=${server}`);
  }

  const gateway = import.meta.env.VITE_GATEWAY_URL;
  if (gateway) {
    parts.push(`RELIANT_GATEWAY_URL=${gateway}`);
  }

  const adminURL = import.meta.env.VITE_CONTROL_PLANE_API_URL;
  if (adminURL) {
    parts.push(`RELIANT_API_BASE_URL=${adminURL}`);
  }

  const authURL = import.meta.env.VITE_SUPABASE_URL;
  if (authURL) {
    parts.push(`RELIANT_AUTH_URL=${authURL}`);
  }

  const authKey = import.meta.env.VITE_SUPABASE_ANON_KEY;
  if (authKey) {
    parts.push(`RELIANT_AUTH_KEY=${authKey}`);
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

/** `reliant auth serve` with env overrides when needed. */
export function authServeCommand(): string {
  return `${envPrefix()}reliant auth serve`;
}

/**
 * Homebrew cask that installs the Reliant desktop app. The desktop app
 * ships the `reliant` CLI and installs it on PATH on first launch — there
 * is no separate CLI-only Homebrew formula.
 */
export const HOMEBREW_CASK_INSTALL =
  "brew install --cask reliant-labs/reliant/reliant";
