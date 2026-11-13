// Copyright (c) 2026 Reliant Labs

/**
 * "Put an agent on it": the agentic workflows the demo offers, in plain words.
 *
 * Nothing runs in the demo. Each card opens a sheet that says what the
 * workflow does in three steps and links to the real setup in Reliant's docs
 * (every URL below is a page and heading that exists on docs.reliantlabs.io;
 * checked against the docs filesystem when this was written).
 */

export type AgentWorkflowId = "fix-errors" | "watch-alerts" | "slow-pages" | "weekly-digest" | "release-guard" | "chat";

export interface DocLink {
  label: string;
  href: string;
}

export interface AgentWorkflow {
  id: AgentWorkflowId;
  /** Finishes "Create an agentic workflow that…" (the chat card stands alone). */
  title: string;
  /** One sentence: the outcome, in everyday words. */
  outcome: string;
  /** What happens, in three plain steps. */
  steps: [string, string, string];
  /** Where it is set up for real: the first is the primary "Set this up" link. */
  docs: [DocLink, ...DocLink[]];
  /** What sets it off, said once on the card ("When a new error appears"). */
  when: string;
  /** The in-context button's words, on the page the workflow acts on. */
  cta: string;
}

const DOCS = "https://docs.reliantlabs.io";

export const AGENT_WORKFLOWS: Record<AgentWorkflowId, AgentWorkflow> = {
  "fix-errors": {
    id: "fix-errors",
    cta: "Put an agent on new errors",
    title: "triages and fixes new errors",
    outcome: "When something breaks for a customer, an agent works out why and drafts the fix.",
    when: "When a new error appears",
    steps: [
      "A new kind of error shows up, or an old one comes back after a release.",
      "An agent reads what the customer hit, the request behind it and the code that changed, and writes up the cause.",
      "You say \"go\", and it writes a fix and a test and opens a pull request for you to review. It never merges on its own.",
    ],
    docs: [
      { label: "Autopilot: alerts and errors", href: `${DOCS}/guides/autopilot#alerts-and-errors` },
      { label: "Autopilot: the issue worker", href: `${DOCS}/guides/autopilot#issue-worker` },
    ],
  },
  "watch-alerts": {
    id: "watch-alerts",
    cta: "Put an agent on alerts",
    title: "watches alerts and opens a fix",
    outcome: "An alert at 2am wakes an agent instead of a person.",
    when: "When an alert fires",
    steps: [
      "An alert fires: checkout is failing, or a page got slow.",
      "An agent opens an issue with what it found: how bad it is, the evidence, the likely cause.",
      "When you agree, an agent writes the fix and opens a pull request, so a reviewed change is waiting in the morning.",
    ],
    docs: [
      { label: "Your organization's alerts", href: `${DOCS}/automations/webhooks#your-organizations-alerts` },
      { label: "Autopilot: alerts and errors", href: `${DOCS}/guides/autopilot#alerts-and-errors` },
    ],
  },
  "slow-pages": {
    id: "slow-pages",
    cta: "Put an agent on slow pages",
    title: "debugs slow pages",
    outcome: "Hand the agent a slow request and it finds the slow part, instead of you guessing.",
    when: "When a page gets slow",
    steps: [
      "A page takes longer than it should, for real customers.",
      "An agent reads the timeline of one slow request, step by step, and finds where the time goes: a slow database query, a slow outside service.",
      "It proposes the fix, and checks after the next release that the page really got faster.",
    ],
    docs: [
      { label: "Describe it in a chat", href: `${DOCS}/automations/overview#just-describe-it-in-a-chat` },
      { label: "Why this helps an agent", href: `${DOCS}/forge/observability/agents` },
    ],
  },
  "weekly-digest": {
    id: "weekly-digest",
    cta: "Get a weekly digest",
    title: "writes a weekly \"how is my app doing\" digest",
    outcome: "Every Monday, one short note: what shipped, what broke, who showed up.",
    when: "Every Monday at 9am",
    steps: [
      "On a schedule you choose, an agent reads the week: releases, errors, slow pages and how many people used the app.",
      "It writes a short summary in plain language, with links to anything that needs a look.",
      "It posts it where your team reads, such as Slack or email.",
    ],
    docs: [
      { label: "Schedules", href: `${DOCS}/automations/schedules` },
      { label: "Create a workflow", href: `${DOCS}/guides/create-a-workflow` },
    ],
  },
  "release-guard": {
    id: "release-guard",
    cta: "Put an agent on every release",
    title: "checks every release and rolls back if it's bad",
    outcome: "Every release is judged against the hour before it. A bad one doesn't stay out.",
    when: "After every release",
    steps: [
      "A release reaches production, and the agent waits for real traffic.",
      "It compares errors and speed with the hour before. If things got worse, it says exactly what.",
      "With your approval, it moves production back to the last good build and files an issue with the evidence.",
    ],
    docs: [
      { label: "Reliant workflows can react", href: `${DOCS}/forge/observability/agents#reliant-workflows-can-react` },
      { label: "Releases and promotion", href: `${DOCS}/forge/deploy/releases-and-promotion` },
    ],
  },
  chat: {
    id: "chat",
    cta: "Chat with an agent",
    title: "Chat with an agent about this app",
    outcome: "Ask in plain words. The agent reads the same errors, requests and releases you see here.",
    when: "Whenever you have a question",
    steps: [
      "Ask a question the way you would ask a colleague: \"Why was checkout slow last night?\"",
      "The agent looks at the same evidence on these pages, and at your code.",
      "It answers with what it found, and offers to fix it.",
    ],
    docs: [
      { label: "Start chatting", href: `${DOCS}/getting-started/quick-start` },
      { label: "Built-in workflows", href: `${DOCS}/guides/overview` },
    ],
  },
};

/** The cards in the order the landing and the Agents page show them. */
export const AGENT_WORKFLOW_ORDER: AgentWorkflowId[] = ["fix-errors", "watch-alerts", "slow-pages", "weekly-digest", "release-guard"];

/** A few questions to start a chat with: each about the sample app's real story. */
export const CHAT_PROMPTS = [
  "Why are some checkouts failing since this morning's release?",
  "Which page is slowest for customers, and why?",
  "Where did this week's new customers come from?",
];
