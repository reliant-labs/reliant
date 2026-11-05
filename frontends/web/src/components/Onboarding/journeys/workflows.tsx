/**
 * The "Use AI workflows" journey.
 *
 * PLACEHOLDER CONTENT: two steps that exercise the engine end to end (an
 * action-gated step, then a page-bound spotlight), plus a Reliant-only outro
 * step that collapses the workspace panels on completion. The real journey
 * (journeys-spec.md §2) replaces
 * these — see ../JOURNEYS.md. When it ships, bump `version` and drop
 * `reshowAfterVersionBump: false`, so users who finished this placeholder
 * (or the retired single tour, recorded as version 0) are offered it once.
 */

import { WORKFLOWS_LIBRARY_PATH } from "../../../lib/workflowsArea";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";
import type { Journey } from "../types";
import { layoutActions } from "./layoutActions";

export const workflowsJourney: Journey = {
  id: "workflows",
  version: 1,
  reshowAfterVersionBump: false,
  // Workflows need Reliant; the forge shell links here instead.
  shells: ["reliant"],
  elsewhere: {
    label: "Try AI workflows in Reliant",
    href: () => "https://app.reliantlabs.io/?journey=workflows",
  },
  title: "Use AI workflows",
  description:
    "Build a workflow step by step: agents, loops and branches, a trigger that runs it for you, and the machine it runs on.",
  steps: [
    {
      id: "wf-open",
      title: "Workflows",
      description:
        "Automate a whole task, like fixing a bug and opening a PR, as one multi-step run instead of message by message.",
      target: "[data-onboarding='workflow-button']",
      spotlightConfig: { padding: 4, detectBorderRadius: true },
      advanceOn: [{ kind: "click" }, { kind: "route", path: "/workflows" }],
      actionHint: "Click Workflows to continue",
      autoAdvance: true,
      onEnter: () => useWorkspaceStateStore.getState().setLeftSidebarExpandedGlobal(true),
    },
    {
      id: "wf-library",
      title: "Library, runs, automations",
      description:
        "Start from a ready-made workflow in the Library, follow every run in Runs, and put one on a schedule in Automations.",
      // forge-ui's SidebarLayout takes no data attributes; its <nav> carries
      // aria-label={navLabel} instead.
      target: "[data-testid='workflows-shell'] nav[aria-label='Workflows']",
      tooltipPosition: "bottom",
      spotlightConfig: { padding: 6, detectBorderRadius: true },
      route: { path: WORKFLOWS_LIBRARY_PATH, label: "Open Workflows" },
    },
  ],
  outro: [
    {
      id: "wf-outro-layout",
      shell: "reliant",
      title: "Back to the chat",
      description:
        "The chat is where work starts. The panels around it, your conversations, files and the terminal, open from their buttons and close the same way. We'll tidy them away for now.",
      onComplete: layoutActions.collapseAll,
    },
  ],
};
