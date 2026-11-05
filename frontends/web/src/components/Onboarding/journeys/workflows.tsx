/**
 * The "Use AI workflows" journey (journeys-spec.md §2): the user builds
 * "Fix my PR" in a draft of their own — an agent reviews a pull request, a
 * loop fixes it until the tests pass, a branch merges or reports — then
 * gives it a schedule, sees what turning it on would do, where it would run,
 * and where automations are listed. The tour never turns anything on.
 *
 * W1 seeds the draft (fixMyPr.ts) and records its slug in the journey
 * context; the builder steps' routes read it. Every required step waits on a
 * tour signal (signals.ts) emitted by the code that performs the action:
 *   W1 workflow:draftCreated   NewWorkflowDialog / "Create it for me"
 *   W2 workflow:edgeConnected  WorkflowBuilder.createEdge
 *   W3 workflow:nodeSelected   WorkflowBuilder.onNodeClick (or loopExpanded)
 *   W4 workflow:nodeAdded      CanvasInsertContext.insertNode
 *   W5 workflow:triggerDeclared + workflow:saved
 *   W6 automation:dialogOpened, then machineChosen or dialogClosed
 *   W7 the Machines route
 * Then a Reliant-only outro tours the chat and its panels and tidies them away.
 */

import { useEffect, useState, type ReactNode } from "react";
import { toast } from "sonner";

import { useTriggers } from "../../../hooks/trigger-queries";
import { reliantAppOrigin } from "../../../lib/shell";
import { triggerRunsWorkflow } from "../../../lib/triggerRail";
import { WORKFLOW_PAGES_PREFIX, WORKFLOWS_LIBRARY_PATH } from "../../../lib/workflowsArea";
import { useProjectStore } from "../../../store/projectStore";
import { useTourStore } from "../../../store/tourStore";
import { tourEvents, type TourEventMap, type TourEventName } from "../tourEvents";
import type { AdvanceOn, Journey, JourneyContext, JourneyRoute } from "../types";
import { FIX_MY_PR_START, seedFixMyPr } from "./fixMyPr";
import { layoutActions } from "./layoutActions";
import { WF_ENABLE_STEP } from "./workflowsTour";

/** The node ids the steps point at; they come from fixMyPrTemplate.yaml. */
const REVIEW = "review";
const LOOP = "fix_until_green";
const MERGE = "merge";
export const LOOP_CONDITION = "outputs.exit_code != 0 && iter.iteration < 3";

const BUILDER_PATH = "/workflow/";

function workflowSlug(context: JourneyContext): string | undefined {
  return typeof context.workflowSlug === "string" && context.workflowSlug ? context.workflowSlug : undefined;
}

/** The builder, on the draft W1 created. */
const builderRoute: JourneyRoute = {
  path: BUILDER_PATH,
  open: (context) => {
    const slug = workflowSlug(context);
    return slug ? { to: "/workflow/$workflowName", params: { workflowName: slug } } : null;
  },
  label: "Open Fix my PR",
  message: "Open your Fix my PR workflow to continue this step.",
};

// ─── Step-specific copy that depends on what the user did ────────────────────

/** Whether `event` (narrowed by `where`) has been emitted since this mounted. */
function useSignalSeen<K extends TourEventName>(event: K, where?: (payload: TourEventMap[K]) => boolean): boolean {
  const [seen, setSeen] = useState(false);
  useEffect(
    () => tourEvents.on(event, (payload) => (!where || where(payload)) && setSeen(true)),
    // `where` is static step content.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [event],
  );
  return seen;
}

function Check({ done, children }: { done: boolean; children: ReactNode }) {
  return (
    <li className="flex items-center gap-1.5" data-done={done ? "true" : "false"}>
      <span aria-hidden="true" className={done ? "text-success-ink" : "text-muted-foreground"}>
        {done ? "✓" : "○"}
      </span>
      <span className="sr-only">{done ? "Done: " : "To do: "}</span>
      {children}
    </li>
  );
}

/** W5: the two checks, and a reminder when W2's edge is still missing. */
function TriggerChecklist() {
  const declared = useSignalSeen("workflow:triggerDeclared", (p) => p.source === "schedule");
  const saved = useSignalSeen("workflow:saved", (p) => p.triggerSources.includes("schedule"));
  const reviewConnected =
    typeof document === "undefined" || !!document.querySelector(`[data-edge-source="${REVIEW}"][data-edge-target="${LOOP}"]`);
  return (
    <>
      <ul className="mt-2 space-y-1 text-xs" data-testid="wf-trigger-checklist">
        <Check done={declared}>Add a Schedule trigger</Check>
        <Check done={saved}>Save</Check>
      </ul>
      {saved && !reviewConnected && <p className="mt-2 text-xs">review isn't connected yet; you can drag it later.</p>}
    </>
  );
}

/** W4: where the Approval landed, recorded by the step's `where`. */
let lastApproval: TourEventMap["workflow:nodeAdded"] | null = null;

function ApprovalPlacement() {
  const onMergeLine = lastApproval?.anchor === "splice" && lastApproval.spliceTarget === MERGE;
  return onMergeLine ? (
    <>It's on the line into merge, so the run stops and asks you before it merges.</>
  ) : (
    <>It's not on the line into merge. That works for the tour, and you can drag it there later.</>
  );
}

/** W8: whether the user turned Fix my PR on in W6 (the tour never does). */
function AutomationStatus() {
  const projectId = useProjectStore((s) => s.currentProject?.id);
  const slug = useTourStore((s) => workflowSlug(s.contexts?.workflows ?? {}));
  const triggers = useTriggers(projectId, { enabled: !!projectId && !!slug });
  const on = !!slug && (triggers.data ?? []).some((t) => triggerRunsWorkflow(t, slug));
  return on ? (
    <>Fix my PR is on that list because you turned it on.</>
  ) : (
    <>Fix my PR isn't on it, because it isn't turned on.</>
  );
}

// ─── W1's fallback: "Create it for me" ───────────────────────────────────────

async function createItForMe(): Promise<void> {
  const projectId = useProjectStore.getState().currentProject?.id;
  try {
    if (!projectId) throw new Error("Pick a project first, then try again.");
    await seedFixMyPr(projectId);
  } catch (error) {
    toast.error(error instanceof Error ? error.message : "Could not create the workflow");
    throw error; // stay on the step: Create it for me again, or End tour
  }
}

const eventSeen = <K extends TourEventName>(event: K, where?: (p: TourEventMap[K]) => boolean): AdvanceOn =>
  ({ kind: "event", event, where }) as AdvanceOn;

export const workflowsJourney: Journey = {
  id: "workflows",
  // v1 was the engine's two-step placeholder; v0 the retired single tour.
  version: 2,
  // Workflows need Reliant; the forge shell links here instead. Reliant's
  // origin is the runtime config's (APP_URL), as the shell's "Open in Reliant".
  shells: ["reliant"],
  elsewhere: {
    label: "Try AI workflows in Reliant",
    href: () => `${reliantAppOrigin()}/?journey=workflows`,
  },
  title: "Use AI workflows",
  description:
    "Build a workflow step by step: agents, loops and branches, a trigger that runs it for you, and the machine it runs on.",
  steps: [
    {
      id: "wf-new",
      title: "Start a workflow",
      description:
        "A workflow is a set of steps an agent follows. You'll build one that reviews a pull request, fixes it until the tests pass, and asks you before it merges. Click New workflow.",
      target: "button:has([data-tour='new-workflow'])",
      route: { path: WORKFLOW_PAGES_PREFIX, open: { to: WORKFLOWS_LIBRARY_PATH }, label: "Open Workflows" },
      phases: [
        { target: "button:has([data-tour='new-workflow'])" },
        {
          when: { kind: "element", selector: "[data-tour='new-workflow-dialog']" },
          target: "[data-tour='new-workflow-dialog']",
          description:
            "Keep the guided start and click Create workflow. The draft is yours: you can change or delete any step.",
        },
      ],
      advanceOn: {
        kind: "when",
        test: ({ signals, location }) =>
          signals["workflow:draftCreated"]?.start === FIX_MY_PR_START &&
          location.pathname.startsWith(BUILDER_PATH) &&
          location.pathname !== "/workflow/new",
      },
      autoAdvance: true,
      actionHint: "Click New workflow, then Create workflow",
      signal: "draft_created",
      // Every later step needs the draft.
      skip: { label: "Create it for me", run: createItForMe },
    },
    {
      id: "wf-edge",
      title: "Connect the steps",
      description: (
        <>
          Steps run in the order their edges say. <code>review</code> isn't connected yet. Drag from the dot on the
          right of <code>review</code> to <code>{LOOP}</code>.
        </>
      ),
      target: `.react-flow__handle.source[data-nodeid="${REVIEW}"]`,
      spotlightConfig: { padding: 10 },
      route: builderRoute,
      advanceOn: eventSeen("workflow:edgeConnected", (p) => p.sourceId === REVIEW && p.targetId === LOOP),
      autoAdvance: true,
      actionHint: `Drag from review to ${LOOP}`,
      signal: "edge_connected",
    },
    {
      id: "wf-loop",
      title: "A loop",
      description: (
        <>
          Click <code>{LOOP}</code>. Inside, an agent makes a fix, then your test command runs. The loop repeats while
          the tests fail, up to 3 times. You can change that condition in the panel that opens.
        </>
      ),
      target: `.react-flow__node[data-id="${LOOP}"]`,
      route: builderRoute,
      advanceOn: [
        eventSeen("workflow:nodeSelected", (p) => p.nodeId === LOOP && p.type === "loop"),
        eventSeen("workflow:loopExpanded", (p) => p.nodeId === LOOP),
      ],
      afterAction: (
        <>
          The condition is <code>{LOOP_CONDITION}</code>.
        </>
      ),
      actionHint: `Click ${LOOP}`,
      signal: "loop_opened",
    },
    {
      id: "wf-branch",
      title: "A branch, and asking you first",
      description:
        "After the loop the workflow branches. If the tests pass it goes to merge; otherwise it goes to report. Click the + on the line into merge and add an Approval step. The run will then stop and ask you before it merges.",
      target: `[data-testid^="edge-insert-"][data-edge-target="${MERGE}"]`,
      spotlightConfig: { padding: 12 },
      route: builderRoute,
      phases: [
        { target: `[data-testid^="edge-insert-"][data-edge-target="${MERGE}"]` },
        {
          when: { kind: "element", selector: "[data-palette-item='approval']" },
          target: "[data-palette-item='approval']",
          description: "Pick Approval. It goes where you clicked the +.",
        },
      ],
      // Deliberately lenient: an Approval anywhere counts.
      advanceOn: eventSeen("workflow:nodeAdded", (p) => {
        if (p.type !== "approval") return false;
        lastApproval = p;
        return true;
      }),
      afterAction: <ApprovalPlacement />,
      actionHint: "Click the + on the line into merge, then pick Approval",
      signal: "approval_added",
      onEnter: () => {
        lastApproval = null;
      },
    },
    {
      id: "wf-trigger",
      title: "When it runs",
      description: (
        <>
          Right now a chat starts it. Click Add trigger and pick Schedule. It starts as weekdays at 9:00 in your
          timezone; change it if you like. Then click Save.
          <TriggerChecklist />
        </>
      ),
      target: "[data-tour='add-trigger']",
      route: builderRoute,
      phases: [
        { target: "[data-tour='add-trigger']" },
        { when: { kind: "element", selector: "[data-palette-item='schedule']" }, target: "[data-palette-item='schedule']" },
        {
          when: eventSeen("workflow:triggerDeclared", (p) => p.source === "schedule"),
          target: "[data-testid='workflow-save']",
        },
      ],
      advanceOn: {
        kind: "when",
        test: ({ signals }) =>
          signals["workflow:triggerDeclared"]?.source === "schedule" &&
          !!signals["workflow:saved"]?.triggerSources.includes("schedule"),
      },
      autoAdvance: true,
      actionHint: "Add a Schedule trigger, then Save",
      signal: "schedule_saved",
      // Without a saved schedule there is no Activate button: W6 explains it from the trigger lane.
      skip: { run: ({ setContext }) => setContext({ scheduleSkipped: true }) },
    },
    {
      id: WF_ENABLE_STEP,
      title: "Turning it on is your call",
      description:
        "The schedule is declared but off. Nothing runs until you turn it on. Click Activate to see what that asks.",
      target: "[data-tour='activate-trigger']",
      route: builderRoute,
      phases: [
        { target: "[data-tour='activate-trigger']" },
        {
          // W5 was skipped: no schedule, so no Activate. Explain it from the lane.
          when: { kind: "when", test: ({ context }) => context.scheduleSkipped === true },
          target: "[data-tour='add-trigger']",
          description:
            "A trigger starts a workflow by itself. Once one is declared and saved, Activate opens the Turn on dialog: you pick the project, the machine it runs on and its inputs, and from then on it runs without you. The tour never turns anything on.",
        },
        {
          when: { kind: "element", selector: "[data-tour='runs-on']" },
          target: "[data-tour='runs-on']",
          description:
            "Runs on is where each run happens: one of your machines, or No machine for workflows that only use the web and integrations. This one runs your tests, so it needs a machine. Turn on enables every trigger this workflow declares, with the project, machine and inputs you pick here. After that it runs on weekdays at 9:00 without you. Each run is listed in Workflows › Runs, a failed run notifies you, and you can switch it off any time. Fix my PR is still a draft, and a draft can't run yet: Publish makes it runnable, then Turn on starts its schedule. The tour won't press either. Close the dialog when you're done.",
        },
      ],
      // Turning it on stays the user's call: the dialog's button is live, and
      // the tour never presses it or asks them to.
      advanceOn: [
        {
          kind: "when",
          test: ({ signals }) =>
            !!signals["automation:dialogOpened"] &&
            (!!signals["automation:machineChosen"] || !!signals["automation:dialogClosed"]),
        },
        { kind: "when", test: ({ context }) => context.scheduleSkipped === true },
      ],
      actionHint: "Click Activate, look at Runs on, then close the dialog",
      signal: "enable_viewed",
    },
    {
      id: "wf-machines",
      title: "Machines",
      description:
        "A machine is a computer that runs your workflows: your own, or a cloud machine Reliant runs for you. A cloud machine wakes when a run needs it and suspends when it's idle, so it doesn't run up hours while it waits. Automations pick one when you turn them on. No machine yet? Add one here when a workflow needs to run code.",
      target: "[data-tour='machines-list']",
      tooltipPosition: "left",
      route: { path: "/settings/machines", label: "Open Machines" },
      advanceOn: { kind: "route", path: "/settings/machines" },
      actionHint: "Open Machines",
      signal: "machines_viewed",
    },
    {
      id: "wf-automations",
      title: "Automations",
      description: (
        <>
          Automations live in the Workflows list. Runs by itself says what starts each workflow and Next run says when;
          the On switch pauses or resumes it. The On filter shows only the workflows that run by themselves. Webhooks
          and GitHub, Gmail or Slack events work the same way as your schedule. <AutomationStatus />
        </>
      ),
      // The filter chip is always there, with or without any trigger; the
      // summary above the list only appears once something has one.
      target: "[data-tour='workflows-show-on']",
      tooltipPosition: "bottom",
      route: { path: WORKFLOWS_LIBRARY_PATH, label: "Open Workflows" },
    },
    {
      id: "wf-params",
      title: "Inputs belong to the workflow",
      description: (
        <>
          Fix my PR has two inputs, <code>pr</code> and <code>test_command</code>. When you pick a workflow in a chat,
          its inputs become the controls under the message box, so each workflow brings its own chat parameters. A
          preset is a saved set of those values.
        </>
      ),
      target: "[data-tour='workflow-inputs']",
      tooltipPosition: "bottom",
      route: builderRoute,
    },
  ],
  outro: [
    {
      id: "wf-outro-layout",
      // The forge shell has no chat or workspace panels.
      shell: "reliant",
      title: "Back to the chat",
      description:
        "The chat is where work starts: pick a workflow under the message box and send. Your conversations are on the left; files and the terminal open on the right. Each panel has a button at the top of the window, and the same button closes it. We'll close the side panels now so you start with just the chat. Open any of them again from its button.",
      targets: [
        { selector: "[data-onboarding='chat-input']", label: "The chat", description: "Start work here." },
        { selector: "[aria-label='Toggle Chat Sidebar']", label: "Conversations", description: "Opens and closes the list on the left." },
        { selector: "[aria-label='Toggle File Browser']", label: "Files", description: "Opens and closes files on the right." },
        { selector: "[aria-label='Toggle Terminal']", label: "Terminal", description: "Opens and closes the terminal." },
      ],
      route: {
        path: "/project/",
        open: () => {
          const projectId = useProjectStore.getState().currentProject?.id;
          return projectId ? { to: "/project/$projectId", params: { projectId } } : { to: "/" };
        },
        label: "Open the chat",
      },
      // Show the panels being explained, then tidy them away.
      onEnter: () => {
        layoutActions.showSidebar();
        layoutActions.showFiles();
      },
      onComplete: layoutActions.collapseAll,
    },
  ],
};
