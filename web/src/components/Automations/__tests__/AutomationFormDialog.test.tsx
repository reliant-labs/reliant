// Copyright (c) 2025 Reliant Labs

/**
 * The create/edit form builds the right request.
 *
 * The RPC client is the only mock, so what is asserted is the actual
 * CreateTriggerRequest / UpdateTriggerRequest the form produced — presets
 * compiled to cron, the zone, the overlap policy — and that a server
 * InvalidArgument is shown beside the field it is about.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  CreateTriggerResponseSchema,
  ScheduleSourceSchema,
  TriggerOverlapPolicy,
  TriggerSchema,
  UpdateTriggerResponseSchema,
} from "@/gen/reliant/v1/trigger_pb";
import { jsToProtoValue } from "@/api/proto-utils";
import { DaemonInfoSchema, DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { ProjectDaemonSchema, ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import { triggerFromProto } from "@/api/trigger-grpc";
import { renderAtRoute } from "./automationTestUtils";

const createTrigger = vi.fn();
const updateTrigger = vi.fn();
const listWorkflows = vi.fn();
const listDaemons = vi.fn();
const listProjectDaemons = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    trigger: () => ({ createTrigger, updateTrigger }),
    workflow: () => ({ listWorkflows }),
    daemonRegistry: () => ({ listDaemons }),
    project: () => ({ listProjectDaemons }),
  },
}));

function daemon(daemonId: string, hostname: string, status = DaemonStatus.ACTIVE) {
  return create(DaemonInfoSchema, { daemonId, hostname, status });
}

function install(projectId: string, daemonId: string, installState = ProjectInstallState.INSTALLED) {
  return create(ProjectDaemonSchema, { projectId, daemonId, installState });
}

vi.mock("@/store/projectStore", () => {
  const snapshot = () => ({
    projects: [
      { id: "proj-1", name: "Reliant" },
      { id: "proj-2", name: "Forge" },
    ],
    currentProject: { id: "proj-2", name: "Forge" },
    loadProjects: vi.fn(async () => undefined),
  });
  const useProjectStore = Object.assign(
    (selector?: (s: ReturnType<typeof snapshot>) => unknown) => (selector ? selector(snapshot()) : snapshot()),
    { getState: snapshot },
  );
  return { useProjectStore };
});

import { AutomationFormDialog } from "../AutomationFormDialog";

/**
 * Set a field's value in one change event, as a paste would. Per-keystroke
 * user.type of long strings pushed this file past the 5s per-test budget in
 * the full parallel run; the handlers under test are onChange either way.
 */
function fill(element: HTMLElement, value: string) {
  fireEvent.change(element, { target: { value } });
}

function storedTrigger() {
  return create(TriggerSchema, {
    id: "trig-9",
    name: "Nightly deps",
    projectId: "proj-1",
    worktreeId: "wt-1",
    enabled: true,
    workflow: "builtin://agent",
    presets: { "": "fast" },
    params: { depth: jsToProtoValue(2) },
    message: "Bump dependencies",
    daemonId: "daemon-2",
    source: {
      case: "schedule",
      value: create(ScheduleSourceSchema, {
        cron: ["0 2 * * *"],
        timezone: "Europe/Berlin",
        overlap: TriggerOverlapPolicy.ALLOW,
        catchupWindow: "30m",
      }),
    },
  });
}

describe("AutomationFormDialog", () => {
  beforeEach(() => {
    createTrigger.mockReset();
    updateTrigger.mockReset();
    listDaemons.mockReset();
    listProjectDaemons.mockReset();
    listDaemons.mockResolvedValue({
      daemons: [daemon("daemon-1", "laptop"), daemon("daemon-2", "cloud-box", DaemonStatus.SUSPENDED)],
    });
    // Forge (proj-2, the current project) is installed on daemon-1 only; Reliant
    // (proj-1) is installed on both.
    listProjectDaemons.mockResolvedValue({
      projectDaemons: [install("proj-2", "daemon-1"), install("proj-1", "daemon-1"), install("proj-1", "daemon-2")],
    });
    listWorkflows.mockReset();
    listWorkflows.mockResolvedValue({
      workflows: [
        { name: "agent", source: "builtin", stepCount: 1, nodes: [], edges: [], validationErrors: [] },
        { name: "triage", source: "project", stepCount: 2, nodes: [], edges: [], validationErrors: [] },
      ],
      invalidWorkflows: [],
    });
  });

  it("builds a CreateTriggerRequest from the weekdays preset", async () => {
    const created = create(TriggerSchema, { id: "new-1", name: "Morning triage", projectId: "proj-2" });
    createTrigger.mockResolvedValue(create(CreateTriggerResponseSchema, { trigger: created }));
    const onSaved = vi.fn();
    const onClose = vi.fn();
    const user = userEvent.setup();

    renderAtRoute(<AutomationFormDialog open onClose={onClose} onSaved={onSaved} />);

    fill(await screen.findByLabelText("Name"), "Morning triage");
    // The current project is the default.
    expect(screen.getByLabelText("Project")).toHaveValue("proj-2");
    // The one daemon with Forge installed is preselected; the other is shown
    // but cannot be chosen, because the server would refuse it.
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    expect(screen.getByRole("option", { name: /laptop \(online, project installed\)/ })).toBeEnabled();
    expect(screen.getByRole("option", { name: /cloud-box \(suspended, project not installed\)/ })).toBeDisabled();
    await screen.findByRole("option", { name: "Triage" });
    await user.selectOptions(screen.getByLabelText("Workflow"), "triage");
    fill(screen.getByLabelText("Prompt"), "Triage new issues");
    // "Every weekday" is the default preset.
    expect(screen.getByLabelText("Repeat")).toHaveValue("weekdays");
    fill(screen.getByLabelText("At"), "08:30");
    fill(screen.getByLabelText("Time zone"), "America/New_York");
    expect(screen.getByText("Every weekday at 8:30 AM ET")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Create automation" }));

    await waitFor(() => expect(createTrigger).toHaveBeenCalledTimes(1));
    const definition = createTrigger.mock.calls[0]![0].trigger;
    expect(definition).toMatchObject({
      name: "Morning triage",
      projectId: "proj-2",
      workflow: "triage",
      message: "Triage new issues",
      presets: {},
      params: {},
      daemonId: "daemon-1",
    });
    expect(definition.enabled).toBeUndefined();
    expect(definition.worktreeId).toBeUndefined();
    expect(definition.source.case).toBe("schedule");
    expect(definition.source.value).toMatchObject({
      cron: ["30 8 * * 1-5"],
      timezone: "America/New_York",
      overlap: TriggerOverlapPolicy.SKIP,
    });
    expect(definition.source.value.interval).toBeUndefined();
    expect(onSaved).toHaveBeenCalledWith(expect.objectContaining({ id: "new-1" }));
    expect(onClose).toHaveBeenCalled();
  });

  it("compiles the weekly and interval presets, and the allow overlap", async () => {
    createTrigger.mockResolvedValue(
      create(CreateTriggerResponseSchema, { trigger: create(TriggerSchema, { id: "x" }) }),
    );
    const user = userEvent.setup();

    renderAtRoute(<AutomationFormDialog open onClose={vi.fn()} />);
    fill(await screen.findByLabelText("Name"), "Sweep");
    fill(screen.getByLabelText("Prompt"), "Sweep stale branches");
    await user.selectOptions(screen.getByLabelText("Repeat"), "interval");
    fill(screen.getByLabelText("Interval amount"), "2");
    await user.selectOptions(screen.getByLabelText("Interval unit"), "h");
    const allow = screen.getByRole("radio", { name: "Start another run anyway" });
    expect(allow).toHaveAccessibleDescription(/pile up/);
    await user.click(allow);
    await user.click(screen.getByRole("button", { name: "Create automation" }));

    await waitFor(() => expect(createTrigger).toHaveBeenCalledTimes(1));
    const source = createTrigger.mock.calls[0]![0].trigger.source.value;
    expect(source.cron).toEqual([]);
    expect(source.interval).toBe("2h");
    expect(source.overlap).toBe(TriggerOverlapPolicy.ALLOW);
  });

  it("blocks submit with inline errors when required fields are empty", async () => {
    const user = userEvent.setup();
    renderAtRoute(<AutomationFormDialog open onClose={vi.fn()} />);

    await user.click(await screen.findByRole("button", { name: "Create automation" }));

    expect(screen.getByText("Give the automation a name.")).toBeInTheDocument();
    expect(screen.getByText("Write the prompt each run starts from.")).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("Name")).toHaveFocus();
    expect(createTrigger).not.toHaveBeenCalled();
  });

  it("shows a server InvalidArgument beside the schedule", async () => {
    createTrigger.mockRejectedValue(
      new ConnectError("cron: invalid expression 0 9 * * 8: end of range (8) above maximum (6)", Code.InvalidArgument),
    );
    const user = userEvent.setup();

    renderAtRoute(<AutomationFormDialog open onClose={vi.fn()} />);
    fill(await screen.findByLabelText("Name"), "Bad");
    fill(screen.getByLabelText("Prompt"), "x");
    await user.selectOptions(screen.getByLabelText("Repeat"), "advanced");
    fill(screen.getByLabelText("Cron expressions"), "0 9 * * 8");
    await user.click(screen.getByRole("button", { name: "Create automation" }));

    const alert = await screen.findByText(/end of range \(8\) above maximum/);
    expect(alert).toHaveAttribute("role", "alert");
    // rawMessage: no "[invalid_argument]" prefix.
    expect(alert.textContent).toBe("cron: invalid expression 0 9 * * 8: end of range (8) above maximum (6)");
    expect(screen.getByLabelText("Cron expressions")).toHaveAttribute("aria-invalid", "true");
  });

  it("edits as a full replacement, carrying the fields the form does not show", async () => {
    const stored = storedTrigger();
    updateTrigger.mockResolvedValue(create(UpdateTriggerResponseSchema, { trigger: stored }));
    const user = userEvent.setup();

    renderAtRoute(
      <AutomationFormDialog open onClose={vi.fn()} trigger={triggerFromProto(stored)} />,
    );

    // Reopens on the preset that produced the stored cron.
    expect(await screen.findByLabelText("Repeat")).toHaveValue("daily");
    await screen.findByRole("option", { name: /cloud-box/ });
    expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-2");
    expect(screen.getByLabelText("At")).toHaveValue("02:00");
    fill(screen.getByLabelText("Name"), "Nightly dependency bump");
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(updateTrigger).toHaveBeenCalledTimes(1));
    const request = updateTrigger.mock.calls[0]![0];
    expect(request.id).toBe("trig-9");
    expect(request.trigger).toMatchObject({
      name: "Nightly dependency bump",
      projectId: "proj-1",
      worktreeId: "wt-1",
      workflow: "builtin://agent",
      presets: { "": "fast" },
      // The stored daemon round-trips untouched — even though daemon-1 would
      // be the "first" choice, an edit never re-defaults it.
      daemonId: "daemon-2",
    });
    expect(request.trigger.params.depth.kind).toEqual({ case: "numberValue", value: 2 });
    // Unset enabled means "unchanged" on update.
    expect(request.trigger.enabled).toBeUndefined();
    expect(request.trigger.source.value).toMatchObject({
      cron: ["0 2 * * *"],
      timezone: "Europe/Berlin",
      overlap: TriggerOverlapPolicy.ALLOW,
      catchupWindow: "30m",
    });
  });

  it("lets an edit change the daemon, and sends the new one", async () => {
    const stored = storedTrigger();
    updateTrigger.mockResolvedValue(create(UpdateTriggerResponseSchema, { trigger: stored }));
    const user = userEvent.setup();

    renderAtRoute(<AutomationFormDialog open onClose={vi.fn()} trigger={triggerFromProto(stored)} />);
    await screen.findByRole("option", { name: /laptop/ });
    await user.selectOptions(screen.getByLabelText("Runs on"), "daemon-1");
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(updateTrigger).toHaveBeenCalledTimes(1));
    expect(updateTrigger.mock.calls[0]![0].trigger.daemonId).toBe("daemon-1");
  });

  it("blocks submit and explains when the user has no daemon", async () => {
    listDaemons.mockResolvedValue({ daemons: [] });
    listProjectDaemons.mockResolvedValue({ projectDaemons: [] });
    const user = userEvent.setup();

    renderAtRoute(<AutomationFormDialog open onClose={vi.fn()} />);
    fill(await screen.findByLabelText("Name"), "Morning triage");
    fill(screen.getByLabelText("Prompt"), "Triage new issues");
    expect(await screen.findByText(/You have no daemon yet/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create automation" }));

    expect(
      await screen.findByText("Connect a daemon first — automations run on one of your daemons."),
    ).toBeInTheDocument();
    expect(createTrigger).not.toHaveBeenCalled();
  });

  it("blocks submit when several daemons fit and none is chosen", async () => {
    listProjectDaemons.mockResolvedValue({ projectDaemons: [] }); // not tracked: both eligible
    const user = userEvent.setup();

    renderAtRoute(<AutomationFormDialog open onClose={vi.fn()} />);
    fill(await screen.findByLabelText("Name"), "Morning triage");
    fill(screen.getByLabelText("Prompt"), "Triage new issues");
    await screen.findByRole("option", { name: /cloud-box/ });
    // Two equally valid daemons: no guess.
    expect(screen.getByLabelText("Runs on")).toHaveValue("");
    await user.click(screen.getByRole("button", { name: "Create automation" }));

    expect(await screen.findByText("Choose the daemon this automation runs on.")).toBeInTheDocument();
    expect(screen.getByLabelText("Runs on")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("Runs on")).toHaveFocus();
    expect(createTrigger).not.toHaveBeenCalled();
  });

  it("shows the server's daemon rejection beside the daemon field", async () => {
    createTrigger.mockRejectedValue(
      new ConnectError("project is not installed on that daemon", Code.FailedPrecondition),
    );
    const user = userEvent.setup();

    renderAtRoute(<AutomationFormDialog open onClose={vi.fn()} />);
    fill(await screen.findByLabelText("Name"), "Morning triage");
    fill(screen.getByLabelText("Prompt"), "Triage new issues");
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));
    await user.click(screen.getByRole("button", { name: "Create automation" }));

    expect(await screen.findByText("project is not installed on that daemon")).toBeInTheDocument();
    expect(screen.getByLabelText("Runs on")).toHaveAttribute("aria-invalid", "true");
  });
});
