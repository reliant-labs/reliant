// Copyright (c) 2025 Reliant Labs

/**
 * The three preset modals, in their new home (components/workflow/presets/,
 * extracted from the retired WorkflowHub without behaviour change; WORKFLOW_UI.md
 * §14.1 decision 7). Pinned at the RPC each one sends, which is the behaviour
 * a host depends on:
 *
 *   - PresetConfigModal reads a workflow's preset groups and saves one
 *     default per group with SetDefaultPreset;
 *   - PresetViewModal copies a built-in into a new preset with CreatePreset;
 *   - PresetEditModal saves a renamed user preset with UpdatePreset;
 *   - PresetList deletes a user preset with DeletePreset after a confirm.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

import type { Preset } from "@/store/globalDataStore";
import { getWorkflowByName } from "../../run/__tests__/runFormFixtures";

const mocks = vi.hoisted(() => ({
  getWorkflow: vi.fn(),
  listWorkflows: vi.fn(),
  getDefaultPresetsBatch: vi.fn(),
  setDefaultPreset: vi.fn(),
  createPreset: vi.fn(),
  updatePreset: vi.fn(),
  deletePreset: vi.fn(),
}));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    workflow: () => ({ getWorkflow: mocks.getWorkflow, listWorkflows: mocks.listWorkflows }),
    preset: () => ({
      getDefaultPresetsBatch: mocks.getDefaultPresetsBatch,
      setDefaultPreset: mocks.setDefaultPreset,
      createPreset: mocks.createPreset,
      updatePreset: mocks.updatePreset,
      deletePreset: mocks.deletePreset,
    }),
  },
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }));

import { usePreferencesStore } from "@/store/preferencesStore";
import { PresetConfigModal } from "../PresetConfigModal";
import { PresetEditModal } from "../PresetEditModal";
import { PresetList } from "../PresetList";
import { PresetViewModal } from "../PresetViewModal";

function wrap(ui: ReactNode) {
  return render(<QueryClientProvider client={new QueryClient()}>{ui}</QueryClientProvider>);
}

const careful: Preset = {
  name: "careful",
  description: "Look harder",
  source: "user",
  tag: "agent",
  params: { depth: 5 },
} as Preset;

const builtinPreset: Preset = {
  name: "fast",
  description: "Quick and cheap",
  source: "builtin",
  tag: "agent",
  params: { depth: 1 },
} as Preset;

beforeEach(() => {
  for (const mock of Object.values(mocks)) mock.mockReset();
  mocks.getWorkflow.mockImplementation(async (request: { name: string }) => getWorkflowByName(request));
  mocks.listWorkflows.mockResolvedValue({ workflows: [], invalidWorkflows: [] });
  mocks.getDefaultPresetsBatch.mockResolvedValue({ presetsByWorkflow: {} });
  mocks.setDefaultPreset.mockResolvedValue({ success: true });
  mocks.createPreset.mockResolvedValue({ success: true });
  mocks.updatePreset.mockResolvedValue({ success: true });
  mocks.deletePreset.mockResolvedValue({ success: true });
  // The modals read preferences; a loaded, empty set keeps them off the network.
  usePreferencesStore.setState({
    preferences: {} as never,
    loadPreferences: vi.fn(async () => undefined),
    isPresetHidden: () => false,
    togglePresetVisibility: vi.fn(async () => undefined),
  } as never);
});

describe("PresetConfigModal", () => {
  it("offers one picker per preset group and saves a default per group", async () => {
    const onSave = vi.fn();
    const onClose = vi.fn();
    const strict = { ...careful, name: "strict", tag: "review" } as Preset;
    wrap(
      <PresetConfigModal
        workflowName="triage"
        projectId="proj-1"
        availablePresets={[careful, strict]}
        onSave={onSave}
        onClose={onClose}
      />,
    );
    // The modal's labels are not bound to their selects (unchanged from the
    // hub), so the pickers are found in group order: top-level, then review.
    await screen.findByText("Top-level");
    const [topLevel, review] = screen.getAllByRole("combobox");
    if (!topLevel || !review) throw new Error("expected two group pickers");
    // Each group only offers the presets carrying its tag.
    expect(within(topLevel).getByRole("option", { name: "careful (user)" })).toBeInTheDocument();
    expect(within(topLevel).queryByRole("option", { name: "strict (user)" })).toBeNull();

    await userEvent.selectOptions(topLevel, "careful");
    await userEvent.selectOptions(review, "strict");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(onSave).toHaveBeenCalled();
    expect(mocks.setDefaultPreset).toHaveBeenCalledWith(
      expect.objectContaining({ projectId: "proj-1", workflowName: "triage", presetName: "careful" }),
    );
    expect(mocks.setDefaultPreset).toHaveBeenCalledWith(
      expect.objectContaining({ workflowName: "triage", groupName: "review", presetName: "strict" }),
    );
  });
});

describe("PresetViewModal", () => {
  it("copies a built-in preset into a new one with its params and tag", async () => {
    const onCopy = vi.fn();
    const onClose = vi.fn();
    wrap(<PresetViewModal preset={builtinPreset} projectId="proj-1" onCopy={onCopy} onClose={onClose} />);
    const name = screen.getByPlaceholderText("New preset name");
    expect(name).toHaveValue("my-fast");
    await userEvent.click(screen.getByRole("button", { name: "Create Copy" }));
    await waitFor(() => expect(onCopy).toHaveBeenCalled());
    expect(onClose).toHaveBeenCalled();
    expect(mocks.createPreset).toHaveBeenCalledWith(
      expect.objectContaining({ projectId: "proj-1", name: "my-fast", tag: "agent", description: "Quick and cheap" }),
    );
  });
});

describe("PresetEditModal", () => {
  it("saves a renamed user preset", async () => {
    const onSave = vi.fn();
    const onClose = vi.fn();
    wrap(<PresetEditModal preset={careful} projectId="proj-1" onSave={onSave} onClose={onClose} />);
    const name = screen.getByDisplayValue("careful");
    await userEvent.clear(name);
    await userEvent.type(name, "very-careful");
    await userEvent.click(screen.getByRole("button", { name: "Save Changes" }));
    await waitFor(() => expect(onSave).toHaveBeenCalled());
    expect(onClose).toHaveBeenCalled();
    expect(mocks.updatePreset).toHaveBeenCalledWith(
      expect.objectContaining({ projectId: "proj-1", name: "careful", newName: "very-careful" }),
    );
  });
});

describe("PresetList", () => {
  it("deletes a user preset after confirming, and offers no delete for a built-in", async () => {
    const onChanged = vi.fn();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    wrap(<PresetList projectId="proj-1" presets={[careful, builtinPreset]} onChanged={onChanged} label="Presets" />);

    await userEvent.click(screen.getByRole("button", { name: "More actions for careful" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    expect(mocks.deletePreset).toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-1", name: "careful" }));

    await userEvent.click(screen.getByRole("button", { name: "More actions for fast" }));
    const menu = await screen.findByRole("menu", { name: "More actions for fast" });
    expect(within(menu).queryByRole("menuitem", { name: "Delete" })).toBeNull();
    expect(within(menu).getByRole("menuitem", { name: "Copy to new preset" })).toBeInTheDocument();
  });

  it("opens a preset's view when its name is clicked", async () => {
    wrap(<PresetList projectId="proj-1" presets={[careful]} onChanged={vi.fn()} label="Presets" />);
    await userEvent.click(screen.getByRole("button", { name: /^careful/ }));
    expect(await screen.findByRole("dialog", { name: "careful" })).toBeInTheDocument();
  });
});
