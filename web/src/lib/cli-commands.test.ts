import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

/**
 * The regression these cover: the generated daemon start command used to omit
 * RELIANT_GATEWAY_URL whenever VITE_GATEWAY_URL was unset — which was always,
 * in local dev, since nothing set it. The daemon then derived its gateway from
 * RELIANT_SERVER_URL, and because deriveGatewayURL leaves loopback hosts alone
 * it dialed the api-server for ToolsDaemonService (gateway-only) and died on a
 * 404. The command was copy-pasteable and could never work.
 */

// Re-import per test so module-level env reads are re-evaluated.
async function load() {
  vi.resetModules();
  return import('./cli-commands');
}

const ORIGINAL_ENV = { ...import.meta.env };

function setEnv(vars: Record<string, string | undefined>) {
  for (const [k, v] of Object.entries(vars)) {
    if (v === undefined) {
      delete (import.meta.env as Record<string, unknown>)[k];
    } else {
      (import.meta.env as Record<string, unknown>)[k] = v;
    }
  }
}

beforeEach(() => {
  setEnv({
    VITE_API_URL: 'http://localhost:3091',
    VITE_GATEWAY_URL: undefined,
    VITE_CONTROL_PLANE_API_URL: undefined,
    VITE_SUPABASE_URL: undefined,
    VITE_SUPABASE_ANON_KEY: undefined,
  });
});

afterEach(() => {
  for (const k of Object.keys(import.meta.env)) {
    delete (import.meta.env as Record<string, unknown>)[k];
  }
  Object.assign(import.meta.env, ORIGINAL_ENV);
  // @ts-expect-error test cleanup of the Electron-injected global
  delete globalThis.window?.RELIANT_CONFIG;
});

describe('daemonStartCommand gateway handling', () => {
  it('emits VITE_GATEWAY_URL when the build provides one', async () => {
    setEnv({ VITE_GATEWAY_URL: 'http://localhost:29190' });
    const { daemonStartCommand } = await load();
    expect(daemonStartCommand()).toContain(
      'RELIANT_GATEWAY_URL=http://localhost:29190'
    );
  });

  it('prefers the Electron runtime gateway over the build-time value', async () => {
    setEnv({ VITE_GATEWAY_URL: 'http://localhost:29190' });
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
    setEnv({ VITE_GATEWAY_URL: 'http://localhost:29190' });
    const { daemonStartCommandNeedsEditing } = await load();
    expect(daemonStartCommandNeedsEditing()).toBe(false);
  });

  it('omits the gateway for cloud hosts, where the daemon derives it correctly', async () => {
    // api.example.com -> gateway.example.com is a derivation the daemon makes
    // on its own; forcing a placeholder there would be noise.
    setEnv({ VITE_API_URL: 'https://api.example.com' });
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
  // compiled in (release.yml injects them into internal/builddefaults via -X),
  // so prod must print `reliant auth serve` with NO env prefix.
  //
  // This shipped broken: isNonProd() keys off VITE_CLI_DEFAULTS_BAKED and
  // NOTHING set it — not the release workflow, not the KCL frontend env — so
  // production always took the non-prod branch and told users to paste five
  // RELIANT_*= overrides, including the Supabase anon key, for a binary that
  // already knew every one of them.
  it('emits no env prefix when the CLI defaults are baked', async () => {
    setEnv({
      DEV: undefined,
      VITE_CLI_DEFAULTS_BAKED: 'true',
      VITE_API_URL: 'https://api.reliantapi.com',
      VITE_GATEWAY_URL: 'https://gateway.reliantapi.com',
      VITE_CONTROL_PLANE_API_URL: 'https://admin.reliantapi.com',
      VITE_SUPABASE_URL: 'https://dash.reliantlabs.io',
      VITE_SUPABASE_ANON_KEY: 'sb_publishable_example',
    });
    const { authServeCommand, daemonStartCommand } = await load();
    expect(authServeCommand()).toBe('reliant auth serve');
    expect(daemonStartCommand()).not.toContain('RELIANT_');
  });

  it('never leaks the auth key into a copy-pasteable prod command', async () => {
    setEnv({
      DEV: undefined,
      VITE_CLI_DEFAULTS_BAKED: 'true',
      VITE_SUPABASE_ANON_KEY: 'sb_publishable_secret_looking_value',
    });
    const { authServeCommand } = await load();
    expect(authServeCommand()).not.toContain('sb_publishable');
  });

  it('still emits overrides for a non-prod build', async () => {
    // staging/preprod/dev CLI builds carry no injected defaults, so their
    // users genuinely need the prefix — the flag is absent there.
    setEnv({
      VITE_CLI_DEFAULTS_BAKED: undefined,
      VITE_API_URL: 'https://preprod.reliantapi.com',
    });
    const { authServeCommand } = await load();
    expect(authServeCommand()).toContain('RELIANT_SERVER_URL=');
  });
});
