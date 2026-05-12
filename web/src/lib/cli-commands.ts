/**
 * Builds copy-pasteable CLI commands with the correct environment variables
 * for the current deployment.
 *
 * In production the CLI defaults are correct, so we return bare commands.
 * In non-prod (staging, local dev, etc.) the user needs env-var overrides
 * so the CLI talks to the right server, auth provider, and gateway.
 */

// ---------------------------------------------------------------------------
// Environment detection
// ---------------------------------------------------------------------------

/** The API URL the web frontend is currently talking to. */
function getServerURL(): string {
  return (
    import.meta.env.VITE_API_URL ||
    import.meta.env.VITE_GRPC_URL ||
    ""
  );
}

/**
 * True when the web app is pointing at a non-production backend.
 *
 * We consider it "production" when:
 *  - No VITE_API_URL / VITE_GRPC_URL is set (the CLI will fall back to its
 *    compiled-in production default), OR
 *  - The URL contains "reliantapi.com" (the production domain).
 */
function isNonProd(): boolean {
  const url = getServerURL();
  if (!url) return false; // no override → production defaults
  return !url.includes("reliantapi.com");
}

// ---------------------------------------------------------------------------
// Env-var prefix builder
// ---------------------------------------------------------------------------

/**
 * Returns a shell env-var prefix string like:
 *   RELIANT_SERVER_URL=https://… RELIANT_AUTH_URL=https://… RELIANT_AUTH_KEY=… 
 *
 * Empty string in production (no prefix needed).
 */
function envPrefix(): string {
  if (!isNonProd()) return "";

  const parts: string[] = [];

  const server = getServerURL();
  if (server) {
    parts.push(`RELIANT_SERVER_URL=${server}`);
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

/** Homebrew formula that installs the CLI binary (not the Electron cask). */
export const HOMEBREW_CLI_INSTALL = "brew install reliant-labs/reliant/reliant";

/** Homebrew cask that installs the Electron desktop app. */
export const HOMEBREW_CASK_INSTALL =
  "brew install --cask reliant-labs/reliant/reliant";
