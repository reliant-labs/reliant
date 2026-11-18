// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The port of @hyperdx/browser's wrapper: HyperDX.init's options become
// @hyperdx/otel-web's Rum.init options exactly as the wrapper made them, and
// replay becomes the runtime's recorder, configured as the wrapper configured
// its own (masking that no `replay` key loosens), minus its one bug.
import { afterEach, describe, expect, it, vi } from "vitest";

import { buildSdkOptions } from "./telemetry.js";
import {
  createSdkClient,
  recorderOptions,
  resolveRum,
  type RumLike,
  rumOptions,
} from "./sdk-client.js";

afterEach(() => {
  vi.unstubAllGlobals();
});

const ORIGIN = "https://shop.test";

function fakeRum() {
  const calls: Array<[string, unknown[]]> = [];
  const rum: RumLike & { calls: typeof calls } = {
    calls,
    init(...args) {
      if (this !== rum) throw new TypeError("init called off the Rum object");
      calls.push(["init", args]);
      rum.provider = {
        resource: {
          attributes: { "service.name": "web", "rum.sessionId": "s1" },
        },
      };
    },
    recordException(...args) {
      calls.push(["recordException", args]);
    },
    addAction(...args) {
      calls.push(["addAction", args]);
    },
    setGlobalAttributes(...args) {
      calls.push(["setGlobalAttributes", args]);
    },
    getSessionId: () => "s1",
  };
  return rum;
}

describe("rumOptions", () => {
  it("is what @hyperdx/browser 0.26.0 hands Rum.init for the runtime's options", () => {
    const options = buildSdkOptions(
      {
        serviceName: "web",
        environment: "prod",
        apiOrigins: ["https://api.test"],
      },
      `${ORIGIN}/_otel`,
      ORIGIN,
    );
    const rum = rumOptions(options, () => false);
    expect(rum).toMatchObject({
      debug: false,
      url: `${ORIGIN}/_otel/v1/traces`,
      allowInsecureUrl: true,
      apiKey: "forge-browser-public",
      applicationName: "web",
      ignoreUrls: options.ignoreUrls,
      resourceAttributes: options.otelResourceAttributes,
    });
    const inst = rum.instrumentations as Record<
      string,
      Record<string, unknown>
    >;
    // The wrapper's own keys, then the caller's `instrumentations` over them.
    expect(inst.visibility).toBe(true);
    expect(inst.console).toBe(true);
    expect(inst.interactions).toBe(false);
    // The caller's fetch/xhr replace the wrapper's whole keys, as they did.
    expect(inst.fetch).toBe(
      (options.instrumentations as Record<string, unknown>).fetch,
    );
  });

  it("propagates to the targets and reads advancedNetworkCapture live when the caller does not override fetch", () => {
    let capture = false;
    const rum = rumOptions(
      { url: "https://c.test", service: "s", tracePropagationTargets: [/x/] },
      () => capture,
    );
    const fetch = (
      rum.instrumentations as Record<string, Record<string, unknown>>
    ).fetch!;
    expect(fetch.propagateTraceHeaderCorsUrls).toEqual([/x/]);
    capture = true;
    expect((fetch.advancedNetworkCapture as () => boolean)()).toBe(true);
    expect(rum.url).toBe("https://c.test/v1/traces");
  });
});

describe("recorderOptions", () => {
  it("passes the replay options and forces masking and blocking over them", () => {
    const options = buildSdkOptions(
      { serviceName: "web", replay: true },
      `${ORIGIN}/_otel`,
      ORIGIN,
    );
    const rrweb = recorderOptions({
      ...options,
      replay: {
        ...(options.replay as object),
        // A `replay` key cannot loosen masking: the wrapper set these after.
        maskAllInputs: false,
        maskTextSelector: undefined,
        recordCanvas: true,
      },
    });
    expect(rrweb).toMatchObject({
      maskAllInputs: true,
      maskTextSelector: "*",
      recordCanvas: false,
      blockSelector: options.blockSelector,
      inlineImages: false,
      collectFonts: false,
      slimDOMOptions: "all",
    });
    expect(rrweb.plugins).toHaveLength(1);
  });

  it("keeps replay.sampling, which the wrapper silently erased with an undefined top-level `sampling`", () => {
    const options = buildSdkOptions(
      { serviceName: "web", replay: true },
      `${ORIGIN}/_otel`,
      ORIGIN,
    );
    expect(recorderOptions(options).sampling).toEqual({
      mousemove: 100,
      scroll: 150,
      media: 800,
      input: "last",
    });
    // A top-level `sampling`, when given, still wins, as in the wrapper.
    expect(
      recorderOptions({ ...options, sampling: { scroll: 50 } }).sampling,
    ).toEqual({ scroll: 50 });
  });
});

describe("createSdkClient", () => {
  it("inits Rum, and starts the runtime's recorder on the logs URL with the SDK's resource and session", () => {
    vi.stubGlobal("window", {});
    const rum = fakeRum();
    const start = vi.fn(() => ({
      started: Promise.resolve(true),
      flush: () => {},
      stop: vi.fn(),
    }));
    const client = createSdkClient(rum, start);
    client.init(
      buildSdkOptions(
        {
          serviceName: "web",
          replay: true,
          replayStart: "interaction",
          replayFlushIntervalMs: 20_000,
        },
        `${ORIGIN}/_otel`,
        ORIGIN,
      ),
    );
    expect(rum.calls.map(([name]) => name)).toEqual(["init"]);
    expect(start).toHaveBeenCalledTimes(1);
    const recorder = (
      start.mock.calls[0] as unknown as [Record<string, unknown>]
    )[0];
    expect(recorder).toMatchObject({
      url: `${ORIGIN}/_otel/v1/logs`,
      apiKey: "forge-browser-public",
      start: "interaction",
      flushIntervalMs: 20_000,
    });
    expect((recorder.resource as () => unknown)()).toEqual({
      "service.name": "web",
      "rum.sessionId": "s1",
    });
    expect((recorder.sessionId as () => unknown)()).toBe("s1");
  });

  it("starts no recorder with replay off, and stops the one it started", () => {
    vi.stubGlobal("window", {});
    const stop = vi.fn();
    const start = vi.fn(() => ({
      started: Promise.resolve(true),
      flush() {},
      stop,
    }));
    const off = createSdkClient(fakeRum(), start);
    off.init(
      buildSdkOptions({ serviceName: "web" }, `${ORIGIN}/_otel`, ORIGIN),
    );
    expect(start).not.toHaveBeenCalled();
    off.stopSessionRecorder?.();

    const on = createSdkClient(fakeRum(), start);
    on.init(
      buildSdkOptions(
        { serviceName: "web", replay: true },
        `${ORIGIN}/_otel`,
        ORIGIN,
      ),
    );
    on.stopSessionRecorder?.();
    expect(stop).toHaveBeenCalledTimes(1);
  });

  it("forwards errors, actions, attributes and the session to Rum, and does nothing without a window", () => {
    const rum = fakeRum();
    const client = createSdkClient(rum, vi.fn());
    client.init({ url: ORIGIN });
    client.addAction?.("x");
    expect(rum.calls).toEqual([]);

    vi.stubGlobal("window", {});
    const err = new Error("boom");
    client.recordException(err, { a: 1 });
    client.addAction?.("signup", { plan: "pro" });
    client.setGlobalAttributes?.({ userId: "u" });
    expect(client.getSessionId?.()).toBe("s1");
    expect(rum.calls).toEqual([
      ["recordException", [err, { a: 1 }]],
      ["addAction", ["signup", { plan: "pro" }]],
      ["setGlobalAttributes", [{ userId: "u" }]],
    ]);
  });
});

describe("resolveRum", () => {
  it("finds the Rum object behind a bundler's module namespace", () => {
    const rum = fakeRum();
    expect(resolveRum({ default: rum, Rum: rum })).toBe(rum);
    expect(resolveRum({ default: { default: rum } })).toBe(rum);
    expect(resolveRum(rum)).toBe(rum);
    expect(resolveRum({ default: {} })).toBeNull();
    expect(resolveRum(undefined)).toBeNull();
  });
});
