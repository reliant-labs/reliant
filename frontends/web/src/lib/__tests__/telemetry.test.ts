import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setRuntimeConfig } from "@/test/runtimeConfig";

// The option matrix is pinned through the real initTelemetry(): what reaches
// the runtime's initBrowserTelemetry is the privacy boundary, so the test
// drives it rather than a helper in isolation.

const privacy = vi.hoisted(() => {
  const listeners = new Set<(s: { crashReportingEnabled: boolean }) => void>();
  const state = { crashReportingEnabled: true, analyticsEnabled: true };
  // The consent write in flight: privacyWritesSettled() resolves with it.
  const write = { settled: Promise.resolve() };
  return {
    state,
    listeners,
    write,
    subscribe: (fn: (s: { crashReportingEnabled: boolean }) => void) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    set(next: Partial<typeof state>) {
      Object.assign(state, next);
      listeners.forEach((fn) => fn(state));
    },
  };
});

const runtime = vi.hoisted(() => ({
  initBrowserTelemetry: vi.fn(async () => true),
  enableAttribution: vi.fn(),
  disableTelemetry: vi.fn(),
  setTelemetryUser: vi.fn(),
  reportException: vi.fn(() => true),
  setGlobalAttributes: vi.fn(),
}));

vi.mock("@reliantlabs/forge-web-runtime", () => ({
  disableTelemetry: runtime.disableTelemetry,
  setTelemetryUser: runtime.setTelemetryUser,
  reportException: runtime.reportException,
}));
vi.mock("@reliantlabs/forge-web-runtime/telemetry", () => ({
  initBrowserTelemetry: runtime.initBrowserTelemetry,
  enableAttribution: runtime.enableAttribution,
}));
vi.mock("@hyperdx/browser", () => ({
  default: { setGlobalAttributes: runtime.setGlobalAttributes },
}));
vi.mock("../../hooks/privacy-queries", () => ({
  privacyConsent: () => privacy.state,
  privacyWritesSettled: () => privacy.write.settled,
  subscribePrivacyConsent: privacy.subscribe,
}));
vi.mock("../constants", () => ({ isDev: false }));
vi.mock("../appBuild", () => ({
  appBuild: () => ({ version: "1.8.1", commit: "c", ref: "r", built: "t" }),
}));

import { holdLandingAttribution, LANDING_ATTRIBUTION_KEY } from "../landingAttribution";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { demoAppHost, forgeAppHost, resetShellForTests, resolveShell } from "../shell";
import {
  buildTelemetryConfig,
  DEMO_SHELL_SERVICE_NAME,
  demoReplayRate,
  initTelemetry,
  replaySampleRate,
  reportErrorToTelemetry,
  resetTelemetryForTests,
  setReloadForTests,
  setTelemetryUserId,
} from "../telemetry";

const ENDPOINT = "https://ingest.example.dev/k/rbk_x";

type Config = {
  serviceName: string;
  frontend?: string;
  serviceVersion?: string;
  environment?: string;
  endpoint?: string;
  apiOrigins?: Array<string | undefined>;
  consoleCapture?: boolean;
  advancedNetworkCapture?: boolean;
  replay?: boolean;
  replaySampleRate?: number;
  appClient?: string;
};

function lastConfig(): Config {
  const calls = runtime.initBrowserTelemetry.mock.calls as unknown as Config[][];
  return calls[calls.length - 1][0];
}

/** Layer values over this test's runtime config document (config.js). */
function stubConfig(values: Record<string, string>) {
  setRuntimeConfig({ ...(window.__FORGE_CONFIG__ as Record<string, string>), ...values });
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.unstubAllEnvs();
  resetTelemetryForTests();
  setReloadForTests(vi.fn());
  privacy.listeners.clear();
  privacy.write.settled = Promise.resolve();
  privacy.state.crashReportingEnabled = true;
  privacy.state.analyticsEnabled = true;
  localStorage.clear();
  sessionStorage.clear();
  delete (window as { electronAPI?: unknown }).electronAPI;
  setRuntimeConfig({ OTEL_ENDPOINT: ENDPOINT });
});

afterEach(() => {
  vi.unstubAllEnvs();
  resetShellForTests();
});

describe("initTelemetry: when it starts", () => {
  it("reaches the runtime with a frontend name, never an anonymous page", async () => {
    await initTelemetry();
    expect(lastConfig().frontend).toBe("reliant-web");
  });

  it("does not initialise when crash-reporting consent is off", async () => {
    privacy.state.crashReportingEnabled = false;
    expect(await initTelemetry()).toBe(false);
    expect(runtime.initBrowserTelemetry).not.toHaveBeenCalled();
  });

  it("does not initialise when the endpoint is `off`", async () => {
    stubConfig({ OTEL_ENDPOINT: "off" });
    expect(await initTelemetry()).toBe(false);
    expect(runtime.initBrowserTelemetry).not.toHaveBeenCalled();
  });

  it("does not initialise without an endpoint, so merging changes nothing by itself", async () => {
    stubConfig({ OTEL_ENDPOINT: "" });
    expect(await initTelemetry()).toBe(false);
    expect(runtime.initBrowserTelemetry).not.toHaveBeenCalled();
  });

  it("initialises once however often it is called", async () => {
    await Promise.all([initTelemetry(), initTelemetry()]);
    await initTelemetry();
    expect(runtime.initBrowserTelemetry).toHaveBeenCalledTimes(1);
  });

  it("is a no-op when the runtime reports it is not running", async () => {
    runtime.initBrowserTelemetry.mockResolvedValueOnce(false);
    expect(await initTelemetry()).toBe(false);
    expect(runtime.setGlobalAttributes).not.toHaveBeenCalled();
  });

  it("never throws when the runtime does", async () => {
    runtime.initBrowserTelemetry.mockRejectedValueOnce(new Error("blocked"));
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    await expect(initTelemetry()).resolves.toBe(false);
    warn.mockRestore();
  });
});

describe("initTelemetry: the options it passes", () => {
  it("never captures console output, which can hold chat content", async () => {
    await initTelemetry();
    expect(lastConfig().consoleCapture).toBe(false);
  });

  it("never captures request or response bodies", async () => {
    await initTelemetry();
    expect(lastConfig().advancedNetworkCapture).toBe(false);
  });

  it("names the service and passes the keyed endpoint through", async () => {
    await initTelemetry();
    expect(lastConfig()).toMatchObject({
      serviceName: "reliant-web",
      endpoint: ENDPOINT,
      environment: "prod",
    });
  });

  it("uses VITE_APP_VERSION as service.version, else this bundle's build", async () => {
    await initTelemetry();
    expect(lastConfig().serviceVersion).toBe("1.8.1");

    resetTelemetryForTests();
    vi.stubEnv("VITE_APP_VERSION", "build-42");
    await initTelemetry();
    expect(lastConfig().serviceVersion).toBe("build-42");
  });

  it("lists the app's backends so their requests carry a traceparent", async () => {
    stubConfig({
      API_URL: "https://api.example.dev",
      CONTROL_PLANE_API_URL: "https://cp.example.dev",
      GATEWAY_URL: "https://gw.example.dev",
    });
    await initTelemetry();
    expect(lastConfig().apiOrigins).toEqual(
      expect.arrayContaining([
        "https://api.example.dev",
        "https://cp.example.dev",
        "https://gw.example.dev",
      ]),
    );
  });

  it("marks a prerelease desktop build 'prerelease' and records it in full", async () => {
    (window as { electronAPI?: unknown }).electronAPI = {
      getVersion: async () => "1.9.0-rc.2",
    };
    stubConfig({ OTEL_REPLAY: "true", OTEL_REPLAY_SAMPLE_POLICY: "new-user-funnel" });
    await initTelemetry();
    expect(lastConfig()).toMatchObject({ environment: "prerelease", replaySampleRate: 1 });
  });

  it("names app.client=web on a browser and stamps only reliant.build itself", async () => {
    await initTelemetry();
    expect(lastConfig().appClient).toBe("web");
    // The runtime stamps app.client from appClient; the app adds the build.
    expect(runtime.setGlobalAttributes).toHaveBeenCalledWith({
      "reliant.build": "1.8.1",
    });
  });

  it("names app.client=electron in the Electron renderer", async () => {
    (window as { electronAPI?: unknown }).electronAPI = {
      getVersion: async () => "1.8.1",
    };
    await initTelemetry();
    expect(lastConfig().appClient).toBe("electron");
  });
});

describe("replay decision", () => {
  it("keeps replay off unless OTEL_REPLAY is 'true'", async () => {
    await initTelemetry();
    expect(lastConfig().replay).toBe(false);

    resetTelemetryForTests();
    stubConfig({ OTEL_REPLAY: "true" });
    await initTelemetry();
    expect(lastConfig().replay).toBe(true);
  });

  it("records a new user's sessions in full under the new-user-funnel policy", async () => {
    stubConfig({ OTEL_REPLAY: "true", OTEL_REPLAY_SAMPLE_POLICY: "new-user-funnel" });
    await initTelemetry();
    expect(lastConfig().replaySampleRate).toBe(1);
  });

  it("falls to 10% without the policy", async () => {
    stubConfig({ OTEL_REPLAY: "true" });
    await initTelemetry();
    expect(lastConfig().replaySampleRate).toBe(0.1);
  });
});

describe("replaySampleRate", () => {
  const now = 1_000_000_000_000;
  const base = { prerelease: false, policy: "new-user-funnel", now };
  const store = (initial: Record<string, string> = {}) => {
    const data = { ...initial };
    return {
      getItem: (k: string) => data[k] ?? null,
      setItem: (k: string, v: string) => {
        data[k] = v;
      },
      data,
    };
  };

  it("records everything on a prerelease", () => {
    expect(
      replaySampleRate({ ...base, prerelease: true, local: store(), session: store() }),
    ).toBe(1);
  });

  it("lets a numeric override win over the policy", () => {
    expect(
      replaySampleRate({ ...base, rateOverride: "0.25", local: store(), session: store() }),
    ).toBe(0.25);
  });

  it("records the first 5 sessions in full, then 10% once the window has passed", () => {
    const local = store();
    for (let i = 1; i <= 5; i++) {
      // A fresh tab each time: its own sessionStorage.
      expect(
        replaySampleRate({ ...base, now: now + 3_600_000 * i, local, session: store() }),
      ).toBe(1);
    }
    expect(
      replaySampleRate({ ...base, now: now + 3_600_000 * 6, local, session: store() }),
    ).toBe(0.1);
  });

  it("records any session within 15 minutes of first sight", () => {
    const local = store({
      telemetry_session_tracking: JSON.stringify({ firstSeenAt: now, sessionCount: 40 }),
    });
    expect(
      replaySampleRate({ ...base, now: now + 14 * 60_000, local, session: store() }),
    ).toBe(1);
    expect(
      replaySampleRate({ ...base, now: now + 16 * 60_000, local, session: store() }),
    ).toBe(0.1);
  });

  it("counts a reloaded tab as one session", () => {
    const local = store();
    const session = store();
    for (let i = 0; i < 20; i++) {
      replaySampleRate({ ...base, now: now + 3_600_000, local, session });
    }
    expect(JSON.parse(local.data.telemetry_session_tracking).sessionCount).toBe(1);
  });

  it("falls to 10% when storage is unavailable or corrupt", () => {
    expect(replaySampleRate({ ...base, local: null, session: null })).toBe(1);
    expect(
      replaySampleRate({
        ...base,
        local: store({ telemetry_session_tracking: "{not json" }),
        session: store(),
      }),
    ).toBe(0.1);
  });
});

describe("consent withdrawn mid-session", () => {
  it("calls disableTelemetry when crash reporting is switched off", async () => {
    await initTelemetry();
    expect(runtime.disableTelemetry).not.toHaveBeenCalled();
    privacy.set({ crashReportingEnabled: false });
    expect(runtime.disableTelemetry).toHaveBeenCalledTimes(1);
  });

  it("does not call it while consent stays on", async () => {
    await initTelemetry();
    privacy.set({ analyticsEnabled: false });
    expect(runtime.disableTelemetry).not.toHaveBeenCalled();
  });

  // The SDK's fetch/XHR and error spans have no off switch once it runs, so
  // only a page without it stops them: withdrawal reloads, and the reloaded
  // page's consent-gated startup never initialises the SDK.
  it("reloads into a startup that leaves the SDK out", async () => {
    const reload = vi.fn();
    setReloadForTests(reload);
    await initTelemetry();
    expect(runtime.initBrowserTelemetry).toHaveBeenCalledTimes(1);

    privacy.set({ crashReportingEnabled: false });
    await vi.waitFor(() => expect(reload).toHaveBeenCalledTimes(1));

    // What the reload runs: a fresh module state, consent now off.
    resetTelemetryForTests();
    expect(await initTelemetry()).toBe(false);
    expect(runtime.initBrowserTelemetry).toHaveBeenCalledTimes(1);
  });

  it("waits for the switch to be saved before reloading, and reloads once", async () => {
    const reload = vi.fn();
    setReloadForTests(reload);
    let save!: () => void;
    privacy.write.settled = new Promise((resolve) => (save = resolve));
    await initTelemetry();

    privacy.set({ crashReportingEnabled: false });
    // The optimistic flip and the refetch after the save both report it.
    privacy.set({ crashReportingEnabled: false });
    await Promise.resolve();
    expect(runtime.disableTelemetry).toHaveBeenCalledTimes(1);
    expect(reload).not.toHaveBeenCalled();

    save();
    await vi.waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
    privacy.set({ crashReportingEnabled: false });
    await Promise.resolve();
    expect(reload).toHaveBeenCalledTimes(1);
  });

  // The runtime awaits visibility and the SDK import before it initialises,
  // and the consent subscription does not replay: a withdrawal saved in that
  // window is the only one there will be.
  it("stops and reloads when consent is withdrawn while the SDK is starting", async () => {
    const reload = vi.fn();
    setReloadForTests(reload);
    let finish!: (running: boolean) => void;
    runtime.initBrowserTelemetry.mockImplementationOnce(
      () => new Promise<boolean>((resolve) => (finish = resolve)),
    );
    const starting = initTelemetry();
    await vi.waitFor(() => expect(runtime.initBrowserTelemetry).toHaveBeenCalledTimes(1));

    // Withdrawn and saved before the SDK finished starting: no listener yet.
    privacy.set({ crashReportingEnabled: false });
    finish(true);
    await starting;

    expect(runtime.disableTelemetry).toHaveBeenCalledTimes(1);
    await vi.waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
    resetTelemetryForTests();
    expect(await initTelemetry()).toBe(false);
    expect(runtime.initBrowserTelemetry).toHaveBeenCalledTimes(1);
  });

  it("does not reload when the server refused the switch", async () => {
    const reload = vi.fn();
    setReloadForTests(reload);
    let save!: () => void;
    privacy.write.settled = new Promise((resolve) => (save = resolve));
    await initTelemetry();

    privacy.set({ crashReportingEnabled: false });
    // The rollback restores consent before the write settles.
    privacy.state.crashReportingEnabled = true;
    save();
    await privacy.write.settled;
    await Promise.resolve();
    expect(reload).not.toHaveBeenCalled();
  });
});

describe("user and error reporting", () => {
  it("sets the user by id only", () => {
    setTelemetryUserId({ id: "u-1" });
    expect(runtime.setTelemetryUser).toHaveBeenCalledWith("u-1");
  });

  it("clears the user on sign-out", () => {
    setTelemetryUserId(null);
    expect(runtime.setTelemetryUser).toHaveBeenCalledWith(null);
  });

  it("leaves the user alone when no endpoint is configured", () => {
    stubConfig({ OTEL_ENDPOINT: "" });
    setTelemetryUserId({ id: "u-1" });
    expect(runtime.setTelemetryUser).not.toHaveBeenCalled();
  });

  it("tells the Electron main process the user, whose own telemetry cannot see it", () => {
    const setTelemetryUser = vi.fn(async () => {});
    (window as { electronAPI?: unknown }).electronAPI = { setTelemetryUser };
    setTelemetryUserId({ id: "u-1" });
    setTelemetryUserId(null);
    expect(setTelemetryUser.mock.calls).toEqual([["u-1"], [null]]);
  });

  it("reports a boundary-caught error with its component stack", () => {
    const err = new Error("boom");
    reportErrorToTelemetry(err, "\n    at Foo");
    expect(runtime.reportException).toHaveBeenCalledWith(err, {
      componentStack: "\n    at Foo",
    });
  });

  it("bounds the component stack", () => {
    reportErrorToTelemetry(new Error("boom"), "x".repeat(10_000));
    const attrs = (runtime.reportException.mock.calls[0] as unknown[])[1] as {
      componentStack: string;
    };
    expect(attrs.componentStack.length).toBe(4000);
  });

  it("does not report without consent or without an endpoint", () => {
    privacy.state.crashReportingEnabled = false;
    reportErrorToTelemetry(new Error("boom"));
    privacy.state.crashReportingEnabled = true;
    stubConfig({ OTEL_ENDPOINT: "" });
    reportErrorToTelemetry(new Error("boom"));
    expect(runtime.reportException).not.toHaveBeenCalled();
  });
});

describe("buildTelemetryConfig", () => {
  const base = { config: {}, prerelease: false, buildVersion: "1", replayRate: 1 };

  it("names the forge frontend, so the SDK emits forge.frontend and the build's source maps can be found", () => {
    expect(buildTelemetryConfig(base).frontend).toBe("reliant-web");
    expect(buildTelemetryConfig({ ...base, desktop: true }).frontend).toBe("desktop");
  });

  it("fixes the privacy options regardless of config", () => {
    const config = buildTelemetryConfig({
      config: { OTEL_REPLAY: "true" },
      prerelease: false,
      buildVersion: "1",
      replayRate: 1,
    });
    expect(config.consoleCapture).toBe(false);
    expect(config.advancedNetworkCapture).toBe(false);
  });
});

// The public demo (lib/shell.ts `demo`): anonymous visitors, its own service,
// replay by DEMO_TELEMETRY, and nothing at all for a browser that opted out.
describe("the demo shell", () => {
  const base = { config: {}, prerelease: false, buildVersion: "1", replayRate: 1 };
  const setNavigator = (props: Record<string, unknown>) => {
    for (const [key, value] of Object.entries(props)) Object.defineProperty(navigator, key, { value, configurable: true });
  };
  afterEach(() => setNavigator({ doNotTrack: null, globalPrivacyControl: undefined }));

  it("reports as forge-demo, still the reliant-web frontend (source maps)", () => {
    const config = buildTelemetryConfig({ ...base, shell: "demo", demoTelemetry: "replay" });
    expect(config.serviceName).toBe(DEMO_SHELL_SERVICE_NAME);
    expect(DEMO_SHELL_SERVICE_NAME).toBe("forge-demo");
    expect(config.frontend).toBe("reliant-web");
    expect(buildTelemetryConfig({ ...base, shell: "forge" }).serviceName).toBe("forge-web");
    expect(buildTelemetryConfig(base).serviceName).toBe("reliant-web");
  });

  it("records replay by DEMO_TELEMETRY, not OTEL_REPLAY", () => {
    expect(buildTelemetryConfig({ ...base, shell: "demo", demoTelemetry: "replay" }).replay).toBe(true);
    expect(buildTelemetryConfig({ ...base, config: { OTEL_REPLAY: "true" }, shell: "demo", demoTelemetry: "analytics" }).replay).toBe(false);
    expect(demoReplayRate(undefined)).toBe(1);
    expect(demoReplayRate("0.25")).toBe(0.25);
  });

  it("starts anonymously with replay on by default", async () => {
    resetShellForTests("demo");
    expect(await initTelemetry()).toBe(true);
    expect(lastConfig()).toMatchObject({ serviceName: "forge-demo", replay: true, replaySampleRate: 1 });
    expect(runtime.setTelemetryUser).not.toHaveBeenCalled();
  });

  it("records analytics without replay when DEMO_TELEMETRY=analytics, and nothing when off", async () => {
    resetShellForTests("demo");
    stubConfig({ DEMO_TELEMETRY: "analytics" });
    await initTelemetry();
    expect(lastConfig().replay).toBe(false);
    resetTelemetryForTests();
    runtime.initBrowserTelemetry.mockClear();
    stubConfig({ DEMO_TELEMETRY: "off" });
    expect(await initTelemetry()).toBe(false);
    expect(runtime.initBrowserTelemetry).not.toHaveBeenCalled();
  });

  it.each([
    ["Global Privacy Control", { globalPrivacyControl: true }],
    ["Do Not Track", { doNotTrack: "1" }],
  ])("never starts for a browser that sends %s", async (_name, signal) => {
    resetShellForTests("demo");
    setNavigator(signal);
    expect(await initTelemetry()).toBe(false);
    expect(runtime.initBrowserTelemetry).not.toHaveBeenCalled();
  });

  it("leaves the other shells alone when the browser sends DNT (their consent is the settings switch)", async () => {
    resetShellForTests("forge");
    setNavigator({ doNotTrack: "1" });
    expect(await initTelemetry()).toBe(true);
  });
});

// The demo host on PRODUCTION's config.js. Prod's document is rendered by
// control-plane (deploy/kcl/lib/env.k: reliant_web_runtime_config("prod") |
// reliant_web_telemetry_config(), served by lib/reliant_web.k): the same
// document for every host of the reliant-web site, and it carries no DEMO_*
// key, so the demo runs on this bundle's defaults plus prod's same-origin
// OTEL_ENDPOINT. This pins that projection end to end: the keys a render may
// carry, their defaults, the shell the host resolves to, and what telemetry
// starts with.
describe("the demo host on the production config document", () => {
  // Prod's key set, as control-plane renders it (values are placeholders
  // except the ones the demo reads).
  const PROD_DOCUMENT: Record<string, string> = {
    API_URL: "https://api.reliantlabs.io",
    CONTROL_PLANE_API_URL: "https://admin.reliantlabs.io",
    GATEWAY_URL: "https://gateway.reliantlabs.io",
    APP_URL: "https://app.reliantlabs.io",
    AUTH_MODE: "cloud",
    SUPABASE_URL: "https://auth.reliantlabs.io",
    SUPABASE_ANON_KEY: "sb_publishable_example",
    DISABLE_TLS: "false",
    STRIPE_PUBLISHABLE_KEY: "pk_live_example",
    CLI_DEFAULTS_BAKED: "true",
    OTEL_ENDPOINT: "/_otel",
    OTEL_REPLAY: "true",
    OTEL_REPLAY_SAMPLE_POLICY: "new-user-funnel",
  };
  const DEMO_KEYS = ["DEMO_APP_HOST", "DEMO_TELEMETRY", "DEMO_TOUR", "DEMO_DATA_SOURCE", "DEMO_DATA_URL"];
  const shellOn = (hostname: string) =>
    resolveShell({ hostname, search: "", forgeHost: forgeAppHost(), demoHost: demoAppHost(), electron: false, storage: null });

  it("lets a render carry every demo key, and the KCL runtime projection emits each", () => {
    // config_schema_gen.json is what control-plane's render admits keys by;
    // frontend_config_gen.k's runtime lambda is what a KCL-declared env emits.
    const schema = JSON.parse(readFileSync(resolve(__dirname, "../config_schema_gen.json"), "utf8")) as { keys: Array<{ name: string }> };
    const kcl = readFileSync(resolve(__dirname, "../../../../../deploy/kcl/frontend_config_gen.k"), "utf8");
    for (const key of DEMO_KEYS) {
      expect(schema.keys.map((k) => k.name)).toContain(key);
      expect(kcl).toContain(`"${key}" = c.${key.toLowerCase()}`);
    }
  });

  it("resolves demo.reliantlabs.io to the demo shell and every other prod host as before", () => {
    setRuntimeConfig(PROD_DOCUMENT);
    expect(demoAppHost()).toBe("demo.reliantlabs.io");
    expect(shellOn("demo.reliantlabs.io")).toBe("demo");
    expect(shellOn("forge.reliantlabs.io")).toBe("forge");
    expect(shellOn("app.reliantlabs.io")).toBe("reliant");
  });

  it("starts telemetry as forge-demo, to the same-origin /_otel, with replay on every visit", async () => {
    setRuntimeConfig(PROD_DOCUMENT);
    resetShellForTests("demo");
    expect(await initTelemetry()).toBe(true);
    expect(lastConfig()).toMatchObject({ serviceName: "forge-demo", endpoint: "/_otel", replay: true, replaySampleRate: 1 });
    // A relative endpoint is the demo host's own origin: no cross-origin ingest.
    expect(lastConfig().endpoint?.startsWith("/")).toBe(true);
  });

  it("follows an override of the demo keys in the document", async () => {
    setRuntimeConfig({ ...PROD_DOCUMENT, DEMO_APP_HOST: "https://demo.staging.test/", DEMO_TELEMETRY: "analytics" });
    expect(shellOn("demo.staging.test")).toBe("demo");
    expect(shellOn("demo.reliantlabs.io")).toBe("reliant");
    resetShellForTests("demo");
    expect(await initTelemetry()).toBe(true);
    expect(lastConfig()).toMatchObject({ serviceName: "forge-demo", endpoint: "/_otel", replay: false });

    resetTelemetryForTests();
    runtime.initBrowserTelemetry.mockClear();
    setRuntimeConfig({ ...PROD_DOCUMENT, DEMO_TELEMETRY: "off" });
    expect(await initTelemetry()).toBe(false);
    expect(runtime.initBrowserTelemetry).not.toHaveBeenCalled();
  });
});

// The landing → app handoff (landingAttribution.ts): held at boot, handed to the
// runtime when telemetry starts, after the sign-in has replaced the URL it came on.
describe("initTelemetry: the landing attribution handoff", () => {
  // A fixed clock: the handoff's age is checked against HOLD_MS.
  const NOW = Date.UTC(2026, 10, 4, 12, 0, 0);
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(NOW);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("hands the held rid and utm_* to the runtime before it starts, and drops it once it runs", async () => {
    holdLandingAttribution("?rid=v_landing_123456&utm_source=reddit", NOW);
    window.history.replaceState(null, "", "/auth/callback?code=abc");
    await initTelemetry();
    expect(runtime.enableAttribution).toHaveBeenCalledWith({ search: "?rid=v_landing_123456&utm_source=reddit" });
    expect(runtime.enableAttribution.mock.invocationCallOrder[0]).toBeLessThan(
      runtime.initBrowserTelemetry.mock.invocationCallOrder[0],
    );
    expect(localStorage.getItem(LANDING_ATTRIBUTION_KEY)).toBeNull();
  });

  it("keeps it held when the runtime does not start", async () => {
    runtime.initBrowserTelemetry.mockResolvedValueOnce(false);
    holdLandingAttribution("?rid=v_landing_123456&utm_source=reddit", NOW);
    await expect(initTelemetry()).resolves.toBe(false);
    expect(localStorage.getItem(LANDING_ATTRIBUTION_KEY)).not.toBeNull();
  });

  it("keeps it held when the runtime's start rejects", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    runtime.initBrowserTelemetry.mockRejectedValueOnce(new Error("sdk load failed"));
    holdLandingAttribution("?rid=v_landing_123456&utm_source=reddit", NOW);
    await expect(initTelemetry()).resolves.toBe(false);
    warn.mockRestore();
    expect(localStorage.getItem(LANDING_ATTRIBUTION_KEY)).not.toBeNull();
  });

  it("hands it over on a later start in the same page after a start that did not run", async () => {
    runtime.initBrowserTelemetry.mockResolvedValueOnce(false);
    holdLandingAttribution("?rid=v_landing_123456&utm_source=reddit", NOW);
    await expect(initTelemetry()).resolves.toBe(false);
    await expect(initTelemetry()).resolves.toBe(true);
    expect(runtime.initBrowserTelemetry).toHaveBeenCalledTimes(2);
    expect(runtime.enableAttribution).toHaveBeenLastCalledWith({ search: "?rid=v_landing_123456&utm_source=reddit" });
    expect(localStorage.getItem(LANDING_ATTRIBUTION_KEY)).toBeNull();
  });

  it("hands it over on a later start in the same page after a start that rejected", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    runtime.initBrowserTelemetry.mockRejectedValueOnce(new Error("sdk load failed"));
    holdLandingAttribution("?rid=v_landing_123456&utm_source=reddit", NOW);
    await expect(initTelemetry()).resolves.toBe(false);
    warn.mockRestore();
    await expect(initTelemetry()).resolves.toBe(true);
    expect(runtime.initBrowserTelemetry).toHaveBeenCalledTimes(2);
    expect(localStorage.getItem(LANDING_ATTRIBUTION_KEY)).toBeNull();
  });

  it("drops it without analytics consent", async () => {
    privacy.state.analyticsEnabled = false;
    holdLandingAttribution("?rid=v_landing_123456&utm_source=reddit", NOW);
    await initTelemetry();
    expect(runtime.enableAttribution).not.toHaveBeenCalled();
    expect(localStorage.getItem(LANDING_ATTRIBUTION_KEY)).toBeNull();
  });

  it("leaves the runtime's own capture alone when nothing is held", async () => {
    await initTelemetry();
    expect(runtime.enableAttribution).not.toHaveBeenCalled();
  });
});

describe("a console.error about an RPC failure the transport already decided on", () => {
  // The SDK records any Error passed to console.error. Stand in for its
  // wrapper: the mocked init wraps console.error the way the SDK does.
  function installSdkWrapper(recorded: unknown[][]) {
    runtime.initBrowserTelemetry.mockImplementationOnce(async () => {
      const inner = console.error;
      console.error = (...args: unknown[]) => {
        recorded.push(args);
        inner(...args);
      };
      return true;
    });
  }

  let original: typeof console.error;
  let printed: unknown[][];
  beforeEach(() => {
    original = console.error;
    printed = [];
    console.error = (...args: unknown[]) => void printed.push(args);
  });
  afterEach(() => {
    resetTelemetryForTests();
    console.error = original;
  });

  it("is logged but not recorded by the SDK", async () => {
    const { ConnectError, Code } = await import("@connectrpc/connect");
    const { noteRpcFailureDecided } = await import("../handledRpcFailures");
    const recorded: unknown[][] = [];
    installSdkWrapper(recorded);
    await initTelemetry();

    // What the transport saw, and what the caller holds: the runtime's
    // normalized error wrapping it.
    const seen = new ConnectError("file not found: /w/post.md", Code.NotFound);
    noteRpcFailureDecided(seen);
    const held = new ConnectError("file not found: /w/post.md", Code.NotFound, undefined, undefined, seen);
    console.error("Failed to fetch file preview info:", held);

    expect(recorded).toEqual([]);
    expect(printed).toEqual([["Failed to fetch file preview info:", held]]);
  });

  it("leaves every other console.error to the SDK", async () => {
    const { ConnectError, Code } = await import("@connectrpc/connect");
    const recorded: unknown[][] = [];
    installSdkWrapper(recorded);
    await initTelemetry();

    const bug = new TypeError("Cannot read properties of undefined (reading 'id')");
    const undecided = new ConnectError("stream reset", Code.Internal);
    console.error("render failed:", bug);
    console.error(undecided);

    expect(recorded).toEqual([["render failed:", bug], [undecided]]);
  });

  it("is not installed when the SDK did not start", async () => {
    runtime.initBrowserTelemetry.mockResolvedValueOnce(false);
    const before = console.error;
    await initTelemetry();
    expect(console.error).toBe(before);
  });
});
