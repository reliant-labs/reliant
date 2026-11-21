// Copyright (c) 2026 Reliant Labs

/**
 * THE PROJECT SHELL'S EMPTY STATE (U6d, owner): every empty page has a real
 * call to action, not just a sentence. Three ways forward, always in this
 * order, then the guide:
 *
 *   1. the page's own action, when it has one ("Add a domain")
 *   2. Ask an agent to deploy this with forge — a new chat with the deploy
 *      written up (DeployWithAgentButton inside the console, which knows the
 *      app; a chat with the deploy starter outside it)
 *   3. Migrate an existing app to forge — the Move to Forge workflow
 *      (builtin://forge-migrate), started from its Run dialog
 *   4. Learn — the section's guide (lib/learnLinks.tsx)
 *
 * `ProjectEmptyCta` is the whole block (icon, title, what the page shows, the
 * row). `ProjectEmptyCtaRow` is the row alone, for a frame that already says
 * what the page is (GettingStarted renders it under its agent action).
 */

import { lazy, Suspense, useContext, useState, type ReactNode } from "react";
import { ArrowRightLeft, Rocket } from "lucide-react";

import Button, { buttonClasses } from "@/components/forge-ui/button";
import Link from "@/components/forge-ui/link";
import { getCachedProjects } from "@/hooks/project-queries";
import { LearnLink, type LearnSection } from "@/lib/learnLinks";
import { homePath, projectChatsPath, withSearch, workflowDetailPath } from "@/lib/paths";
import { cn } from "@/lib/utils";
import { useProjectStore } from "@/store/projectStore";

import { START_APP, START_REPO } from "../Apps/appStart";
import { ForgeConsoleContext } from "../Forge/Console/consoleContextValue";
import { learnSectionOf } from "../Forge/Console/consoleLearn";

// The Run dialog loads when it opens: every console empty state renders this
// row, and the dialog brings the run form and the chat actions with it.
const RunNowDialog = lazy(() => import("@/components/workflow/run/RunNowDialog").then((m) => ({ default: m.RunNowDialog })));

/** The Move to Forge workflow (internal/workflow/builtin/forge-migrate.yaml). */
export const MIGRATE_WORKFLOW_REF = "builtin://forge-migrate";

/** The deploy action's words on every empty state (owner, U6d). */
export const ASK_DEPLOY_LABEL = "Ask an agent to deploy this with forge";
export const MIGRATE_LABEL = "Migrate an existing app to forge";

/**
 * The Reliant project an action runs in: the one named, the console's, the
 * one open in the shell, else the person's first. Null with no project at all.
 */
function useActionProjectId(projectId?: string | null): string | null {
  const consoleProjectId = useContext(ForgeConsoleContext)?.projectId ?? null;
  const current = useProjectStore((state) => state.currentProject?.id ?? null);
  return projectId ?? consoleProjectId ?? current ?? getCachedProjects()[0]?.id ?? null;
}

/**
 * Migrate an existing app to forge: opens the Move to Forge workflow's Run
 * dialog in the project (its inputs, a machine, then the run). With no project
 * to run in, the workflow's own page, which says what it needs.
 */
export function MigrateToForgeButton({ projectId, variant = "secondary" }: { projectId?: string | null; variant?: "primary" | "secondary" }) {
  const project = useActionProjectId(projectId);
  const [open, setOpen] = useState(false);
  if (!project) {
    return (
      <Link href={workflowDetailPath(MIGRATE_WORKFLOW_REF)} className={buttonClasses({ variant, size: "md" })} data-testid="empty-cta-migrate">
        <ArrowRightLeft className="h-4 w-4" aria-hidden="true" />
        {MIGRATE_LABEL}
      </Link>
    );
  }
  return (
    <>
      <Button
        variant={variant}
        size="md"
        leftIcon={<ArrowRightLeft className="h-4 w-4" />}
        onClick={() => setOpen(true)}
        title="Run the Move to Forge workflow: it reads your code, plans the port, asks before it changes anything, then moves it onto forge."
        data-testid="empty-cta-migrate"
      >
        {MIGRATE_LABEL}
      </Button>
      {open && (
        <Suspense fallback={null}>
          <RunNowDialog open onClose={() => setOpen(false)} projectId={project} workflowRef={MIGRATE_WORKFLOW_REF} />
        </Suspense>
      )}
    </>
  );
}

/**
 * Ask an agent to deploy, outside the console (a project with no app yet): a
 * new chat in the project with the deploy starter in the composer, or the
 * app-building one with no project. Inside the console use DeployWithAgentButton,
 * which writes the app's own deploy up.
 */
export function DeployChatLink({ projectId, variant = "primary" }: { projectId?: string | null; variant?: "primary" | "secondary" }) {
  const href = projectId ? withSearch(projectChatsPath(projectId), { start: START_REPO }) : withSearch(homePath(), { start: START_APP });
  return (
    <Link href={href} className={cn(buttonClasses({ variant, size: "md" }))} data-testid="empty-cta-deploy">
      <Rocket className="h-4 w-4" aria-hidden="true" />
      {ASK_DEPLOY_LABEL}
    </Link>
  );
}

export interface ProjectEmptyCtaRowProps {
  /** The page's own action, first. */
  action?: ReactNode;
  /**
   * The deploy action: a node to use (DeployWithAgentButton), `"chat"` for a
   * chat in `projectId` (DeployChatLink), or false for none.
   */
  deploy?: ReactNode | "chat" | false;
  projectId?: string | null;
  /** The guide; default: the console page's (consoleLearn.ts). */
  learn?: LearnSection | false;
  className?: string;
  testId?: string;
}

/** The empty state's row of actions, then its Learn link. */
export function ProjectEmptyCtaRow({ action, deploy = "chat", projectId, learn, className, testId = "project-empty-cta" }: ProjectEmptyCtaRowProps) {
  const consolePage = useContext(ForgeConsoleContext)?.page;
  const section = learn === false ? undefined : (learn ?? (consolePage ? learnSectionOf(consolePage) : undefined));
  const primaryTaken = !!action;
  return (
    <div className={cn("flex flex-wrap items-center gap-2", className)} data-testid={testId}>
      {action}
      {deploy === "chat" ? <DeployChatLink projectId={projectId} variant={primaryTaken ? "secondary" : "primary"} /> : deploy || null}
      <MigrateToForgeButton projectId={projectId} />
      {section && <LearnLink section={section} className="ml-1" />}
    </div>
  );
}

export interface ProjectEmptyCtaProps extends ProjectEmptyCtaRowProps {
  icon?: ReactNode;
  title: ReactNode;
  /** What the page shows once it has something, and anything about why it is empty. */
  children?: ReactNode;
  /** The heading's rung: 1 when the block is the whole page (an undeployed app's page). */
  headingLevel?: 1 | 2;
  testId?: string;
}

/** The whole empty page: icon, title, what goes here, then the row. */
export function ProjectEmptyCta({ icon, title, children, headingLevel = 2, testId = "project-empty", ...row }: ProjectEmptyCtaProps) {
  const rowProps = { ...row, testId: `${testId}-cta` };
  const Heading = headingLevel === 1 ? "h1" : "h2";
  return (
    <div className="mx-auto flex max-w-[var(--form-max)] flex-col items-start gap-3 py-16" data-testid={testId}>
      <span className="flex size-9 items-center justify-center rounded-xl border border-border text-ink-muted [&_svg]:size-4" aria-hidden="true">
        {icon ?? <Rocket />}
      </span>
      <div className="space-y-1">
        <Heading className="text-section text-ink">{title}</Heading>
        {children && <div className="space-y-1 text-ui text-ink-muted">{children}</div>}
      </div>
      <ProjectEmptyCtaRow {...rowProps} className="pt-1" />
    </div>
  );
}
