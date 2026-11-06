// TEMPORARY (README.md): Reliant's workflows journey as the demo's journey
// registry sees it. Reliant-only (shells: ["reliant"]), so the demo never walks
// it; the end card links to it in Reliant through `elsewhere`.

import type { Journey } from "@/components/Onboarding/types";
import { reliantAppOrigin } from "@/lib/shell";

export const workflowsJourney: Journey = {
  id: "workflows",
  version: 2,
  shells: ["reliant"],
  elsewhere: {
    label: "Try AI workflows in Reliant",
    href: () => `${reliantAppOrigin()}/?journey=workflows`,
  },
  title: "Use AI workflows",
  description: "Build a workflow step by step, in Reliant.",
  steps: [{ id: "wf-new", title: "Start a workflow", description: "Workflows are in Reliant." }],
};
