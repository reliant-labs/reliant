// Copyright (c) 2025 Reliant Labs

/**
 * An automation that may run only on its organization's AI accounts, none of
 * which can run its model yet, is still created, updated, resumed or enabled:
 * the server answers with a funding warning, and the person sees it once per
 * org and model. A write with no warning shows nothing.
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const mocks = vi.hoisted(() => ({ warning: vi.fn() }));
vi.mock("../../lib/toast-manager", () => ({ toast: { warning: mocks.warning } }));

import { ScheduleSourceSchema } from "@/gen/reliant/v1/trigger_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { useCreateTrigger, useEnableWorkflow, useSetTriggerEnabled } from "../trigger-queries";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } },
});

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

const warning = {
  orgId: "acme",
  model: "claude-opus-4-6",
  message: "No account of this organization can run claude-opus-4-6 yet, so its runs will fail until an admin adds one.",
};
const wire = {
  id: "trg-1",
  enabled: true,
  name: "Nightly",
  projectId: "proj-1",
  daemonId: "daemon-1",
  source: { case: "schedule" as const, value: create(ScheduleSourceSchema, { cron: ["0 9 * * *"] }) },
};

beforeEach(() => mocks.warning.mockReset());
afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

describe("automation funding warning", () => {
  it("a create that went through with a warning shows it", async () => {
    setTransport(mockTransport({ "TriggerService.CreateTrigger": () => ({ trigger: wire, fundingWarning: warning }) }));
    const hook = renderHook(() => useCreateTrigger(), { wrapper });
    await act(async () => {
      await hook.result.current.mutateAsync({
        name: "Nightly",
        projectId: "proj-1",
        workflow: "",
        presets: {},
        params: {},
        message: "go",
        daemonId: "daemon-1",
        notifyOnComplete: false,
        source: { kind: "schedule", schedule: { cron: ["0 9 * * *"], timezone: "UTC", overlap: "skip" } },
      });
    });
    expect(mocks.warning).toHaveBeenCalledTimes(1);
    expect(mocks.warning).toHaveBeenCalledWith(warning.message, expect.objectContaining({ id: "trigger-funding:acme:claude-opus-4-6" }));
  });

  it("a resume shows the warning; a resume without one shows nothing", async () => {
    let warn = true;
    setTransport(
      mockTransport({
        "TriggerService.SetTriggerEnabled": () => ({ trigger: wire, fundingWarning: warn ? warning : undefined }),
      }),
    );
    const hook = renderHook(() => useSetTriggerEnabled(), { wrapper });
    await act(async () => {
      await hook.result.current.mutateAsync({ id: "trg-1", enabled: true });
    });
    expect(mocks.warning).toHaveBeenCalledTimes(1);

    warn = false;
    await act(async () => {
      await hook.result.current.mutateAsync({ id: "trg-1", enabled: true });
    });
    expect(mocks.warning).toHaveBeenCalledTimes(1);
  });

  it("enabling a workflow shows each trigger's warning under one id per org and model", async () => {
    setTransport(
      mockTransport({
        "TriggerService.EnableWorkflow": () => ({
          enabled: true,
          results: [
            { trigger: "nightly", fundingWarning: warning },
            { trigger: "hook", fundingWarning: warning },
            { trigger: "local" },
          ],
        }),
      }),
    );
    const hook = renderHook(() => useEnableWorkflow(), { wrapper });
    await act(async () => {
      await hook.result.current.mutateAsync({ workflow: "triage", projectId: "proj-1" });
    });
    expect(mocks.warning).toHaveBeenCalledTimes(2);
    for (const [, options] of mocks.warning.mock.calls) {
      expect(options).toEqual(expect.objectContaining({ id: "trigger-funding:acme:claude-opus-4-6" }));
    }
  });
});
