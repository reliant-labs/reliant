// Copyright (c) 2025 Reliant Labs

/**
 * ScenarioPanel is ScenarioService on forge's generated hooks. The list is
 * `useListScenarios`; every write is a generated mutation invalidating
 * `scenarioServiceKeys.all`, so the panel never reloads by hand:
 *
 *   1. a run refetches the list, and the row shows the new status;
 *   2. a delete refetches the list, and the row is gone;
 *   3. an expanded scenario's YAML is `useExportScenario`.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen, waitFor } from "@testing-library/react";
import type { QueryClient } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";

import { setTransport } from "@/lib/connect";
import { mockTransport, renderWithTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { toast } from "sonner";
import { ScenarioPanel } from "../ScenarioPanel";

interface Row {
  id: string;
  name: string;
  lastRunStatus: string;
}

let rows: Row[];
let listCalls: number;
let clients: QueryClient[];

function transport() {
  return mockTransport(
    serviceHandlers("ScenarioService", {
      listScenarios: (req: { projectId: string; workflowSlug: string }) => {
        listCalls++;
        expect(req).toMatchObject({ projectId: "proj-1", workflowSlug: "wf" });
        return { scenarios: rows, scenariosDir: "/repo/.reliant/scenarios/wf" };
      },
      runScenario: (req: { scenarioId: string }) => {
        rows = rows.map((r) => (r.id === req.scenarioId ? { ...r, lastRunStatus: "passed" } : r));
        return { result: { status: "passed" } };
      },
      deleteScenario: (req: { scenarioId: string }) => {
        rows = rows.filter((r) => r.id !== req.scenarioId);
        return {};
      },
      uploadScenario: (req: { filename: string; yamlContent: string }) => {
        expect(req).toMatchObject({ filename: "uploaded", yamlContent: "name: uploaded\n" });
        rows.push({ id: "s-2", name: "uploaded", lastRunStatus: "" });
        return { success: true, path: "/repo/.reliant/scenarios/wf/uploaded.yaml" };
      },
      exportScenario: (req: { scenarioId: string }) => ({
        yamlContent: `name: ${req.scenarioId}\nstatus: ${rows.find((r) => r.id === req.scenarioId)?.lastRunStatus}\n`,
        filename: `${req.scenarioId}.yaml`,
      }),
    }),
  );
}

beforeEach(() => {
  rows = [{ id: "s-1", name: "happy path", lastRunStatus: "" }];
  listCalls = 0;
  clients = [];
  vi.mocked(toast.success).mockClear();
});

afterEach(() => {
  cleanup();
  clients.forEach((client) => client.clear());
  setTransport(null);
  vi.restoreAllMocks();
});

function mountPanel() {
  const view = renderWithTransport(<ScenarioPanel projectId="proj-1" workflowSlug="wf" />, { transport: transport() });
  clients.push(view.queryClient);
  return view;
}

describe("ScenarioPanel", () => {
  it("a run refetches the list and shows the new status", async () => {
    mountPanel();
    await screen.findByText("happy path");
    expect(screen.getByText("Not run")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Run test happy path" }));

    await waitFor(() => expect(screen.getByText("Passed")).toBeInTheDocument());
    expect(listCalls).toBe(2);
    expect(toast.success).toHaveBeenCalledWith("Test passed");
  });

  it("a delete refetches the list and the row is gone", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    mountPanel();
    await screen.findByText("happy path");

    await userEvent.click(screen.getByRole("button", { name: "Delete test happy path" }));

    await screen.findByText("No tests yet");
    expect(listCalls).toBe(2);
    expect(toast.success).toHaveBeenCalledWith("Test deleted");
  });

  it("an upload refetches the list", async () => {
    const view = mountPanel();
    await screen.findByText("happy path");
    // The existing picker is a hidden input, activated by the Upload button.
    const picker = view.container.querySelector('input[type="file"]') as HTMLInputElement;
    const file = new File(["name: uploaded\n"], "uploaded.yaml", { type: "application/x-yaml" });
    // jsdom's File lacks the browser's Blob.text().
    Object.defineProperty(file, "text", { value: async () => "name: uploaded\n" });
    await userEvent.upload(picker, file);

    await screen.findByText("uploaded");
    expect(listCalls).toBe(2);
    expect(toast.success).toHaveBeenCalledWith("Test uploaded", expect.anything());
  });

  it("a run also refreshes an expanded scenario's exported YAML", async () => {
    mountPanel();
    await userEvent.click(await screen.findByText("happy path"));
    await userEvent.click(screen.getByRole("button", { name: "YAML" }));
    expect(await screen.findByText(/name: s-1/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Run test happy path" }));

    expect(await screen.findByText(/status: passed/)).toBeInTheDocument();
  });
});
