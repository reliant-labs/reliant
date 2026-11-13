// Copyright (c) 2026 Reliant Labs

/**
 * "Put an agent on it": big, friendly cards, each one agentic workflow, plus
 * "Chat with an agent about this app". The demo cannot run anything, so a
 * card opens a sheet: what the workflow does in three plain steps, "Set this
 * up in Reliant" (the real docs), and Sign up.
 *
 *   AgentCardGrid     the five workflow cards (the landing, the Agents page)
 *   ChatWithAgentCard the chat card, wide
 *   PageAgentStrip    one card in context, at the top of Errors, Alerts,
 *                     Slow requests and Releases (pageTop, DemoNavExtras)
 */

import { useState, type ReactNode } from "react";
import { ArrowRight, BellRing, Bug, ExternalLink, MessageSquare, Newspaper, ShieldCheck, Timer } from "lucide-react";

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
    <span className={cn("inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary-ink", className)}>
      <Icon className="h-5 w-5" aria-hidden="true" />
    </span>
  );
}

/** The sheet a card opens: three steps, the real setup, and sign up. */
export function AgentWorkflowSheet({ workflow, open, onClose }: { workflow: AgentWorkflow; open: boolean; onClose: () => void }) {
  const chat = workflow.id === "chat";
  const [primary, ...more] = workflow.docs;
  return (
    <Modal
      open={open}
      onClose={onClose}
      size="md"
      title={chat ? workflow.title : `An agent that ${workflow.title}`}
      description={workflow.outcome}
    >
      <div className="space-y-5" data-testid={`agent-sheet-${workflow.id}`}>
        <ol className="space-y-3">
          {workflow.steps.map((step, index) => (
            <li key={step} className="flex gap-3 text-base leading-relaxed text-ink">
              <span className="mt-0.5 inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-full border border-border text-sm font-semibold text-ink-muted" aria-hidden="true">
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
            className="inline-flex min-h-11 items-center gap-2 rounded-lg border border-border bg-surface px-4 text-base font-medium text-ink hover:bg-ink/5 focus-visible:ring-2 focus-visible:ring-ring"
            data-testid="agent-sheet-setup"
          >
            Set this up in Reliant
            <ExternalLink className="h-4 w-4" aria-hidden="true" />
          </a>
          <SignUpButton placement="agent_sheet" />
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

/** One big card: the whole card is the button. */
export function AgentWorkflowCard({ id, placement, className }: { id: AgentWorkflowId; placement: string; className?: string }) {
  const workflow = AGENT_WORKFLOWS[id];
  const sheet = useSheet(id, placement);
  return (
    <>
      <button
        type="button"
        onClick={sheet.show}
        className={cn(
          "group flex h-full w-full flex-col items-start gap-4 rounded-2xl border border-border bg-surface p-5 text-left transition-colors hover:border-primary/50 hover:bg-ink/[0.02] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:p-6",
          className,
        )}
        data-testid={`agent-card-${id}`}
      >
        <WorkflowIcon id={id} />
        <span className="space-y-1.5">
          <span className="block text-sm font-medium text-ink-muted">{workflow.when}</span>
          <span className="block text-lg font-semibold leading-snug text-ink">{capitalize(workflow.title)}</span>
          <span className="block text-base leading-relaxed text-ink-muted">{workflow.outcome}</span>
        </span>
        <span className="mt-auto inline-flex items-center gap-1.5 text-base font-medium text-primary-ink">
          See how it works
          <ArrowRight className="h-4 w-4 transition-transform group-hover:translate-x-0.5" aria-hidden="true" />
        </span>
      </button>
      <AgentWorkflowSheet workflow={workflow} open={sheet.open} onClose={sheet.close} />
    </>
  );
}

export function AgentCardGrid({ placement, ids = AGENT_WORKFLOW_ORDER }: { placement: string; ids?: AgentWorkflowId[] }) {
  return (
    <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3" data-testid="agent-card-grid">
      {ids.map((id) => (
        <AgentWorkflowCard key={id} id={id} placement={placement} />
      ))}
    </div>
  );
}

/**
 * The workflows as a plain list, one row each: when it runs, what it does,
 * and its own call to action (the same words as on the pages). The landing's
 * editorial alternative to the card grid.
 */
export function AgentWorkflowList({ placement, ids = AGENT_WORKFLOW_ORDER }: { placement: string; ids?: AgentWorkflowId[] }) {
  return (
    <ul className="divide-y divide-border border-y border-border" data-testid="agent-workflow-list">
      {ids.map((id) => (
        <AgentWorkflowRow key={id} id={id} placement={placement} />
      ))}
    </ul>
  );
}

function AgentWorkflowRow({ id, placement }: { id: AgentWorkflowId; placement: string }) {
  const workflow = AGENT_WORKFLOWS[id];
  const sheet = useSheet(id, placement);
  return (
    <li className="grid gap-x-6 gap-y-2 py-3 sm:grid-cols-[10rem_minmax(0,1fr)_auto] sm:items-center" data-testid={`agent-card-${id}`}>
      <span className="text-xs font-medium text-ink-muted">{workflow.when}</span>
      <span className="min-w-0">
        <span className="block text-sm font-semibold text-ink">{capitalize(workflow.title)}</span>
        <span className="block text-sm text-ink-muted">{workflow.outcome}</span>
      </span>
      <button
        type="button"
        onClick={sheet.show}
        className="inline-flex min-h-9 items-center justify-center gap-1.5 justify-self-start rounded-lg border border-border bg-surface px-3 text-sm font-semibold text-ink hover:border-primary/50 hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:justify-self-end"
        data-testid={`agent-card-open-${id}`}
      >
        {workflow.cta}
        <ArrowRight className="h-3.5 w-3.5" aria-hidden="true" />
      </button>
      <AgentWorkflowSheet workflow={workflow} open={sheet.open} onClose={sheet.close} />
    </li>
  );
}

/** "Chat with an agent about this app": wide, with sample questions. `bare` drops the icon tile. */
export function ChatWithAgentCard({ placement, bare = false }: { placement: string; bare?: boolean }) {
  const workflow = AGENT_WORKFLOWS.chat;
  const sheet = useSheet("chat", placement);
  return (
    <>
      <button
        type="button"
        onClick={sheet.show}
        className="group flex w-full flex-col gap-4 rounded-2xl border border-primary/40 bg-primary/5 p-5 text-left transition-colors hover:bg-primary/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:flex-row sm:items-center sm:p-6"
        data-testid="agent-card-chat"
      >
        {!bare && <WorkflowIcon id="chat" className="bg-primary/15" />}
        <span className="min-w-0 flex-1 space-y-1">
          <span className="block text-lg font-semibold text-ink">{workflow.title}</span>
          <span className="block text-base leading-relaxed text-ink-muted">“{CHAT_PROMPTS[0]}” — ask it the way you'd ask a colleague.</span>
        </span>
        <span className="inline-flex shrink-0 items-center gap-1.5 text-base font-medium text-primary-ink">
          Start a chat
          <ArrowRight className="h-4 w-4 transition-transform group-hover:translate-x-0.5" aria-hidden="true" />
        </span>
      </button>
      <AgentWorkflowSheet workflow={workflow} open={sheet.open} onClose={sheet.close} />
    </>
  );
}

/**
 * The page's own "put an agent on it", in context: what the page shows, in a
 * sentence, and the one workflow that acts on it. Replaces the small "How an
 * agent helps" disclosure on these pages.
 */
export function PageAgentStrip({ id, lead }: { id: AgentWorkflowId; lead: ReactNode }) {
  const workflow = AGENT_WORKFLOWS[id];
  const sheet = useSheet(id, "page_strip");
  return (
    <section
      className="flex flex-col gap-3 rounded-2xl border border-border bg-surface px-4 py-4 sm:flex-row sm:items-center sm:gap-4 sm:px-5"
      aria-label="Put an agent on it"
      data-testid={`page-agent-strip-${id}`}
    >
      <WorkflowIcon id={id} className="max-sm:hidden" />
      <div className="min-w-0 flex-1">
        <p className="text-base leading-relaxed text-ink">{lead}</p>
        <p className="text-sm text-ink-muted">Put an agent on it: {workflow.outcome}</p>
      </div>
      <button
        type="button"
        onClick={sheet.show}
        className="inline-flex min-h-11 shrink-0 items-center justify-center gap-2 rounded-lg bg-primary px-4 text-base font-semibold text-primary-foreground hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        data-testid={`page-agent-strip-open-${id}`}
      >
        {workflow.cta}
      </button>
      <AgentWorkflowSheet workflow={workflow} open={sheet.open} onClose={sheet.close} />
    </section>
  );
}
