/**
 * The "Use AI workflows" journey: SHOW a real workflow, don't make the user
 * build one. The first step adds "Fix my PR" to the user's workflows (their
 * own draft, complete and wired: review, a loop until the tests pass, a
 * branch that asks before merging, a weekday schedule) and opens it on the
 * builder canvas. Each step after pans the canvas to one idea and explains it
 * beside the steps it is about, never over them (workflowsCanvas.tsx). The
 * only action anywhere is optional: "Try it yourself" on the edge step.
 *
 *   1 wf-example   the example, and what a step is (review, an agent)
 *   2 wf-edge      edges, and how to draw one (a looping drag demonstration)
 *   3 wf-loop      a loop and how it ends
 *   4 wf-branch    a switch, and an approval before merge
 *   5 wf-trigger   the schedule: declared, off until turned on
 *   6 wf-machine   where it runs (Activate asks: Runs on)
 *   7 wf-inputs    inputs belong to the workflow
 *   8 outro        back to the chat; Make your own, or Finish
 *
 * Phones get the same lessons as a card deck with still diagrams
 * (workflowsPhone.tsx). The copy for both is in workflowsLessons.tsx.
 */

import { useEffect, useRef, useState, type ReactNode } from "react";
import { useLocation, useNavigate, useSearch } from "@tanstack/react-router";
import { toast } from "sonner";

import { reliantAppOrigin } from "../../../lib/shell";
import { useProjectStore } from "../../../store/projectStore";
import { useTourStore } from "../../../store/tourStore";
import type { TourEventMap, TourEventName } from "../tourEvents";
import type { AdvanceOn, Journey, JourneyContext, JourneyRoute, JourneyStep } from "../types";
import { useTourNavigation } from "../useTourNavigation";
import { EXAMPLE_SLUG, ensureFixMyPrExample } from "./fixMyPr";
import { layoutActions } from "./layoutActions";
import { EdgeLesson, FOCUS_TARGET, OnCanvas } from "./workflowsCanvas";
import { APPROVE, LESSONS, LOOP, MERGE, REPORT, REVIEW, SCHEDULE, START, SWITCH } from "./workflowsLessons";
import { WorkflowsPhoneDeck } from "./workflowsPhone";

const BUILDER_PATH = "/workflow/";

function exampleSlug(context: JourneyContext): string | undefined {
  const slug = context[EXAMPLE_SLUG];
  return typeof slug === "string" && slug ? slug : undefined;
}

/** The builder, on the example. */
const builderRoute: JourneyRoute = {
  path: BUILDER_PATH,
  open: (context) => {
    const slug = exampleSlug(context);
    return slug ? { to: "/workflow/$workflowName", params: { workflowName: slug } } : null;
  },
  label: "Open Fix my PR",
  message: "Open the Fix my PR example to continue.",
};

const lesson = (id: string) => LESSONS.find((l) => l.id === id)!;

// ─── Step 1: open the example (adding it once) ───────────────────────────────

/** One import per project at a time, however often the step mounts (StrictMode, Back). */
const opening = new Map<string, Promise<string>>();

function ExampleIntro() {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const search = useSearch({ strict: false }) as { tour?: string };
  const projectId = useProjectStore((s) => s.currentProject?.id);
  const slug = useTourStore((s) => exampleSlug(s.contexts?.workflows ?? {}));
  const [failed, setFailed] = useState<string | null>(null);
  const onExample = !!slug && pathname === `/workflow/${slug}`;
  const tour = useRef(search.tour);
  tour.current = search.tour;

  useEffect(() => {
    if (onExample || !projectId || failed) return;
    let cancelled = false;
    const pending = opening.get(projectId) ?? ensureFixMyPrExample(projectId);
    opening.set(projectId, pending);
    pending
      .then((made) => {
        if (cancelled) return;
        void navigate({ to: "/workflow/$workflowName", params: { workflowName: made }, search: { tour: tour.current } } as never);
      })
      .catch((error: unknown) => {
        if (cancelled) return;
        const message = error instanceof Error ? error.message : "Could not add the example";
        toast.error(message);
        setFailed(message);
      })
      .finally(() => opening.delete(projectId));
    return () => {
      cancelled = true;
    };
  }, [onExample, projectId, failed, navigate]);

  if (onExample) return <OnCanvas nodes={[REVIEW]} />;
  if (!projectId) return <p className="mt-2 text-xs" data-testid="wf-example-status">Pick a project first, then start the tour again.</p>;
  if (failed)
    return (
      <p className="mt-2 text-xs" data-testid="wf-example-status">
        {failed}{" "}
        <button type="button" className="font-medium underline underline-offset-2" onClick={() => setFailed(null)}>
          Try again
        </button>
      </p>
    );
  return (
    <p className="mt-2 text-xs text-muted-foreground" data-testid="wf-example-status">
      Opening Fix my PR…
    </p>
  );
}

// ─── The end: Make your own ──────────────────────────────────────────────────

/** Set by Make your own just before Finish: the journey's `finish` sends the user to New workflow. */
let makeOwnRequested = false;

function MakeYourOwn() {
  const nav = useTourNavigation();
  return (
    <button
      type="button"
      data-testid="wf-make-your-own"
      className="mt-3 block rounded-md border border-border px-2.5 py-1.5 text-xs font-medium text-foreground hover:bg-muted"
      onClick={() => {
        makeOwnRequested = true;
        void nav.finish();
      }}
    >
      Make your own
    </button>
  );
}

// ─── Steps ───────────────────────────────────────────────────────────────────

const eventSeen = <K extends TourEventName>(event: K, where?: (p: TourEventMap[K]) => boolean): AdvanceOn =>
  ({ kind: "event", event, where }) as AdvanceOn;

/** A step on the canvas: the lesson's copy, the canvas panned to `nodes`, and the card beside them. */
function canvasStep(id: string, nodes: string[], extra: Partial<JourneyStep> & { box?: string[]; more?: ReactNode } = {}): JourneyStep {
  const { box, more, ...rest } = extra;
  const { title, body } = lesson(id);
  return {
    id,
    title,
    description: (
      <>
        {body}
        {more}
        <OnCanvas nodes={nodes} box={box} />
      </>
    ),
    target: FOCUS_TARGET,
    tooltipPosition: "right",
    spotlightConfig: { padding: 4 },
    route: builderRoute,
    ...rest,
  };
}

export const workflowsJourney: Journey = {
  id: "workflows",
  // v3 shows a finished example instead of building one; v2 built it, v1 was
  // the engine's placeholder and v0 the retired single tour. People who
  // finished v2 see v3 once.
  version: 3,
  controls: "callout",
  // Workflows need Reliant; the forge shell links here instead.
  shells: ["reliant"],
  elsewhere: {
    label: "Try AI workflows in Reliant",
    href: () => `${reliantAppOrigin()}/?journey=workflows`,
  },
  title: "Use AI workflows",
  description:
    "See a finished workflow on the canvas: agent steps, edges, a loop, a branch that asks you first, a schedule, and where it runs.",
  // Make your own ends on New workflow; Finish ends on the chat.
  finish: (shell) => {
    const makeOwn = makeOwnRequested;
    makeOwnRequested = false;
    return shell === "reliant" && makeOwn ? { to: "/workflow/new" } : null;
  },
  // Phones have no builder: the same lessons as a card deck.
  phone: WorkflowsPhoneDeck,
  steps: [
    {
      id: "wf-example",
      title: lesson("wf-example").title,
      description: (
        <>
          {lesson("wf-example").body}
          <ExampleIntro />
        </>
      ),
      target: FOCUS_TARGET,
      tooltipPosition: "right",
      spotlightConfig: { padding: 4 },
      // No route: the step adds the example if needed and opens it itself.
    },
    canvasStep("wf-edge", [REVIEW, LOOP], {
      more: <EdgeLesson source={REVIEW} target={LOOP} />,
      // Optional: Next works throughout; drawing the edge (after Try it yourself) also moves on.
      advanceOn: eventSeen("workflow:edgeConnected", (p) => p.sourceId === REVIEW && p.targetId === LOOP),
      showNext: true,
      autoAdvance: true,
      afterAction: <>That's an edge. Nothing changed in your workflow: it's the same one it had.</>,
      signal: "edge_connected",
    }),
    canvasStep("wf-loop", [LOOP]),
    canvasStep("wf-branch", [SWITCH, APPROVE, MERGE, REPORT]),
    canvasStep("wf-trigger", [START], { box: ["[data-testid='trigger-lane']"] }),
    canvasStep("wf-machine", [START], { box: [`[data-testid="trigger-card-${SCHEDULE}"]`] }),
    {
      id: "wf-inputs",
      title: lesson("wf-inputs").title,
      description: lesson("wf-inputs").body,
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
      description: (
        <>
          The chat is where work starts: pick a workflow under the message box and send. Conversations are on the left;
          files and the terminal open on the right, each from its button at the top. We'll close the side panels when
          you finish. Ready to build one? Make your own starts a new workflow.
          <MakeYourOwn />
        </>
      ),
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

/** For tests: whether a pending Make your own will route Finish to New workflow. */
export function _makeOwnRequested(): boolean {
  return makeOwnRequested;
}
