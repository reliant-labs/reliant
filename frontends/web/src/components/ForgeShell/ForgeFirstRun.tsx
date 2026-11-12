// Copyright (c) 2025 Reliant Labs

/**
 * What a brand-new forge account sees on /apps before anything is deployed:
 * three steps, each command with a copy button.
 *
 *   1. Deploy from the app's folder (signed in: login, adopt, deploy).
 *   2. Or ask an agent: the MCP install commands and one-click links.
 *   3. The docs.
 *
 * Copies and link clicks are recorded with opaque ids only (never the command).
 */

import { ArrowUpRight, Bot, BookOpen, Rocket } from "lucide-react";

import { TourCommand } from "@/components/Onboarding/journeys/TourCommand";
import { trackEvent } from "@/lib/analytics";

export const FORGE_NPX = "npx -y @reliantlabs/forge";

/**
 * forge#2163 makes `env deploy --claimable` adopt a bare Vite or Next.js app
 * itself. Until a release carries it (npm `latest` was 1.4.0 without it), the
 * no-login deploy is two commands. Flip this when forge ships it.
 */
export const CLAIMABLE_DEPLOY_ADOPTS = false;

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
    command: `${FORGE_NPX} env deploy prod --claimable`,
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

const linkClass =
  "inline-flex min-h-9 items-center gap-1.5 rounded-lg border border-border bg-surface px-3 text-sm font-medium text-ink hover:bg-ink/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export function ForgeFirstRun({ testId = "apps-empty" }: { testId?: string }) {
  return (
    <section className="rounded-lg border border-border bg-surface p-4 sm:p-5" data-testid={testId} aria-label="Deploy your first app">
      <h2 className="text-base font-semibold text-ink">Deploy your first app</h2>
      <p className="mt-1 text-sm text-ink-muted">Nothing is deployed to this account yet. Start from the folder of an existing Vite or Next.js app.</p>

      <ol className="mt-4 space-y-5">
        <Step n={1} icon={<Rocket className="h-4 w-4 text-ink-muted" aria-hidden="true" />} title="In your app folder, deploy">
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

        <Step n={2} icon={<Bot className="h-4 w-4 text-ink-muted" aria-hidden="true" />} title="Or ask your agent">
          <p className="text-xs text-ink-muted">
            Add forge to your agent, then ask it to deploy this app with forge. It returns a live URL and a claim link.
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
