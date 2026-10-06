/**
 * Editing a declared trigger edits the workflow definition: every change is
 * a whole-trigger updateTrigger through WorkflowMutationContext (the
 * builder's mutation path), in the proto shape the YAML codec writes.
 */

import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";

vi.mock("../../../../lib/monacoManager", () => ({ useMonaco: () => null }));

import {
  CatalogEntryKind,
  CatalogEntrySchema,
  CatalogEntrySummarySchema,
  CatalogIntegrationSchema,
  GetCatalogEntryResponseSchema,
  SearchCatalogResponseSchema,
} from "@/gen/reliant/v1/catalog_pb";
import { renderWithQuery } from "@/test/renderWithQuery";

const searchCatalog = vi.fn();
const getCatalogEntry = vi.fn();
const setTriggerEnabled = vi.fn();
vi.mock("@/api/grpc-client", () => ({
  grpcClient: { catalog: () => ({ searchCatalog, getCatalogEntry }), trigger: () => ({ setTriggerEnabled }) },
  getGRPCBaseURLPublic: () => null,
}));

import { DeclaredTriggerPanel } from "../DeclaredTriggerPanel";
import { WorkflowMutationProvider } from "../../WorkflowMutationContext";
import type { Workflow } from "@/types/workflow";
import type { DeclaredTrigger } from "@/lib/declaredTriggers";
import type { Trigger } from "@/api/trigger-grpc";
import { SetTriggerEnabledResponseSchema, TriggerSchema } from "@/gen/reliant/v1/trigger_pb";

const issueTrigger = {
  name: "new-issue",
  description: "",
  filter: "",
  inputs: {},
  source: { case: "integration", value: { integration: "github", events: ["issues.opened"], match: {}, pollInterval: "" } },
} as unknown as DeclaredTrigger;

const scheduleTrigger = {
  name: "nightly",
  description: "",
  filter: "",
  inputs: {},
  source: { case: "schedule", value: { cron: ["0 9 * * 1-5"], timezone: "UTC" } },
} as unknown as DeclaredTrigger;

function issueEntry() {
  return create(GetCatalogEntryResponseSchema, {
    entry: create(CatalogEntrySchema, {
      summary: create(CatalogEntrySummarySchema, {
        ref: "github/issue.opened@1",
        kind: CatalogEntryKind.TRIGGER,
        id: "issue.opened",
        displayName: "Issue opened",
        integration: create(CatalogIntegrationSchema, { id: "github", displayName: "GitHub", icon: "github" }),
      }),
      payloadSchema: {
        type: "object",
        properties: {
          event: { type: "string", enum: ["issues.opened", "issues.reopened"] },
          attributes: { type: "object", properties: { repository: { type: "string", description: "owner/repo", examples: ["acme/app"] } } },
          data: { type: "object", properties: { issue: { type: "object", properties: { number: { type: "integer" } } } } },
        },
      },
    }),
  });
}

/** The real mutation provider over real workflow state, as the builder mounts it. */
function Harness({
  initial,
  onWorkflow,
  index = 0,
  unsaved = false,
  readOnly = false,
  activations = [],
}: {
  initial: DeclaredTrigger[];
  onWorkflow: (w: Workflow) => void;
  index?: number;
  unsaved?: boolean;
  readOnly?: boolean;
  activations?: Trigger[];
}) {
  const [workflow, setWorkflow] = useState<Workflow>({ name: "triage", inputs: { issue_number: { type: "integer" } }, triggers: initial } as Workflow);
  const [dirty, setDirty] = useState(false);
  const triggers = (workflow.triggers ?? []) as DeclaredTrigger[];
  return (
    <WorkflowMutationProvider
      nodes={[]}
      edges={[]}
      setNodes={() => undefined}
      setEdges={() => undefined}
      setHasModifications={setDirty}
      takeSnapshot={() => undefined}
      setSelectedNodeId={() => undefined}
      setSelectedEdgeId={() => undefined}
      setWorkflow={(updater) =>
        setWorkflow((w) => {
          const next = updater(w);
          onWorkflow(next);
          return next;
        })
      }
    >
      <span data-testid="dirty">{String(dirty)}</span>
      {triggers[index] && (
        <DeclaredTriggerPanel
          index={index}
          trigger={triggers[index]!}
          allTriggers={triggers}
          inputs={workflow.inputs}
          catalogRef={triggers[index]!.source?.case === "integration" ? "github/issue.opened@1" : undefined}
          findings={index === 0 && triggers[0]!.source?.case === "schedule" ? [{ field: "schedule.cron", message: '"0 25 * * *" is not a valid cron expression' }] : []}
          activations={activations}
          isReadOnly={readOnly}
          canActivate
          unsaved={unsaved}
          onClose={() => undefined}
          onActivate={() => undefined}
          onEditActivation={() => undefined}
        />
      )}
    </WorkflowMutationProvider>
  );
}

function renderPanel(initial: DeclaredTrigger[], opts: { unsaved?: boolean; readOnly?: boolean; activations?: Trigger[] } = {}) {
  let latest: Workflow | undefined;
  renderWithQuery(
    <Harness initial={initial} onWorkflow={(w) => (latest = w)} unsaved={opts.unsaved} readOnly={opts.readOnly} activations={opts.activations} />,
  );
  return { latest: () => latest?.triggers as DeclaredTrigger[] | undefined };
}

beforeEach(() => {
  searchCatalog.mockReset();
  getCatalogEntry.mockReset();
  getCatalogEntry.mockResolvedValue(issueEntry());
  searchCatalog.mockResolvedValue(create(SearchCatalogResponseSchema, { entries: [] }));
});

describe("DeclaredTriggerPanel", () => {
  it("edits an integration trigger's events, match, filter and inputs into the definition", async () => {
    const user = userEvent.setup();
    const { latest } = renderPanel([issueTrigger]);

    // Events come from the trigger type's payload schema.
    const events = await screen.findByRole("group", { name: "Events" });
    await user.click(within(events).getByRole("button", { name: "issues.reopened" }));
    expect(latest()![0]!.source).toMatchObject({ case: "integration", value: { events: ["issues.opened", "issues.reopened"] } });

    const repo = screen.getByLabelText("repository");
    expect(repo).toHaveAttribute("placeholder", "acme/app");
    fireEvent.change(repo, { target: { value: "acme/app" } });
    fireEvent.blur(repo);
    expect(latest()![0]!.source).toMatchObject({ value: { match: { repository: "acme/app" } } });

    // CELInput renders its plain fallback under jsdom (no Monaco).
    const inputs = screen.getAllByRole("textbox");
    const filter = inputs.find((el) => el.getAttribute("placeholder")?.startsWith("trigger.payload.data.issue.user.login"))!;
    fireEvent.change(filter, { target: { value: "trigger.payload.data.issue.number > 0" } });
    expect(latest()![0]!.filter).toBe("trigger.payload.data.issue.number > 0");

    const mapping = inputs.find((el) => el.getAttribute("placeholder") === "{{ trigger.payload.data… }}")!;
    fireEvent.change(mapping, { target: { value: "{{ trigger.payload.data.issue.number }}" } });
    expect(latest()![0]!.inputs).toEqual({ issue_number: "{{ trigger.payload.data.issue.number }}" });
    expect(screen.getByTestId("dirty")).toHaveTextContent("true");
  });

  it("shows server findings on their field, and offers no filter for a schedule", async () => {
    renderPanel([scheduleTrigger]);
    expect(await screen.findByText('"0 25 * * *" is not a valid cron expression')).toBeInTheDocument();
    expect(screen.queryByText("Filter")).not.toBeInTheDocument();
    expect(screen.getByText("Every weekday at 9:00 AM UTC")).toBeInTheDocument();
  });

  it("validates the name as the server does, and renames on blur", async () => {
    const { latest } = renderPanel([issueTrigger, scheduleTrigger]);
    const name = await screen.findByLabelText("Name");
    fireEvent.change(name, { target: { value: "nightly" } });
    expect(screen.getByText("Another trigger already has this name.")).toBeInTheDocument();
    fireEvent.change(name, { target: { value: "Bad Name" } });
    expect(screen.getByText(/Use lowercase letters/)).toBeInTheDocument();
    fireEvent.change(name, { target: { value: "opened" } });
    fireEvent.blur(name);
    expect(latest()![0]!.name).toBe("opened");
  });

  it("blocks activating a trigger with unsaved edits", async () => {
    const user = userEvent.setup();
    renderPanel([issueTrigger], { unsaved: true });
    await user.click(await screen.findByRole("button", { name: "Activations" }));
    expect(await screen.findByRole("button", { name: "Activate" })).toBeDisabled();
    expect(screen.getByText(/Save the workflow first/)).toBeInTheDocument();
  });

  it("removes the trigger from the definition", async () => {
    const user = userEvent.setup();
    const { latest } = renderPanel([issueTrigger, scheduleTrigger]);
    await user.click(await screen.findByRole("button", { name: /Remove trigger/ }));
    expect(latest()!.map((t) => t.name)).toEqual(["nightly"]);
  });

  it("writes the prompt template into the definition", async () => {
    const { latest } = renderPanel([issueTrigger]);
    await screen.findByRole("group", { name: "Events" });
    const prompt = screen.getAllByRole("textbox").find((el) => el.getAttribute("placeholder")?.startsWith("Triage issue #"))!;
    fireEvent.change(prompt, { target: { value: "Triage #{{ trigger.payload.data.issue.number }}" } });
    expect(latest()![0]!).toMatchObject({ prompt: "Triage #{{ trigger.payload.data.issue.number }}" });
  });

  it("on a built-in, opens on your activations and keeps the definition read-only", async () => {
    const user = userEvent.setup();
    const activation = {
      id: "a-1",
      name: "Agent · nightly",
      enabled: true,
      projectName: "Reliant",
      noMachine: true,
      health: { status: "healthy", consecutiveFailures: 0, consecutiveSkips: 0, lastFailureDetail: "" },
      source: { kind: "activation", workflowTrigger: "nightly" },
    } as unknown as Trigger;
    setTriggerEnabled.mockResolvedValue(create(SetTriggerEnabledResponseSchema, { trigger: create(TriggerSchema, { id: "a-1", enabled: false }) }));
    renderPanel([scheduleTrigger], { readOnly: true, activations: [activation] });

    const list = await screen.findByRole("list", { name: "Activations of nightly" });
    expect(within(list).getByText("Reliant · No machine")).toBeInTheDocument();
    await user.click(within(list).getByRole("switch", { name: "Agent · nightly enabled" }));
    expect(setTriggerEnabled).toHaveBeenCalledWith(expect.objectContaining({ id: "a-1", enabled: false }));
    expect(screen.queryByRole("button", { name: /Remove trigger/ })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Definition" }));
    expect(screen.getByText(/Built-in: this definition can't change/)).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toBeDisabled();
  });
});
