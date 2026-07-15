import {
  Boxes,
  GitBranch,
  Cpu,
  Server,
  Workflow,
  KeyRound,
  Building2,
} from "lucide-react";
import type { SlideData } from "./types";

/**
 * The deck, as data. Each entry maps one approved slide markdown file to a
 * typed data object; SlideLayout renders it based on `layout`.
 * Content sourced from pitch-deck-content/slides/*.md. Placeholders marked
 * NEEDS_FOUNDER_INPUT / NEEDS_RESEARCH are preserved verbatim — no invented
 * numbers.
 */
export const slides: SlideData[] = [
  // 01 — Hook
  {
    id: "hook",
    number: 1,
    navLabel: "Hook",
    layout: "centered-hero",
    eyebrow: "Reliant",
    title: "You stopped writing code.\nNow you babysit the AI.",
    subtitle:
      "Cursor and Claude Code made writing code almost free. Driving them still costs you every prompt, every diff, every test.",
  },

  // 02 — Insight
  {
    id: "insight",
    number: 2,
    navLabel: "Insight",
    layout: "two-column",
    eyebrow: "The insight",
    title: "Writing code got cheap.\nVerifying it did not.",
    columns: [
      {
        eyebrow: "Generation → zero",
        heading: "The model layer is a commodity",
        body: "60+ model providers. Bring your own key. No lock-in. And it keeps getting cheaper.",
      },
      {
        eyebrow: "Verification → still yours",
        heading: "Orchestration is where value moved",
        body: "Determinism, checking every diff, a human signing off on the output. That cost didn't fall.",
      },
    ],
    lead: "Vibe coding was the demo. Vibe engineering is the job.",
  },

  // 03 — Why now
  {
    id: "why-now",
    number: 3,
    navLabel: "Why now",
    layout: "timeline",
    eyebrow: "Why now",
    title: "Agents can finally execute.\nNothing makes them reliable.",
    timeline: [
      { marker: "2021", label: "Copilot ships autocomplete" },
      { marker: "2023", label: "GPT-4, Claude: multi-step planning" },
      { marker: "2023", label: "Cursor: agents edit real repos" },
      { marker: "2025", label: "Claude Code: agents run full tasks" },
      {
        marker: "Now",
        label: "Models commoditized. Orchestration and determinism missing.",
        emphasis: true,
      },
    ],
    footnote:
      "Capability timeline from public product release history — dates to be re-verified against a dated source before publishing.",
  },

  // 04 — Product
  {
    id: "product",
    number: 4,
    navLabel: "Product",
    layout: "stacked",
    eyebrow: "The product, running today",
    title: "Define a workflow, run it,\nget committed code.",
    lead: "Multi-agent workflows on a React Flow canvas. Write the YAML, drag the nodes, or describe it and let Reliant build the graph.",
    sections: [
      {
        heading: "Three ways to build",
        body: "Hand-write the YAML, drag nodes on the canvas, or describe what you want and the builder generates the graph.",
      },
      {
        heading: "Every node is an agent",
        body: "Its own tools, its own context. When the run finishes you get committed code — not a suggestion you have to babysit.",
      },
    ],
  },

  // 05 — How it works
  {
    id: "how-it-works",
    number: 5,
    navLabel: "How it works",
    layout: "card-grid",
    eyebrow: "How it works",
    title: "A team of agents,\nnot one chatbot in a loop.",
    cards: [
      {
        icon: Boxes,
        title: "14 specialized agents",
        body: "Planner, researcher, implementer, reviewer, tester, debug, git, and 7 more. They spawn each other.",
      },
      {
        icon: Cpu,
        title: "CEL decides what runs next",
        body: "Node conditions are code — exit_code == 0 — not the model's guess about whether tests passed.",
      },
      {
        icon: GitBranch,
        title: "Git worktrees run in parallel",
        body: "Separate branches, separate agents, at once. This repo ships that way.",
      },
      {
        icon: Server,
        title: "Runs headless and remote",
        body: "The daemon does the file work; it need not sit on your laptop.",
      },
    ],
  },

  // 06 — Proof
  {
    id: "proof",
    number: 6,
    navLabel: "Proof",
    layout: "full-bleed-stat",
    eyebrow: "Traction, honestly",
    title: "We build Reliant with Reliant.",
    stat: {
      value: "62",
      label: "GitHub stars — and we run our company on it",
      source:
        "62 stars, 4 forks: verified from the public repo. Early on the public numbers; real on the dogfooding.",
    },
  },

  // 07 — Competition
  {
    id: "competition",
    number: 7,
    navLabel: "Competition",
    layout: "comparison-table",
    eyebrow: "Competition",
    title: "They give you one agent.\nWe give you a process.",
    table: {
      columns: ["Cursor / Claude Code", "Reliant"],
      rows: [
        { label: "Agents on a task", cells: ["One", "Many, orchestrated"] },
        { label: "Unit of work", cells: ["Chat turn", "Defined workflow"] },
        { label: "Parallel tasks", cells: [false, true] },
        { label: "Role handoffs", cells: ["Manual copy-paste", "Automatic"] },
        { label: "Repeatable runs", cells: [false, true] },
      ],
    },
    footnote:
      "Competitor column describes their public single-agent chat model. Specific competitor metrics to be verified before this slide ships.",
  },

  // 08 — Market
  {
    id: "market",
    number: 8,
    navLabel: "Market",
    layout: "full-bleed-stat",
    eyebrow: "Market",
    title: "Every engineer will run agents.\nSomeone has to orchestrate them.",
    stat: {
      value: "$[ NEEDS RESEARCH ]B",
      label: "Orchestration and verification, sitting above AI-code tools",
      source:
        "Placeholder shown intentionally. Market-sizing research still in progress — no fabricated figure.",
    },
  },

  // 09 — Business model
  {
    id: "business-model",
    number: 9,
    navLabel: "Business model",
    layout: "two-column",
    eyebrow: "Business model",
    title: "BYOK for adoption.\nManaged platform for revenue.",
    columns: [
      {
        eyebrow: "The wedge",
        heading: "BYOK, zero markup",
        body: "Your Claude or OpenAI key, your bill. Nothing to approve in procurement. Gets us in the door — not the business.",
      },
      {
        eyebrow: "The revenue",
        heading: "Managed platform + Forge",
        body: "Hosted control plane for teams that don't want to run it. Forge bundled for scaffolding. Team and enterprise seats.",
      },
    ],
    footnote:
      "Pricing and current numbers: NEEDS_FOUNDER_INPUT — not fabricated here.",
  },

  // 10 — Team
  {
    id: "team",
    number: 10,
    navLabel: "Team",
    layout: "card-grid",
    eyebrow: "Team",
    title: "The team building Reliant",
    cards: [
      {
        icon: Workflow,
        title: "Founders",
        body: "NEEDS_FOUNDER_INPUT — names, roles, and the one prior thing that proves this team can build it.",
      },
      {
        icon: Building2,
        title: "Headcount & split",
        body: "NEEDS_FOUNDER_INPUT — team size and eng/GTM split, still open from the interview.",
      },
      {
        icon: KeyRound,
        title: "Unfair advantage",
        body: "NEEDS_FOUNDER_INPUT — why this team wins. If background is the edge, this slide moves ahead of the product.",
      },
    ],
    footnote:
      "Founder interview not yet returned. No credentials invented — placeholders preserved deliberately.",
  },

  // 11 — Vision
  {
    id: "vision",
    number: 11,
    navLabel: "Vision",
    layout: "centered-hero",
    eyebrow: "Vision",
    title: "The engineering layer\nfor a world run by agents.",
    subtitle: "Workflows that outlive the models running them.",
  },

  // 12 — Ask
  {
    id: "ask",
    number: 12,
    navLabel: "The ask",
    layout: "centered-hero",
    eyebrow: "The ask",
    title: "Raising [ NEEDS_FOUNDER_INPUT ]\nto turn early signal into repeatable growth.",
    subtitle:
      "Round, valuation, and the 2–3 milestones this round funds: to be provided by the founder before delivery. No placeholders in the room.",
  },
];
