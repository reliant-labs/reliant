/**
 * The workflows journey's lessons: one idea each, in the words both the
 * canvas steps (workflows.tsx) and the phone deck (workflowsPhone.tsx) use,
 * with a still diagram of each.
 */

import type { ReactNode } from "react";

import {
  BranchDiagram,
  EdgeDiagram,
  InputsDiagram,
  LoopDiagram,
  MachineDiagram,
  StepDiagram,
  TriggerDiagram,
} from "./workflowsDiagrams";

/** The ids the steps point at; they come from fixMyPrTemplate.yaml. */
export const REVIEW = "review";
export const LOOP = "fix_until_green";
export const SWITCH = `switch-${LOOP}`;
export const APPROVE = "approve_merge";
export const MERGE = "merge";
export const REPORT = "report";
export const START = "workflow"; // the trigger rail (lib/workflow-flow.ts ENTRY_NODE_ID)
export const SCHEDULE = "weekday-mornings";
export const LOOP_CONDITION = "outputs.exit_code != 0 && iter.iteration < 3";

// ─── The lessons: one idea each, shared by the canvas steps and the phone deck ─

export interface Lesson {
  id: string;
  title: string;
  /** What the idea is. Shown on both. */
  body: ReactNode;
  /** The phone deck's copy, where it differs (the phone shows the example, it doesn't add it). */
  phoneBody?: ReactNode;
  /** A still picture of it: the phone deck's stand-in for the canvas. */
  diagram: () => ReactNode;
}

const c = (text: string) => <code>{text}</code>;

export const LESSONS: Lesson[] = [
  {
    id: "wf-example",
    title: "Fix my PR, already built",
    body: (
      <>
        This is Fix my PR, an example we added to your workflows. It's yours to change, run or delete. Each box is a
        step. {c(REVIEW)} is an agent step: an agent with its own prompt, here to review the pull request and fix the
        small problems.
      </>
    ),
    phoneBody: (
      <>
        Fix my PR is an example workflow: it reviews a pull request, fixes it until the tests pass, and asks you before
        it merges. Each box is a step. {c(REVIEW)} is an agent step: an agent with its own prompt.
      </>
    ),
    diagram: StepDiagram,
  },
  {
    id: "wf-edge",
    title: "Edges set the order",
    body: (
      <>
        The lines are edges. A step runs when the one before it finishes, so {c(REVIEW)} runs first, then {c(LOOP)}. To
        connect two steps, drag from the dot on the right of one to the dot on the left of the next.
      </>
    ),
    diagram: EdgeDiagram,
  },
  {
    id: "wf-loop",
    title: "A loop, and how it ends",
    body: (
      <>
        {c(LOOP)} is a loop. Inside, an agent makes a fix, then your test command runs. It goes round again while{" "}
        {c(LOOP_CONDITION)} holds, so it stops as soon as the tests pass, or after 3 tries.
      </>
    ),
    diagram: LoopDiagram,
  },
  {
    id: "wf-branch",
    title: "A branch, and asking you first",
    body: (
      <>
        After the loop, a switch picks one way. If the tests pass, the run reaches {c(APPROVE)}, an approval: it stops
        and asks you, and {c(MERGE)} runs only if you approve. Otherwise it goes to {c(REPORT)}, which says what still
        fails.
      </>
    ),
    diagram: BranchDiagram,
  },
  {
    id: "wf-trigger",
    title: "When it runs",
    body: (
      <>
        Triggers start a workflow without you. Chat lets you start it from a chat. {c(SCHEDULE)} is a schedule:
        weekdays at 9:00 in your timezone. It's declared but off, so nothing runs until you turn it on. Webhooks and
        GitHub, Gmail or Slack events work the same way.
      </>
    ),
    diagram: TriggerDiagram,
  },
  {
    id: "wf-machine",
    title: "Where it runs",
    body: (
      <>
        Activate asks where each run happens. Runs on is one of your machines: your own computer, or a cloud machine
        that wakes for a run and sleeps when idle. Workflows that only use the web and integrations can pick No machine.
        Fix my PR runs your tests, so it needs one. It's a draft: publish it first. The tour never turns anything on.
      </>
    ),
    diagram: MachineDiagram,
  },
  {
    id: "wf-inputs",
    title: "Inputs belong to the workflow",
    body: (
      <>
        Fix my PR takes two inputs, {c("pr")} and {c("test_command")}. Every workflow declares its own. Pick a workflow
        in a chat and its inputs become the controls under the message box; a schedule fills them in when you activate
        it.
      </>
    ),
    diagram: InputsDiagram,
  },
];


