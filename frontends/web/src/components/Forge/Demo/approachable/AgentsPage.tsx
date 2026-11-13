// Copyright (c) 2026 Reliant Labs

/**
 * "Agents": every "put an agent on it" workflow on one page, the chat first,
 * and what an agent will never do (Autopilot's guarantees, docs
 * /guides/autopilot "What it will never do").
 */

import { ExternalLink, GitPullRequest, ShieldCheck, ScrollText } from "lucide-react";

import { AgentCardGrid, ChatWithAgentCard } from "./AgentCards";
import { Panel } from "./ui";

const SAFE = [
  { icon: GitPullRequest, title: "It never merges on its own", body: "An agent proposes a change. A person on your team reads it and decides." },
  { icon: ShieldCheck, title: "It asks before anything important", body: "Nothing turns on until you switch it on, and you choose what needs your approval." },
  { icon: ScrollText, title: "Everything is written down", body: "Every run keeps its full record: what it looked at, what it did, and why." },
];

export function AgentsPage() {
  return (
    <div className="mx-auto w-full max-w-5xl space-y-12 pb-20 pt-4 sm:pt-8" data-testid="demo-agents">
      <header className="space-y-4">
        <h1 className="text-4xl font-semibold tracking-tight text-ink sm:text-5xl">Put an agent on it</h1>
        <p className="max-w-2xl text-xl leading-relaxed text-ink-muted">
          Pick a job. Reliant sets up an agent that does it for your app on its own, using the same errors, timelines and releases you see in this demo.
        </p>
      </header>
      <ChatWithAgentCard placement="agents_page" />
      <section className="space-y-4" aria-labelledby="agents-workflows">
        <h2 id="agents-workflows" className="text-2xl font-semibold text-ink">
          Create an agentic workflow that…
        </h2>
        <AgentCardGrid placement="agents_page" />
      </section>
      <section className="space-y-4" aria-labelledby="agents-safe">
        <h2 id="agents-safe" className="text-2xl font-semibold text-ink">
          How it stays safe
        </h2>
        <div className="grid gap-4 sm:grid-cols-3">
          {SAFE.map(({ icon: Icon, title, body }) => (
            <Panel key={title} className="space-y-2">
              <Icon className="h-5 w-5 text-ink-muted" aria-hidden="true" />
              <h3 className="text-lg font-semibold text-ink">{title}</h3>
              <p className="text-base leading-relaxed text-ink-muted">{body}</p>
            </Panel>
          ))}
        </div>
        <a
          href="https://docs.reliantlabs.io/guides/autopilot#what-it-will-never-do"
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex min-h-11 items-center gap-2 rounded-lg px-1 text-base font-medium text-primary-ink underline-offset-2 hover:underline"
        >
          Read what an agent will never do
          <ExternalLink className="h-4 w-4" aria-hidden="true" />
        </a>
      </section>
    </div>
  );
}
