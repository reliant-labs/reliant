/**
 * PresetService reads and writes share the generated key scope, so no preset
 * surface (composer, Run form, workflow page, Settings → Presets) is left
 * stale by a save:
 *
 *  - every write refetches every mounted preset list before it resolves, so
 *    a caller that selects what it just saved finds it;
 *  - setting a default refetches the mounted defaults;
 *  - a workflow change (invalidateWorkflows) reaches the preset lists,
 *    which the server matches against the workflow's inputs.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import {
  createPreset,
  deletePreset,
  setDefaultPreset,
  useDefaultPresets,
  useProjectPresets,
  useWorkflowPresets,
} from "../preset-queries";
import { presetServiceKeys } from "../preset-service-hooks_gen";
import { invalidateWorkflows } from "../workflow-queries";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

type Row = { name: string; source: string; description: string; params: Record<string, never> };
let server: Row[];
let defaults: Record<string, string>;
const calls: Record<string, number> = {};
const count = (rpc: string) => {
  calls[rpc] = (calls[rpc] ?? 0) + 1;
};

beforeEach(() => {
  server = [{ name: "careful", source: "builtin", description: "", params: {} }];
  defaults = {};
  for (const k of Object.keys(calls)) delete calls[k];
  setTransport(
    mockTransport({
      "PresetService.ListPresets": () => {
        count("ListPresets");
        return { presets: server, invalidPresets: [] };
      },
      "PresetService.ListPresetsForWorkflow": () => {
        count("ListPresetsForWorkflow");
        return { presets: server, invalidPresets: [] };
      },
      "PresetService.GetDefaultPresetsBatch": (req) => {
        count("GetDefaultPresetsBatch");
        const names = (req as { workflowNames: string[] }).workflowNames;
        return { presetsByWorkflow: Object.fromEntries(names.map((n) => [n, { presets: defaults }])) };
      },
      "PresetService.CreatePreset": (req) => {
        const r = req as { name: string };
        server = [...server, { name: r.name, source: "user", description: "", params: {} }];
        return { success: true };
      },
      "PresetService.DeletePreset": (req) => {
        server = server.filter((p) => p.name !== (req as { name: string }).name);
        return { success: true };
      },
      "PresetService.SetDefaultPreset": (req) => {
        const r = req as { groupName?: string; presetName?: string };
        defaults = { ...defaults, [r.groupName ?? ""]: r.presetName ?? "" };
        return { success: true };
      },
    }),
  );
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

const names = (view: { presets: { name: string }[] }) => view.presets.map((p) => p.name);

describe("PresetService keys", () => {
  it("creating a preset refetches every mounted list before it resolves", async () => {
    const all = renderHook(() => useProjectPresets("p1", { includeHidden: true }), { wrapper });
    const forWorkflow = renderHook(() => useWorkflowPresets("p1", "builtin://agent"), { wrapper });
    await waitFor(() => expect(names(forWorkflow.result.current)).toEqual(["careful"]));
    expect(names(all.result.current)).toEqual(["careful"]);

    await act(() => createPreset("p1", { name: "mine", params: {} }));

    // In the cache when createPreset resolves (the refetch was awaited)...
    const cached = queryClient.getQueryData<{ presets: { name: string }[] }>(
      presetServiceKeys.listPresetsForWorkflow({ projectId: "p1", workflowName: "builtin://agent" }),
    );
    expect(cached?.presets.map((p) => p.name)).toEqual(["careful", "mine"]);
    // ...and on screen.
    await waitFor(() => expect(names(all.result.current)).toEqual(["careful", "mine"]));
    expect(names(forWorkflow.result.current)).toEqual(["careful", "mine"]);
    expect(all.result.current.presets[1].source).toBe("user");
  });

  it("deleting a preset refetches the mounted lists", async () => {
    server = [...server, { name: "mine", source: "user", description: "", params: {} }];
    const all = renderHook(() => useProjectPresets("p1"), { wrapper });
    await waitFor(() => expect(names(all.result.current)).toEqual(["careful", "mine"]));

    await act(() => deletePreset("p1", "mine"));
    await waitFor(() => expect(names(all.result.current)).toEqual(["careful"]));
  });

  it("setting a default refetches the mounted defaults", async () => {
    const view = renderHook(() => useDefaultPresets("p1", "builtin://agent"), { wrapper });
    await waitFor(() => expect(calls.GetDefaultPresetsBatch).toBe(1));
    expect(view.result.current.defaults).toEqual({});

    await act(() => setDefaultPreset("p1", "builtin://agent", "", "careful"));
    await waitFor(() => expect(view.result.current.defaults).toEqual({ "": "careful" }));
  });

  it("a workflow change reaches the preset lists", async () => {
    renderHook(() => useWorkflowPresets("p1", "builtin://agent"), { wrapper });
    await waitFor(() => expect(calls.ListPresetsForWorkflow).toBe(1));

    void invalidateWorkflows();
    await waitFor(() => expect(calls.ListPresetsForWorkflow).toBe(2));
  });

  it("presetServiceKeys.all reaches lists and defaults", async () => {
    renderHook(() => useProjectPresets("p1"), { wrapper });
    renderHook(() => useDefaultPresets("p1", "builtin://agent"), { wrapper });
    await waitFor(() => expect([calls.ListPresets, calls.GetDefaultPresetsBatch]).toEqual([1, 1]));

    await queryClient.invalidateQueries({ queryKey: presetServiceKeys.all });
    expect([calls.ListPresets, calls.GetDefaultPresetsBatch]).toEqual([2, 2]);
  });
});
