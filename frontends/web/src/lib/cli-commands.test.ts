import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { setRuntimeConfig } from '@/test/runtimeConfig';

/**
 * The regression these cover: the generated daemon start command used to omit
 * RELIANT_GATEWAY_URL whenever GATEWAY_URL was unset — which was always,
 * in local dev, since nothing set it. The daemon then derived its gateway from
 * RELIANT_SERVER_URL, and because deriveGatewayURL leaves loopback hosts alone
 * it dialed the api-server for ToolsDaemonService (gateway-only) and died on a
 * 404. The command was copy-pasteable and could never work.
 */

// Re-import per test so module-level reads are re-evaluated.
async function load() {
  vi.resetModules();
  return import('./cli-commands');
}

const ORIGINAL_ENV = { ...import.meta.env };

/**
 * Set this test's inputs. DEV is Vite's build-mode flag (import.meta.env);
 * every other key is a runtime config field (config.js). undefined unsets.
 */
function setEnv(vars: Record<string, string | boolean | undefined>) {
  const doc = { ...(window.__FORGE_CONFIG__ as Record<string, string>) };
  for (const [k, v] of Object.entries(vars)) {
    if (k === 'DEV') {
      if (v === undefined) delete (import.meta.env as Record<string, unknown>)[k];
      else (import.meta.env as Record<string, unknown>)[k] = v;
    } else if (v === undefined) {
      delete doc[k];
    } else {
      doc[k] = String(v);
    }
  }
  setRuntimeConfig(doc);
}

beforeEach(() => {
  setRuntimeConfig({ API_URL: 'http://localhost:3091' });
});

afterEach(() => {
  for (const k of Object.keys(import.meta.env)) {
    delete (import.meta.env as Record<string, unknown>)[k];
  }
  Object.assign(import.meta.env, ORIGINAL_ENV);
  // Test cleanup of the Electron-injected global.
  delete globalThis.window?.RELIANT_CONFIG;
});

describe('daemonStartCommand gateway handling', () => {
  it('emits GATEWAY_URL when the build provides one', async () => {
    setEnv({ GATEWAY_URL: 'http://localhost:29190' });
    const { daemonStartCommand } = await load();
    expect(daemonStartCommand()).toContain(
      'RELIANT_GATEWAY_URL=http://localhost:29190'
    );
  });

  it('prefers the Electron runtime gateway over the build-time value', async () => {
    setEnv({ GATEWAY_URL: 'http://localhost:29190' });
    vi.stubGlobal('window', {
      RELIANT_CONFIG: { gatewayUrl: 'http://localhost:39190' },
    });
    const { daemonStartCommand } = await load();
    // The runtime value tracks dynamically-allocated dev ports; the build-time
    // constant cannot.
    expect(daemonStartCommand()).toContain(
      'RELIANT_GATEWAY_URL=http://localhost:39190'
    );
    expect(daemonStartCommand()).not.toContain('29190');
    vi.unstubAllGlobals();
  });

  it('emits a visible placeholder rather than silently dropping the gateway on localhost', async () => {
    const { daemonStartCommand, GATEWAY_URL_PLACEHOLDER } = await load();
    const cmd = daemonStartCommand();
    expect(cmd).toContain(`RELIANT_GATEWAY_URL=${GATEWAY_URL_PLACEHOLDER}`);
  });

  it('flags a placeholder-bearing command as needing an edit', async () => {
    const { daemonStartCommandNeedsEditing } = await load();
    expect(daemonStartCommandNeedsEditing()).toBe(true);
  });

  it('does not flag a command once the gateway is known', async () => {
    setEnv({ GATEWAY_URL: 'http://localhost:29190' });
    const { daemonStartCommandNeedsEditing } = await load();
    expect(daemonStartCommandNeedsEditing()).toBe(false);
  });

  it('omits the gateway for cloud hosts, where the daemon derives it correctly', async () => {
    // api.example.com -> gateway.example.com is a derivation the daemon makes
    // on its own; forcing a placeholder there would be noise.
    setEnv({ API_URL: 'https://api.example.com' });
    const { daemonStartCommand } = await load();
    expect(daemonStartCommand()).not.toContain('RELIANT_GATEWAY_URL');
  });

  it('never points the gateway at the api-server on localhost', async () => {
    // The original bug, stated as an invariant.
    const { daemonStartCommand } = await load();
    expect(daemonStartCommand()).not.toContain(
      'RELIANT_GATEWAY_URL=http://localhost:3091'
    );
  });
});

describe('production renders a bare command', () => {
  // The released binary carries the hosted server/gateway/admin/auth defaults
  // compiled in (release.yml stamps them into pkg/config.BuildDefaults via -X),
  // so prod must print `reliant auth serve` with NO env prefix.
  //
  // This shipped broken: isNonProd() keys off CLI_DEFAULTS_BAKED and
  // NOTHING set it — not the release workflow, not the KCL frontend env — so
  // production always took the non-prod branch and told users to paste five
  // RELIANT_*= overrides, including the Supabase anon key, for a binary that
  // already knew every one of them.
  it('emits no env prefix when the CLI defaults are baked', async () => {
    setEnv({
      DEV: undefined,
      CLI_DEFAULTS_BAKED: 'true',
      API_URL: 'https://api.reliantapi.com',
      GATEWAY_URL: 'https://gateway.reliantapi.com',
      CONTROL_PLANE_API_URL: 'https://admin.reliantapi.com',
      SUPABASE_URL: 'https://dash.reliantlabs.io',
      SUPABASE_ANON_KEY: 'sb_publishable_example',
    });
    const { authServeCommand, daemonStartCommand } = await load();
    expect(authServeCommand()).toBe('reliant auth serve');
    expect(daemonStartCommand()).not.toContain('RELIANT_');
  });

  it('never leaks the auth key into a copy-pasteable prod command', async () => {
    setEnv({
      DEV: undefined,
      CLI_DEFAULTS_BAKED: 'true',
      SUPABASE_ANON_KEY: 'sb_publishable_secret_looking_value',
    });
    const { authServeCommand } = await load();
    expect(authServeCommand()).not.toContain('sb_publishable');
  });

  it('still emits overrides for a non-prod build', async () => {
    // dev and OSS self-hosted CLI builds carry no injected defaults, so their
    // users genuinely need the prefix — the flag is absent there.
    setEnv({
      CLI_DEFAULTS_BAKED: undefined,
      API_URL: 'https://api.self-hosted.example.com',
    });
    const { daemonStartCommand } = await load();
    expect(daemonStartCommand()).toContain('RELIANT_SERVER_URL=');
  });

  it('honours the baked flag even when Vite reports DEV', async () => {
    // `forge env up prod --target reliant-web` serves the PROD KCL env
    // (control-plane deploy/kcl/prod/main.k -> reliant_web_vite_env("prod"))
    // through `npm run dev`, so import.meta.env.DEV is true while every
    // runtime config value is prod's. isNonProd() used to check DEV FIRST and return
    // early, so that surface printed the prod hostnames AND the prod
    // publishable key behind five RELIANT_*= overrides.
    //
    // KCL is the single authority on whether a build's CLI is baked: it sets
    // the flag for prod and withholds it everywhere else. A second, local
    // authority that can contradict it is the bug.
    setEnv({
      DEV: true,
      CLI_DEFAULTS_BAKED: 'true',
      API_URL: 'https://api.reliantapi.com',
      GATEWAY_URL: 'https://gateway.reliantapi.com',
      SUPABASE_ANON_KEY: 'sb_publishable_KKiB3B0EdEv7nguwKfEE5A_iY9rVXod',
    });
    const { daemonStartCommand } = await load();
    expect(daemonStartCommand()).toBe('reliant daemon start');
    expect(daemonStartCommand()).not.toContain('sb_publishable');
  });

  it('still emits overrides in dev when KCL withheld the flag', async () => {
    // The other half of the same rule: a genuine local dev stack gets no flag
    // from KCL, so it must keep its overrides. Removing the DEV check must not
    // silently bare-render a command for an unbaked binary.
    setEnv({
      DEV: true,
      CLI_DEFAULTS_BAKED: undefined,
      API_URL: 'http://localhost:3091',
      GATEWAY_URL: 'http://localhost:29190',
    });
    const { daemonStartCommand } = await load();
    expect(daemonStartCommand()).toContain('RELIANT_SERVER_URL=http://localhost:3091');
    expect(daemonStartCommand()).toContain('RELIANT_GATEWAY_URL=http://localhost:29190');
  });
});

describe('only variables the target binary actually reads are emitted', () => {
  // A copy-pasteable command must not carry a variable its binary ignores:
  // the reader reasonably concludes it matters, and it becomes cargo cult the
  // moment someone pastes it somewhere else.
  it('never emits RELIANT_API_BASE_URL for daemon start', async () => {
    // RELIANT_API_BASE_URL is read ONLY by ResolveReliantBaseURL, whose sole
    // caller chain (BuildAvailableDrivers -> catalog.ListModels /
    // model_selection / llm_request / compact, confirmed with gopls
    // call_hierarchy) lives in the api-server and the temporal worker. The
    // daemon never reads it — electron/src/backend-manager.js passes only
    // RELIANT_SERVER_URL and RELIANT_GATEWAY_URL into the spawned daemon.
    // In cloud envs control-plane's KCL sets it on those SERVER workloads
    // (deploy/kcl/lib/env.k), which is where it belongs.
    setEnv({
      CLI_DEFAULTS_BAKED: undefined,
      CONTROL_PLANE_API_URL: 'https://admin.reliantapi.com',
    });
    const { daemonStartCommand } = await load();
    expect(daemonStartCommand()).not.toContain('RELIANT_API_BASE_URL');
  });

  it('never puts backend or secret vars on auth serve', async () => {
    // The helper contacts NO backend: it starts a localhost HTTP server and
    // hands the authorize-URL template it is POSTed to oauthcallback.Run.
    // Token exchange happens over the browser's authenticated connection. So
    // server/gateway URLs are meaningless here, and the publishable key must
    // never be pasted at a shell.
    setEnv({
      CLI_DEFAULTS_BAKED: undefined,
      API_URL: 'http://localhost:3091',
      GATEWAY_URL: 'http://localhost:29190',
      SUPABASE_URL: 'https://dash.reliantlabs.io',
      SUPABASE_ANON_KEY: 'sb_publishable_example',
    });
    const { authServeCommand } = await load();
    const cmd = authServeCommand();

    expect(cmd).toContain('reliant auth serve');
    for (const forbidden of [
      'RELIANT_SERVER_URL',
      'RELIANT_GATEWAY_URL',
      'RELIANT_API_BASE_URL',
      'RELIANT_AUTH_KEY',
      'sb_publishable_example',
    ]) {
      expect(cmd).not.toContain(forbidden);
    }
  });

  // The helper CORS-checks its caller against an allowlist that cannot know a
  // per-worktree dev port (the dev stack allocates it at runtime). Without
  // RELIANT_WEB_ORIGIN the browser's request is refused as "origin not
  // allowed" — so a dev command that omits it is one the user cannot use.
  it('passes the web origin so the dev server is CORS-allowed', async () => {
    setEnv({ CLI_DEFAULTS_BAKED: undefined });
    const { authServeCommand } = await load();
    expect(authServeCommand()).toContain(
      `RELIANT_WEB_ORIGIN=${window.location.origin}`,
    );
  });

  it('renders bare in a production build', async () => {
    setEnv({ CLI_DEFAULTS_BAKED: 'true' });
    const { authServeCommand } = await load();
    expect(authServeCommand()).toBe('reliant auth serve');
  });
});
