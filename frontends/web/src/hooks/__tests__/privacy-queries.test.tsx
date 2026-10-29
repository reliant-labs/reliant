/**
 * Privacy consent on the generated GetPrivacySettings entry (P9 R4 D6; the
 * privacyStore mirror is gone). Telemetry, error reporting and analytics read it
 * imperatively, so the rules that keep an opt-out honoured are pinned here:
 *
 *  - the initial load waits for a transport that is not ready, then retries;
 *  - a switch flips at once, and back if the server refuses;
 *  - subscribers hear every answer the entry takes (load, write, rollback);
 *  - an unobserved entry is never collected (a collected one would read as
 *    consent), and an opt-out still holds after a sign-out clears the cache.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const config = vi.hoisted(() => ({ waitForConfig: vi.fn(async () => {}) }));
vi.mock("@/lib/configReady", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/configReady")>()),
  waitForConfig: config.waitForConfig,
}));

import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import {
  loadPrivacySettings,
  privacyConsent,
  privacySettingsKey,
  subscribePrivacyConsent,
  usePrivacyConsent,
  useUpdatePrivacySettings,
  type PrivacyConsent,
} from "../privacy-queries";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

let stored: { analyticsEnabled: boolean; crashReportingEnabled: boolean };
let notReadyFor: number;
let refuseWrites: boolean;
let getCalls: number;
/** Resolve a held UpdatePrivacySettings (so a test can look before the answer). */
let release: (() => void) | null;

beforeEach(() => {
  stored = { analyticsEnabled: true, crashReportingEnabled: true };
  notReadyFor = 0;
  refuseWrites = false;
  getCalls = 0;
  release = null;
  config.waitForConfig.mockClear();
  setTransport(
    mockTransport({
      "SettingsService.GetPrivacySettings": () => {
        getCalls++;
        if (notReadyFor > 0) {
          notReadyFor--;
          throw new Error("gRPC client not ready - waiting for backend configuration");
        }
        return { ...stored };
      },
      "SettingsService.UpdatePrivacySettings": async (req) => {
        await new Promise<void>((resolve) => {
          release = resolve;
        });
        if (refuseWrites) throw new ConnectError("refused", Code.PermissionDenied);
        const r = req as { analyticsEnabled?: boolean; crashReportingEnabled?: boolean };
        stored = {
          analyticsEnabled: r.analyticsEnabled ?? stored.analyticsEnabled,
          crashReportingEnabled: r.crashReportingEnabled ?? stored.crashReportingEnabled,
        };
        return { success: true, ...stored };
      },
    }),
  );
});

afterEach(() => {
  release?.();
  cleanup();
  queryClient.clear();
  setTransport(null);
});

/** Put an answer in the entry, as a load would. */
function seed(consent: PrivacyConsent) {
  queryClient.setQueryData(privacySettingsKey(), { $typeName: "reliant.v1.GetPrivacySettingsResponse", ...consent });
}

// First in the file: later tests leave a last-known answer behind (by design).
it("reads as consent before any answer", () => {
  expect(privacyConsent()).toEqual({ crashReportingEnabled: true, analyticsEnabled: true });
});

describe("loadPrivacySettings", () => {
  it("waits for config and retries while the transport is not ready", async () => {
    stored.crashReportingEnabled = false;
    notReadyFor = 2;

    await loadPrivacySettings();

    expect(config.waitForConfig).toHaveBeenCalledTimes(2);
    expect(getCalls).toBe(3);
    expect(privacyConsent().crashReportingEnabled).toBe(false);
  });

  it("gives up quietly after two waits, keeping the previous answer", async () => {
    seed({ crashReportingEnabled: false, analyticsEnabled: false });
    notReadyFor = 5;

    await expect(loadPrivacySettings()).resolves.toBeUndefined();

    expect(config.waitForConfig).toHaveBeenCalledTimes(2);
    expect(privacyConsent()).toEqual({ crashReportingEnabled: false, analyticsEnabled: false });
  });
});

describe("the consent switches", () => {
  it("flip at once and stay flipped once stored", async () => {
    await loadPrivacySettings();
    const consent = renderHook(() => usePrivacyConsent(), { wrapper });
    const update = renderHook(() => useUpdatePrivacySettings(), { wrapper });

    act(() => update.result.current.setAnalytics(false));

    // Before the server answers.
    await waitFor(() => expect(consent.result.current.analyticsEnabled).toBe(false));
    expect(privacyConsent().analyticsEnabled).toBe(false);
    expect(stored.analyticsEnabled).toBe(true);

    act(() => release?.());
    await waitFor(() => expect(stored.analyticsEnabled).toBe(false));
    await waitFor(() => expect(update.result.current.isPending).toBe(false));
    expect(consent.result.current.analyticsEnabled).toBe(false);
  });

  it("flip back when the server refuses", async () => {
    await loadPrivacySettings();
    const consent = renderHook(() => usePrivacyConsent(), { wrapper });
    const update = renderHook(() => useUpdatePrivacySettings(), { wrapper });
    refuseWrites = true;

    act(() => update.result.current.setCrashReporting(false));
    await waitFor(() => expect(consent.result.current.crashReportingEnabled).toBe(false));

    act(() => release?.());
    await waitFor(() => expect(consent.result.current.crashReportingEnabled).toBe(true));
    expect(privacyConsent().crashReportingEnabled).toBe(true);
  });

  it("tell a subscriber about the write and the rollback", async () => {
    await loadPrivacySettings();
    const heard: boolean[] = [];
    const unsubscribe = subscribePrivacyConsent((c) => heard.push(c.crashReportingEnabled));
    const update = renderHook(() => useUpdatePrivacySettings(), { wrapper });
    refuseWrites = true;

    act(() => update.result.current.setCrashReporting(false));
    await waitFor(() => expect(heard).toContain(false));
    act(() => release?.());
    await waitFor(() => expect(heard.at(-1)).toBe(true));
    unsubscribe();

    expect(heard[0]).toBe(false);
  });
});

describe("imperative readers", () => {
  it("the entry is never garbage-collected", async () => {
    await loadPrivacySettings();
    const query = queryClient.getQueryCache().find({ queryKey: privacySettingsKey(), exact: true });
    expect(query?.gcTime).toBe(Infinity);
  });

  it("an opt-out still holds after a sign-out clears the cache", async () => {
    stored.crashReportingEnabled = false;
    await loadPrivacySettings();

    queryClient.clear();

    expect(privacyConsent().crashReportingEnabled).toBe(false);
  });
});
