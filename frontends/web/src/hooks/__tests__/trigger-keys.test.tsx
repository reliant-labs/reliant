// Copyright (c) 2025 Reliant Labs

/**
 * TriggerService reads and writes share the generated key scope. A write
 * (create, enable, delete) refetches the mounted trigger reads; the enable
 * switch flips before the server answers and flips back when it refuses; and
 * every trigger read the Automations surface mounts — the list, one
 * automation's detail, its history — sits under `triggerServiceKeys.all`, the
 * one key the update stream invalidates.
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { ScheduleSourceSchema } from "@/gen/reliant/v1/trigger_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { useCreateTrigger, useDeleteTrigger, useSetTriggerEnabled, useTrigger, useTriggerEvents, useTriggers } from "../trigger-queries";
import { triggerServiceKeys } from "../trigger-service-hooks_gen";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } },
});

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

/** A server holding one scheduled automation, "trg-1", that the writes change. */
function triggerTransport(options: { refuseToggle?: boolean } = {}) {
  let triggers = [{ id: "trg-1", enabled: true, name: "Nightly" }];
  const calls: Record<string, number> = {};
  const count = (name: string) => (calls[name] = (calls[name] ?? 0) + 1);
  const wire = (t: { id: string; enabled: boolean; name: string }) => ({
    ...t,
    projectId: "proj-1",
    daemonId: "daemon-1",
    source: { case: "schedule" as const, value: create(ScheduleSourceSchema, { cron: ["0 9 * * *"] }) },
  });
  let releaseToggle: () => void = () => {};
  const transport = mockTransport({
    "TriggerService.ListTriggers": () => {
      count("ListTriggers");
      return { triggers: triggers.map(wire) };
    },
    "TriggerService.GetTrigger": () => {
      count("GetTrigger");
      return { trigger: wire(triggers[0]!) };
    },
    "TriggerService.ListTriggerEvents": () => {
      count("ListTriggerEvents");
      return { events: [] };
    },
    "TriggerService.SetTriggerEnabled": async (request: unknown) => {
      count("SetTriggerEnabled");
      const { id, enabled } = request as { id: string; enabled: boolean };
      await new Promise<void>((resolve) => (releaseToggle = resolve));
      if (options.refuseToggle) throw new ConnectError("no machine", Code.FailedPrecondition);
      triggers = triggers.map((t) => (t.id === id ? { ...t, enabled } : t));
      return { trigger: wire(triggers.find((t) => t.id === id)!) };
    },
    "TriggerService.DeleteTrigger": (request: unknown) => {
      count("DeleteTrigger");
      triggers = triggers.filter((t) => t.id !== (request as { id: string }).id);
      return {};
    },
    "TriggerService.CreateTrigger": () => {
      count("CreateTrigger");
      const created = { id: "trg-2", enabled: true, name: "Weekly" };
      triggers = [...triggers, created];
      return { trigger: wire(created) };
    },
  });
  return { transport, calls: (name: string) => calls[name] ?? 0, release: () => releaseToggle() };
}

describe("TriggerService keys", () => {
  it("pausing flips the mounted list at once, then refetches it", async () => {
    const { transport, calls, release } = triggerTransport();
    setTransport(transport);

    const list = renderHook(() => useTriggers(), { wrapper });
    await waitFor(() => expect(list.result.current.data?.[0]?.enabled).toBe(true));

    const toggle = renderHook(() => useSetTriggerEnabled(), { wrapper });
    act(() => toggle.result.current.mutate({ id: "trg-1", enabled: false }));

    // Optimistic: the server has not answered yet.
    await waitFor(() => expect(list.result.current.data?.[0]?.enabled).toBe(false));
    expect(calls("ListTriggers")).toBe(1);

    await waitFor(() => expect(calls("SetTriggerEnabled")).toBe(1));
    act(() => release());
    await waitFor(() => expect(calls("ListTriggers")).toBe(2));
    expect(list.result.current.data?.[0]?.enabled).toBe(false);
  });

  it("a refused toggle flips back and reports the error to the caller", async () => {
    const { transport, calls, release } = triggerTransport({ refuseToggle: true });
    setTransport(transport);

    const list = renderHook(() => useTriggers(), { wrapper });
    const detail = renderHook(() => useTrigger("trg-1"), { wrapper });
    await waitFor(() => {
      expect(list.result.current.data?.[0]?.enabled).toBe(true);
      expect(detail.result.current.data?.enabled).toBe(true);
    });

    const toggle = renderHook(() => useSetTriggerEnabled(), { wrapper });
    const errors: Error[] = [];
    act(() => toggle.result.current.mutate({ id: "trg-1", enabled: false }, { onError: (error) => errors.push(error) }));
    await waitFor(() => {
      expect(list.result.current.data?.[0]?.enabled).toBe(false);
      expect(detail.result.current.data?.enabled).toBe(false);
    });

    await waitFor(() => expect(calls("SetTriggerEnabled")).toBe(1));
    act(() => release());
    await waitFor(() => expect(errors).toHaveLength(1));
    expect(list.result.current.data?.[0]?.enabled).toBe(true);
    expect(detail.result.current.data?.enabled).toBe(true);
  });

  it("deleting drops the row and refetches the list", async () => {
    const { transport, calls } = triggerTransport();
    setTransport(transport);

    const list = renderHook(() => useTriggers(), { wrapper });
    await waitFor(() => expect(list.result.current.data).toHaveLength(1));

    const remove = renderHook(() => useDeleteTrigger(), { wrapper });
    await act(async () => {
      await remove.result.current.mutateAsync({ id: "trg-1" });
    });

    await waitFor(() => expect(calls("ListTriggers")).toBe(2));
    expect(list.result.current.data).toHaveLength(0);
    expect(calls("DeleteTrigger")).toBe(1);
  });

  it("creating returns the frontend model and refetches the list", async () => {
    const { transport, calls } = triggerTransport();
    setTransport(transport);

    const list = renderHook(() => useTriggers(), { wrapper });
    await waitFor(() => expect(list.result.current.data).toHaveLength(1));

    const createHook = renderHook(() => useCreateTrigger(), { wrapper });
    let createdName = "";
    await act(async () => {
      const { trigger } = await createHook.result.current.mutateAsync({
        name: "Weekly",
        projectId: "proj-1",
        workflow: "",
        presets: {},
        params: {},
        message: "go",
        daemonId: "daemon-1",
        notifyOnComplete: false,
        source: { kind: "schedule", schedule: { cron: ["0 9 * * 1"], timezone: "UTC", overlap: "skip" } },
      });
      createdName = trigger.name;
    });

    expect(createdName).toBe("Weekly");
    await waitFor(() => expect(list.result.current.data).toHaveLength(2));
    expect(calls("CreateTrigger")).toBe(1);
    expect(calls("ListTriggers")).toBe(2);
  });

  it("invalidating triggerServiceKeys.all — what the update stream does — refetches every trigger read", async () => {
    const { transport, calls } = triggerTransport();
    setTransport(transport);

    const list = renderHook(() => useTriggers(), { wrapper });
    const detail = renderHook(() => useTrigger("trg-1"), { wrapper });
    const events = renderHook(() => useTriggerEvents("trg-1"), { wrapper });
    await waitFor(() => {
      expect(list.result.current.data).toHaveLength(1);
      expect(detail.result.current.data?.id).toBe("trg-1");
      expect(events.result.current.data).toEqual([]);
    });

    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: triggerServiceKeys.all });
    });

    expect(calls("ListTriggers")).toBe(2);
    expect(calls("GetTrigger")).toBe(2);
    expect(calls("ListTriggerEvents")).toBe(2);
  });
});
