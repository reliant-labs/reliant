// Copyright (c) 2025 Reliant Labs

/**
 * What a brand-new forge account sees on /apps before anything is deployed:
 * three steps, each with a copy button.
 *
 *   1. Ask your agent: one prompt for any coding agent with a shell (the same
 *      words as reliantlabs.com/products/forge#start), or forge's MCP server.
 *   2. Or deploy from the app's folder (signed in: login, adopt, deploy).
 *   3. The docs.
 *
 * Copies and link clicks are recorded with opaque ids only (never the command).
 */

import { useEffect, useRef, useState } from "react";
import { ArrowUpRight, Bot, BookOpen, Check, Copy, Rocket } from "lucide-react";

import { TourCommand } from "@/components/Onboarding/journeys/TourCommand";
import { trackEvent } from "@/lib/analytics";

export const FORGE_NPX = "npx -y @reliantlabs/forge";

/**
 * forge#2163 makes `env deploy --claimable` adopt a bare Vite or Next.js app
 * itself, so the no-login deploy is one command. It first shipped in forge
 * v1.5.0; the one-liner runs `npx …@latest` (as the landing page's Terminal
 * tab does) so an older cached forge, which needs the adopt step first, is
 * never the one that runs it.
 */
export const CLAIMABLE_DEPLOY_ADOPTS = true;

/**
 * The prompt for the visitor's own coding agent: run forge through
 * `npx …@latest` (a version floor, and no `npm i -g` EACCES), follow
 * `forge start`, deploy with no login, hand back the URL and the claim link
 * (opened here, the claim lands in this account). `forge start` is the router
 * from reliant-labs/forge#2173; it needs a release carrying it as npm latest.
 * Validated 2026-11-12 against forge main at #2173 with cold agents and no
 * credentials, in a bare Vite app and an empty folder.
 * Word-for-word the same as AGENT_PROMPT in the landing page's
 * src/content/forgeStart.ts.
 */
export const AGENT_PROMPT =
  "Deploy this app with forge, no account needed. Run `npx -y @reliantlabs/forge@latest start` and follow it (use `npx -y @reliantlabs/forge@latest` wherever it says `forge`). When the site is live, give me its URL and the claim link, and remind me it goes offline in 72 hours unless I open the claim link.";

/** What to ask an agent that already has forge's MCP server (its deploy_claimable tool). */
export const MCP_ASK = "Deploy this app with forge, no account needed. When it's live, give me its URL and the claim link.";

export const DOCS_URL = "https://docs.reliantlabs.io/forge/quick-start";
export const CLAUDE_MCP_COMMAND = "claude mcp add forge -- npx -y @reliantlabs/forge mcp";
export const CURSOR_MCP_URL =
  "https://cursor.com/en/install-mcp?name=forge&config=eyJjb21tYW5kIjoibnB4IiwiYXJncyI6WyIteSIsIkByZWxpYW50bGFicy9mb3JnZSIsIm1jcCJdfQ==";
export const VSCODE_MCP_URL =
  "https://insiders.vscode.dev/redirect/mcp/install?name=forge&config=%7B%22command%22%3A%22npx%22%2C%22args%22%3A%5B%22-y%22%2C%22%40reliantlabs%2Fforge%22%2C%22mcp%22%5D%7D";

export interface FirstRunCommand {
  /** Opaque id for analytics. */
  id: string;
  label: string;
  command: string;
}

/** A signed-in person's first deploy: the CLI signs in as them, adopts the app, deploys. */
export function signedInDeployCommands(): FirstRunCommand[] {
  return [
    { id: "login", label: "Sign the CLI in to this account", command: `${FORGE_NPX} login` },
    { id: "adopt", label: "Add forge to a Vite or Next.js app (skip it if the folder has a forge.yaml)", command: `${FORGE_NPX} project new --in-place --kind site` },
    { id: "deploy", label: "Deploy to production", command: `${FORGE_NPX} env deploy prod` },
  ];
}

/** The no-login deploy: live for 72 hours, claimed from the link it prints. */
export function claimableDeployCommands(adopts: boolean = CLAIMABLE_DEPLOY_ADOPTS): FirstRunCommand[] {
  const deploy: FirstRunCommand = {
    id: "claimable_deploy",
    label: adopts ? "Deploy a Vite or Next.js app" : "Deploy",
    command: `${FORGE_NPX}${adopts ? "@latest" : ""} env deploy prod --claimable`,
  };
  if (adopts) return [deploy];
  return [
    { id: "claimable_adopt", label: "Add forge to the app", command: `${FORGE_NPX} project new --in-place --kind site` },
    deploy,
  ];
}

const copied = (id: string) => () => trackEvent("forge_first_run_copied", { id });
const opened = (id: string) => () => trackEvent("forge_first_run_opened", { id });

function Step({ n, icon, title, children }: { n: number; icon: React.ReactNode; title: string; children: React.ReactNode }) {
  return (
    <li className="flex gap-3" data-testid={`forge-first-run-step-${n}`}>
      <span
        className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary-ink"
        aria-hidden="true"
      >
        {n}
      </span>
      <div className="min-w-0 flex-1 space-y-2">
        <p className="flex items-center gap-2 text-sm font-medium text-ink">
          {icon}
          {title}
        </p>
        {children}
      </div>
    </li>
  );
}

function Commands({ commands }: { commands: readonly FirstRunCommand[] }) {
  return (
    <ul className="space-y-2">
      {commands.map((c) => (
        <li key={c.id} className="space-y-1">
          <span className="text-xs text-ink-muted">{c.label}</span>
          <TourCommand command={c.command} onCopied={copied(c.id)} />
        </li>
      ))}
    </ul>
  );
}

/** The agent prompt: prose that wraps (TourCommand is one scrolling shell line), with a Copy button. */
function AgentPrompt({ onCopied }: { onCopied: () => void }) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  async function copy() {
    try {
      await navigator.clipboard.writeText(AGENT_PROMPT);
    } catch {
      return; // No clipboard: the prompt is on screen to select.
    }
    setCopied(true);
    onCopied();
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), 1500);
  }
  return (
    <div className="flex items-start gap-1 rounded-md border border-border bg-muted/40 py-1.5 pl-2.5 pr-0.5" data-testid="forge-first-run-prompt">
      <p className="min-w-0 flex-1 whitespace-pre-wrap text-xs leading-relaxed text-foreground">{AGENT_PROMPT}</p>
      <button
        type="button"
        onClick={() => void copy()}
        aria-label={copied ? "Copied the prompt" : "Copy the prompt"}
        title={copied ? "Copied" : "Copy"}
        className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-foreground"
      >
        {copied ? <Check className="h-3 w-3" aria-hidden="true" /> : <Copy className="h-3 w-3" aria-hidden="true" />}
      </button>
    </div>
  );
}

const linkClass =
  "inline-flex min-h-9 items-center gap-1.5 rounded-lg border border-border bg-surface px-3 text-sm font-medium text-ink hover:bg-ink/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export function ForgeFirstRun({ testId = "apps-empty" }: { testId?: string }) {
  return (
    <section className="rounded-lg border border-border bg-surface p-4 sm:p-5" data-testid={testId} aria-label="Deploy your first app">
      <h2 className="text-base font-semibold text-ink">Deploy your first app</h2>
      <p className="mt-1 text-sm text-ink-muted">Nothing is deployed to this account yet. Use the coding agent you already have, or the terminal.</p>

      <ol className="mt-4 space-y-5">
        <Step n={1} icon={<Bot className="h-4 w-4 text-ink-muted" aria-hidden="true" />} title="Ask your agent">
          <p className="text-xs text-ink-muted">
            Paste this into Claude Code, Cursor, Codex or any agent that can run commands, in your app's folder or an empty one. Open the claim link it
            gives you here to keep the app in this account.
          </p>
          <AgentPrompt onCopied={copied("agent_prompt")} />
          <details className="text-sm" data-testid="forge-first-run-mcp">
            <summary className="cursor-pointer text-ink-muted hover:text-ink">Agent supports MCP?</summary>
            <div className="mt-2 space-y-2">
              <p className="text-xs text-ink-muted">
                Add forge as an MCP server, then ask: “{MCP_ASK}” Its <code className="font-mono">deploy_claimable</code> tool deploys a Vite or Next.js app as
                it stands. Starting from an empty folder? Use the prompt above: <code className="font-mono">forge start</code> covers that.
              </p>
              <div className="space-y-1">
                <span className="text-xs text-ink-muted">Claude Code</span>
                <TourCommand command={CLAUDE_MCP_COMMAND} onCopied={copied("claude_mcp")} />
              </div>
              <div className="flex flex-wrap gap-2">
                <a href={CURSOR_MCP_URL} target="_blank" rel="noopener noreferrer" onClick={opened("cursor_mcp")} className={linkClass} data-testid="forge-first-run-cursor">
                  Add to Cursor
                  <ArrowUpRight className="h-3.5 w-3.5" aria-hidden="true" />
                </a>
                <a href={VSCODE_MCP_URL} target="_blank" rel="noopener noreferrer" onClick={opened("vscode_mcp")} className={linkClass} data-testid="forge-first-run-vscode">
                  Install in VS Code
                  <ArrowUpRight className="h-3.5 w-3.5" aria-hidden="true" />
                </a>
              </div>
            </div>
          </details>
        </Step>

        <Step n={2} icon={<Rocket className="h-4 w-4 text-ink-muted" aria-hidden="true" />} title="Or deploy from your app folder">
          <Commands commands={signedInDeployCommands()} />
          <details className="text-sm" data-testid="forge-first-run-claimable">
            <summary className="cursor-pointer text-ink-muted hover:text-ink">Rather not sign the CLI in?</summary>
            <div className="mt-2 space-y-2">
              <p className="text-xs text-ink-muted">
                This deploys with no login. It stays live for 72 hours and prints a claim link; open it here to keep it in this account.
              </p>
              <Commands commands={claimableDeployCommands()} />
            </div>
          </details>
        </Step>

        <Step n={3} icon={<BookOpen className="h-4 w-4 text-ink-muted" aria-hidden="true" />} title="Read the docs">
          <a href={DOCS_URL} target="_blank" rel="noopener noreferrer" onClick={opened("docs")} className="text-sm font-medium text-primary-ink underline-offset-2 hover:underline" data-testid="forge-first-run-docs">
            Forge quick start
          </a>
        </Step>
      </ol>
    </section>
  );
}
