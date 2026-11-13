// Copyright (c) 2026 Reliant Labs

/**
 * "Put an agent on it", at the tidied console's density (the "75" of the
 * demo density scale): the console keeps its pages and names, and agents
 * appear in two places only.
 *
 *   AgentCardRow    the landing's row of workflow cards (and chat), under the
 *                   build story's hero
 *   PageAgentLine   one compact line on Errors, Alerts, Traces and Releases,
 *                   in place of the small "How an agent helps" tip
 *
 * The demo cannot run anything, so a card opens a sheet: what the workflow
 * does in three steps, "Set this up in Reliant" (the real docs) and Sign up.
 */

import { useState } from "react";
import { ArrowRight, BellRing, Bug, ChevronRight, ExternalLink, MessageSquare, Newspaper, ShieldCheck, Timer } from "lucide-react";

import Modal from "@/components/forge-ui/modal";
import { trackEvent } from "@/lib/analytics";
import { cn } from "@/lib/utils";

import { SignUpButton } from "../DemoCta";
import { AGENT_WORKFLOWS, AGENT_WORKFLOW_ORDER, CHAT_PROMPTS, type AgentWorkflow, type AgentWorkflowId } from "./agentWorkflows";

const ICONS: Record<AgentWorkflowId, typeof Bug> = {
  "fix-errors": Bug,
  "watch-alerts": BellRing,
  "slow-pages": Timer,
  "weekly-digest": Newspaper,
  "release-guard": ShieldCheck,
  chat: MessageSquare,
};

const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

function WorkflowIcon({ id, className }: { id: AgentWorkflowId; className?: string }) {
  const Icon = ICONS[id];
  return (
    <span className={cn("inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary-ink", className)}>
      <Icon className="h-4 w-4" aria-hidden="true" />
    </span>
  );
}

/** The sheet a card opens: three steps, the real setup, and sign up. */
function AgentWorkflowSheet({ workflow, open, onClose }: { workflow: AgentWorkflow; open: boolean; onClose: () => void }) {
  const chat = workflow.id === "chat";
  const [primary, ...more] = workflow.docs;
  return (
    <Modal open={open} onClose={onClose} size="md" title={chat ? workflow.title : `An agent that ${workflow.title}`} description={workflow.outcome}>
      <div className="space-y-5" data-testid={`agent-sheet-${workflow.id}`}>
        <ol className="space-y-3">
          {workflow.steps.map((step, index) => (
            <li key={step} className="flex gap-3 text-sm leading-relaxed text-ink">
              <span
                className="mt-0.5 inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-full border border-border text-xs font-semibold text-ink-muted"
                aria-hidden="true"
              >
                {index + 1}
              </span>
              <span>{step}</span>
            </li>
          ))}
        </ol>
        {chat && (
          <div className="space-y-2">
            <p className="text-sm font-medium text-ink-muted">Questions people ask about this shop</p>
            <ul className="space-y-1.5">
              {CHAT_PROMPTS.map((prompt) => (
                <li key={prompt} className="rounded-lg border border-border bg-surface-sunken px-3 py-2 text-sm text-ink">
                  “{prompt}”
                </li>
              ))}
            </ul>
          </div>
        )}
        <p className="rounded-lg border border-border bg-surface-sunken px-3 py-2 text-sm text-ink-muted">
          This is a sample app, so nothing runs here. In your own app it takes a few minutes to set up, and nothing turns on until you say so.
        </p>
        <div className="flex flex-wrap items-center gap-3">
          <a
            href={primary.href}
            target="_blank"
            rel="noopener noreferrer"
            onClick={() => trackEvent("demo_agent_setup_clicked", { workflow: workflow.id })}
            className="inline-flex min-h-10 items-center gap-2 rounded-lg border border-border bg-surface px-4 text-sm font-medium text-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            data-testid="agent-sheet-setup"
          >
            Set this up in Reliant
            <ExternalLink className="h-4 w-4" aria-hidden="true" />
          </a>
          <SignUpButton placement="agent_sheet" size="md" />
        </div>
        <p className="text-sm text-ink-muted">
          Read more:{" "}
          {[primary, ...more].map((doc, index) => (
            <span key={doc.href}>
              {index > 0 && " · "}
              <a href={doc.href} target="_blank" rel="noopener noreferrer" className="font-medium text-primary-ink underline-offset-2 hover:underline">
                {doc.label}
              </a>
            </span>
          ))}
        </p>
      </div>
    </Modal>
  );
}

function useSheet(id: AgentWorkflowId, placement: string) {
  const [open, setOpen] = useState(false);
  return {
    open,
    show: () => {
      trackEvent("demo_agent_workflow_opened", { workflow: id, placement });
      setOpen(true);
    },
    close: () => setOpen(false),
  };
}

/** One card of the landing's row: the whole card is the button; the sheet says the rest. */
function AgentCard({ id }: { id: AgentWorkflowId }) {
  const workflow = AGENT_WORKFLOWS[id];
  const sheet = useSheet(id, "landing_row");
  const chat = id === "chat";
  return (
    <>
      <button
        type="button"
        onClick={sheet.show}
        title={workflow.outcome}
        className={cn(
          "group flex h-full w-full flex-col items-start gap-2.5 rounded-xl border bg-surface p-3.5 text-left transition-colors hover:border-primary/50 hover:bg-ink/[0.02] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          chat ? "border-primary/40 bg-primary/5" : "border-border",
        )}
        data-testid={`agent-card-${id}`}
      >
        <WorkflowIcon id={id} />
        <span className="block text-sm font-semibold leading-snug text-ink">{chat ? workflow.title : capitalize(workflow.title)}</span>
        <span className="mt-auto inline-flex items-center gap-1 text-xs font-medium text-primary-ink">
          {chat ? "Start a chat" : workflow.when}
          <ChevronRight className="h-3.5 w-3.5 transition-transform group-hover:translate-x-0.5" aria-hidden="true" />
        </span>
      </button>
      <AgentWorkflowSheet workflow={workflow} open={sheet.open} onClose={sheet.close} />
    </>
  );
}

/** The landing's row: "Create an agentic workflow that…", the five workflows and chat. */
export function AgentCardRow() {
  return (
    <section className="space-y-3" aria-labelledby="agent-row-title" data-testid="agent-card-row">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <h2 id="agent-row-title" className="text-base font-semibold text-ink">
          Put an agent on it
        </h2>
        <p className="text-sm text-ink-muted">Agentic workflows that read this app's errors, traces and releases, and act on them. Open one to see how.</p>
      </div>
      <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
        {[...AGENT_WORKFLOW_ORDER, "chat" as const].map((id) => (
          <AgentCard key={id} id={id} />
        ))}
      </div>
    </section>
  );
}

/**
 * The page's own "put an agent on it", in one line: what an agent would do
 * with what this page shows, and the button that opens the workflow's sheet.
 */
export function PageAgentLine({ id, lead }: { id: AgentWorkflowId; lead: string }) {
  const workflow = AGENT_WORKFLOWS[id];
  const sheet = useSheet(id, "page_line");
  return (
    <section
      className="flex flex-col gap-2 rounded-lg border border-border bg-surface px-4 py-2.5 sm:flex-row sm:items-center sm:gap-3"
      aria-label="Put an agent on it"
      data-testid={`page-agent-line-${id}`}
    >
      <WorkflowIcon id={id} className="h-7 w-7 max-sm:hidden [&>svg]:h-3.5 [&>svg]:w-3.5" />
      <p className="min-w-0 flex-1 text-sm text-ink">
        <span className="font-medium">Put an agent on it:</span> <span className="text-ink-muted">{lead}</span>
      </p>
      <button
        type="button"
        onClick={sheet.show}
        className="inline-flex h-8 shrink-0 items-center justify-center gap-1.5 self-start rounded-md border border-border bg-surface px-3 text-sm font-medium text-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:self-auto"
        data-testid={`page-agent-line-open-${id}`}
      >
        {workflow.cta}
        <ArrowRight className="h-3.5 w-3.5" aria-hidden="true" />
      </button>
      <AgentWorkflowSheet workflow={workflow} open={sheet.open} onClose={sheet.close} />
    </section>
  );
}
