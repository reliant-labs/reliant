import { describe, expect, it, beforeEach, afterEach, vi } from "vitest";

// installConsoleOverride() replaces console.* with wrappers that themselves
// forward to the Electron main process. logger.warn/error also forwarded, and
// THEN called the (now-overridden) global console method — so every logger call
// shipped two IPC messages and produced two identical lines on disk. Measured
// in production logs: every warn appeared exactly twice with the same
// millisecond timestamp, doubling both the IPC traffic and the log volume.
//
// This locks in one-send-per-call while keeping console output intact.

describe("logger does not double-send to Electron", () => {
  let sent: Array<[string, unknown[]]>;
  let pristineConsole: Record<string, unknown>;

  beforeEach(() => {
    // Each test re-imports logger.ts, which re-runs installConsoleOverride().
    // Without restoring a pristine console first, each install would wrap the
    // PREVIOUS test's wrapper and the send counts would stack — a test
    // artifact, not product behavior (the module loads once in the app).
    pristineConsole = {
      log: console.log,
      error: console.error,
      warn: console.warn,
      info: console.info,
      debug: console.debug,
    };
    vi.resetModules();
    sent = [];
    (globalThis as unknown as { window: unknown }).window = globalThis;
    (globalThis as unknown as { electronAPI: unknown }).electronAPI = {
      log: (level: string, ...args: unknown[]) => {
        sent.push([level, args]);
      },
    };
  });

  afterEach(() => {
    Object.assign(console, pristineConsole);
    delete (globalThis as unknown as { electronAPI?: unknown }).electronAPI;
    vi.restoreAllMocks();
  });

  it("sends exactly one IPC message per logger.warn call", async () => {
    const { logger } = await import("../logger");

    logger.warn("[Test] a single warning", { detail: 1 });

    const warnSends = sent.filter(([level]) => level === "warn");
    expect(warnSends).toHaveLength(1);
  });

  it("sends exactly one IPC message per logger.error call", async () => {
    const { logger } = await import("../logger");

    logger.error("[Test] a single error");

    const errorSends = sent.filter(([level]) => level === "error");
    expect(errorSends).toHaveLength(1);
  });

  it("still forwards a direct console.warn call exactly once", async () => {
    // The console override remains the capture path for third-party code that
    // calls console.* directly; the fix must not disable it.
    await import("../logger");

    console.warn("[Test] direct console call");

    expect(sent.filter(([level]) => level === "warn")).toHaveLength(1);
  });
});
