/**
 * Default presets are asked per WORKFLOW but fetched in ONE
 * GetDefaultPresetsBatch per project per tick, and cached per workflow under
 * the generated presetServiceKeys.getDefaultPreset key.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { fetchDefaultPresets } from "../preset-queries";
import { presetServiceKeys } from "../preset-service-hooks_gen";

const mocks = vi.hoisted(() => ({
  getDefaultPreset: vi.fn(),
  getDefaultPresetsBatch: vi.fn(),
}));

beforeEach(() => {
  mocks.getDefaultPreset.mockReset();
  mocks.getDefaultPresetsBatch.mockReset();
  mocks.getDefaultPresetsBatch.mockResolvedValue({ presetsByWorkflow: {} });
  setTransport(
    mockTransport({
      "PresetService.GetDefaultPreset": (req) => mocks.getDefaultPreset(req),
      "PresetService.GetDefaultPresetsBatch": (req) => mocks.getDefaultPresetsBatch(req),
    }),
  );
});

afterEach(() => {
  queryClient.clear();
  setTransport(null);
});

describe("default-preset batching", () => {
  it("collapses N concurrent per-workflow lookups into ONE batch request", async () => {
    mocks.getDefaultPresetsBatch.mockResolvedValue({
      presetsByWorkflow: {
        "agent-a": { presets: { "": "researcher" } },
        "agent-b": { presets: { Critic: "careful" } },
        // agent-c intentionally absent: a workflow with no defaults is omitted.
      },
    });

    // Six different agents on screen, all asking in the same tick.
    const workflows = ["agent-a", "agent-b", "agent-c", "agent-d", "agent-e", "agent-f"];
    const results = await Promise.all(workflows.map((name) => fetchDefaultPresets("project-1", name)));

    // The whole point: one request, not six.
    expect(mocks.getDefaultPresetsBatch).toHaveBeenCalledTimes(1);
    // And the per-workflow RPC is not used at all.
    expect(mocks.getDefaultPreset).not.toHaveBeenCalled();

    const request = mocks.getDefaultPresetsBatch.mock.calls[0][0];
    expect(request.projectId).toBe("project-1");
    expect([...request.workflowNames].sort()).toEqual([...workflows].sort());

    // Each caller still gets exactly its own workflow's defaults back.
    expect(results[0]).toEqual({ "": "researcher" });
    expect(results[1]).toEqual({ Critic: "careful" });
    // Omitted from the response => "no defaults", same as the single RPC gives.
    expect(results[2]).toEqual({});

    // Cached per workflow, in GetDefaultPreset's own shape.
    expect(
      queryClient.getQueryData(presetServiceKeys.getDefaultPreset({ projectId: "project-1", workflowName: "agent-b" })),
    ).toMatchObject({ presets: { Critic: "careful" } });
  });

  it("asks once for repeated workflow names, and serves the next ask from cache", async () => {
    await Promise.all([
      fetchDefaultPresets("project-1", "agent-a"),
      fetchDefaultPresets("project-1", "agent-a"),
      fetchDefaultPresets("project-1", "agent-b"),
    ]);
    await fetchDefaultPresets("project-1", "agent-a");

    expect(mocks.getDefaultPresetsBatch).toHaveBeenCalledTimes(1);
    expect([...mocks.getDefaultPresetsBatch.mock.calls[0][0].workflowNames].sort()).toEqual(["agent-a", "agent-b"]);
  });

  it("keeps separate projects in separate batches", async () => {
    await Promise.all([fetchDefaultPresets("project-1", "agent-a"), fetchDefaultPresets("project-2", "agent-a")]);

    expect(mocks.getDefaultPresetsBatch).toHaveBeenCalledTimes(2);
  });

  it("resolves every caller with {} when the batch request fails", async () => {
    mocks.getDefaultPresetsBatch.mockRejectedValue(new Error("network down"));
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);

    const results = await Promise.all([
      fetchDefaultPresets("project-1", "agent-a"),
      fetchDefaultPresets("project-1", "agent-b"),
    ]);

    // Graceful degradation: callers treat presets as absent, never throw.
    expect(results).toEqual([{}, {}]);
    warn.mockRestore();
  });
});
