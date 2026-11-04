import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setRuntimeConfig } from "@/test/runtimeConfig";

// The option matrix is pinned through the real initTelemetry(): what reaches
// the runtime's initBrowserTelemetry is the privacy boundary, so the test
// drives it rather than a helper in isolation.

const privacy = vi.hoisted(() => {
  const listeners = new Set<(s: { crashReportingEnabled: boolean }) => void>();
  const state = { crashReportingEnabled: true, analyticsEnabled: true };
  return {
    state,
    listeners,
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
}));
vi.mock("@hyperdx/browser", () => ({
  default: { setGlobalAttributes: runtime.setGlobalAttributes },
}));
vi.mock("../../hooks/privacy-queries", () => ({
  privacyConsent: () => privacy.state,
  subscribePrivacyConsent: privacy.subscribe,
}));
vi.mock("../constants", () => ({ isDev: false }));
vi.mock("../appBuild", () => ({
  appBuild: () => ({ version: "1.8.1", commit: "c", ref: "r", built: "t" }),
}));

import {
  buildTelemetryConfig,
  initTelemetry,
  replaySampleRate,
  reportErrorToTelemetry,
  resetTelemetryForTests,
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
  privacy.listeners.clear();
  privacy.state.crashReportingEnabled = true;
  privacy.state.analyticsEnabled = true;
  localStorage.clear();
  sessionStorage.clear();
  delete (window as { electronAPI?: unknown }).electronAPI;
  setRuntimeConfig({ OTEL_ENDPOINT: ENDPOINT });
});

afterEach(() => {
  vi.unstubAllEnvs();
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
