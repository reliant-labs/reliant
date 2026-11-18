// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The HyperDX browser client, built from its parts.
//
// @hyperdx/browser is a ~100-line wrapper (packages/browser/src/index.ts in
// hyperdx-js) over two packages: @hyperdx/otel-web, which does errors, traces,
// web vitals and the RUM session, and @hyperdx/otel-web-session-recorder,
// which records replay. It ships as ONE pre-built UMD file with both inlined,
// so importing it evaluated rrweb, protobufjs and fflate in a single long
// task (~500 ms at 4x CPU) whether replay was on or not, and its recorder did
// its encoding on the main thread.
//
// This module is that wrapper, ported: it drives @hyperdx/otel-web's ESM
// build (imported lazily, so it is its own chunk) with exactly the options the
// wrapper derived from HyperDX.init's, and records replay with the runtime's
// own recorder (replay-recorder.ts) instead of the bundled one. initBrowser-
// Telemetry still builds HyperDX.init's options (buildSdkOptions, public and
// unchanged) and calls init() with them; nothing an app passes changes
// meaning. What the wrapper did that the runtime never used is left out: the
// Intercom hook (always disabled) and session URLs.

import { trace } from "@opentelemetry/api";

import {
  REPLAY_LOGS_PATH,
  REPLAY_SCOPE,
  type ReplayRecorder,
  type ReplayStart,
  startReplayRecorder,
} from "./replay-recorder.js";

/** The slice of the client initBrowserTelemetry calls: HyperDX.init's shape. */
export interface HyperDXClient {
  init(config: Record<string, unknown>): void;
  recordException(error: unknown, attributes?: Record<string, unknown>): void;
  addAction?(name: string, attributes?: Record<string, unknown>): void;
  setGlobalAttributes?(attributes: Record<string, string>): void;
  stopSessionRecorder?(): void;
  getSessionId?(): string | undefined;
}

/** The slice of @hyperdx/otel-web's Rum this module drives. */
export interface RumLike {
  init(options: Record<string, unknown>): void;
  recordException(
    error: unknown,
    attributes?: Record<string, unknown>,
  ): unknown;
  addAction(name: string, attributes?: Record<string, unknown>): void;
  setGlobalAttributes(attributes: Record<string, string>): void;
  getSessionId(): string | undefined;
  provider?: { resource?: { attributes?: Record<string, unknown> } };
}

/** What the client needs to start replay; a test seam. */
export type StartRecorder = typeof startReplayRecorder;

/**
 * Loads @hyperdx/otel-web (lazily: its own chunk) and returns the client
 * over it. Rejects when the chunk cannot load (an ad blocker, a network
 * failure); initBrowserTelemetry treats that as "no telemetry".
 */
export async function loadSdkClient(): Promise<HyperDXClient> {
  const rum = resolveRum(await import("@hyperdx/otel-web"));
  if (!rum) {
    throw new Error("@hyperdx/otel-web has no Rum");
  }
  return createSdkClient(rum);
}

/**
 * The client over `rum`: HyperDX.init's options become Rum.init's as the
 * wrapper made them, and replay becomes the runtime's recorder.
 */
export function createSdkClient(
  rum: RumLike,
  startRecorder: StartRecorder = startReplayRecorder,
): HyperDXClient {
  let recorder: ReplayRecorder | null = null;
  let advancedNetworkCapture = false;
  const hasWindow = () => typeof window !== "undefined";
  return {
    init(config) {
      if (!hasWindow()) {
        return;
      }
      advancedNetworkCapture = config.advancedNetworkCapture === true;
      const ingest = typeof config.url === "string" ? config.url : "";
      rum.init(rumOptions(config, () => advancedNetworkCapture));
      if (config.disableReplay !== true) {
        recorder = startRecorder({
          url:
            typeof config.logsUrl === "string"
              ? config.logsUrl
              : `${ingest}${REPLAY_LOGS_PATH}`,
          apiKey: typeof config.apiKey === "string" ? config.apiKey : undefined,
          rrweb: recorderOptions(config),
          resource: () => ({ ...(rum.provider?.resource?.attributes ?? {}) }),
          sessionId: () => rum.getSessionId(),
          tracer: trace.getTracer(REPLAY_SCOPE.name, REPLAY_SCOPE.version),
          start: replayStart(config.replayStart),
          flushIntervalMs:
            typeof config.replayFlushIntervalMs === "number"
              ? config.replayFlushIntervalMs
              : undefined,
          debug: config.debug === true,
        });
      }
    },
    recordException(error, attributes) {
      if (hasWindow()) {
        void rum.recordException(error, attributes);
      }
    },
    addAction(name, attributes) {
      if (hasWindow()) {
        rum.addAction(name, attributes);
      }
    },
    setGlobalAttributes(attributes) {
      if (hasWindow()) {
        rum.setGlobalAttributes(attributes);
      }
    },
    stopSessionRecorder() {
      recorder?.stop();
    },
    getSessionId() {
      return rum.getSessionId();
    },
  };
}

/**
 * Rum.init's options from HyperDX.init's, as @hyperdx/browser 0.26.0 makes
 * them: traces to `${url}/v1/traces`, the page-visibility instrumentation on,
 * console capture as asked, fetch/xhr propagating to the given targets, and
 * the caller's `instrumentations` spread over all of it. Exported for tests.
 */
export function rumOptions(
  config: Record<string, unknown>,
  advancedNetworkCapture: () => boolean = () =>
    config.advancedNetworkCapture === true,
): Record<string, unknown> {
  const url = typeof config.url === "string" ? config.url : undefined;
  const targets = config.tracePropagationTargets;
  const propagate =
    targets != null ? { propagateTraceHeaderCorsUrls: targets } : {};
  return {
    debug: config.debug === true,
    url:
      typeof config.tracesUrl === "string"
        ? config.tracesUrl
        : `${url ?? "https://in-otel.hyperdx.io"}/v1/traces`,
    allowInsecureUrl: true,
    apiKey: config.apiKey,
    applicationName: config.service,
    ignoreUrls: config.ignoreUrls,
    resourceAttributes: config.otelResourceAttributes,
    instrumentations: {
      visibility: true,
      console: config.captureConsole ?? config.consoleCapture ?? false,
      fetch: { ...propagate, advancedNetworkCapture },
      xhr: { ...propagate, advancedNetworkCapture },
      ...((config.instrumentations as Record<string, unknown>) ?? {}),
    },
  };
}

/**
 * rrweb record()'s options from HyperDX.init's, as the wrapper handed them to
 * its recorder: `replay` spread first, then masking and blocking, which no
 * `replay` key can loosen. One deliberate difference: the wrapper also spread
 * a top-level `sampling` that was undefined unless given, which silently
 * erased `replay.sampling`; here `replay.sampling` holds unless `sampling` is
 * set. Exported for tests.
 */
export function recorderOptions(
  config: Record<string, unknown>,
): Record<string, unknown> {
  const replay = (config.replay as Record<string, unknown>) ?? {};
  const out: Record<string, unknown> = {
    ...replay,
    blockClass: config.blockClass,
    blockSelector: config.blockSelector,
    ignoreClass: config.ignoreClass,
    maskAllInputs: config.maskAllInputs ?? true,
    maskTextClass: config.maskClass,
    maskTextSelector: config.maskAllText === true ? "*" : undefined,
    recordCanvas: config.recordCanvas === true,
  };
  const sampling = config.sampling ?? replay.sampling;
  if (sampling !== undefined) {
    out.sampling = sampling;
  }
  for (const key of Object.keys(out)) {
    if (out[key] === undefined) {
      delete out[key];
    }
  }
  return out;
}

function replayStart(value: unknown): ReplayStart {
  return value === "interaction" ? "interaction" : "load";
}

// A bundler hands back a module NAMESPACE; @hyperdx/otel-web exports its Rum
// object both as `default` and as `Rum`, and its methods use `this`, so the
// object itself is what is called, never the namespace in front of it.
export function resolveRum(mod: unknown): RumLike | null {
  let cur: unknown = mod;
  for (let depth = 0; depth < 4; depth++) {
    if (isRum(cur)) {
      return cur;
    }
    const named = peek(cur, "Rum");
    if (isRum(named)) {
      return named;
    }
    cur = peek(cur, "default");
    if (cur === undefined) {
      break;
    }
  }
  return null;
}

function isRum(v: unknown): v is RumLike {
  return (
    typeof peek(v, "init") === "function" &&
    typeof peek(v, "recordException") === "function" &&
    typeof peek(v, "addAction") === "function"
  );
}

function peek(obj: unknown, key: string): unknown {
  try {
    return (obj as Record<string, unknown> | null)?.[key];
  } catch {
    return undefined;
  }
}
