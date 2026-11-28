// Copyright (c) 2026 Reliant Labs

/**
 * The Tests pages' agent actions: "Ask an agent to fix" (Flaky tests, a
 * test's page) and "Raise coverage of <pkg>" (Coverage). Each builds its
 * AskReliant subject when chosen (testsAgent.ts), reading what the page has
 * not loaded yet through the same query keys the pages use, so a read the
 * page already made is not made again. It then starts an agent the way "Fix
 * with agent" does: a new chat on a workspace and git branch of its own,
 * sent at once (useAskReliant, intent "fix").
 *
 * Where there is no chat to start (the forge shell, a demo: handoff mode),
 * the same action opens the handoff sheet with the same subject: its prompt
 * and the forge commands to run.
 *
 * Unavailable when no agent can start in Reliant (agentAvailability): no
 * Reliant project for this app, or a project that is not a git repository.
 * The fix action is then a disabled button (or menu item) whose tooltip says
 * why; the coverage button keeps its reason beside it.
 *
 * useFixFlakyTest is the action without a control, for a row's menu.
 */

import { useCallback, useId, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Bot, Sparkles } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Tooltip from "@/components/forge-ui/tooltip";
import { testsServiceQueries } from "@/hooks/controlplane/tests-service-hooks_gen";
import { onControlPlane, refuseWithoutControlPlane } from "@/hooks/test-queries";
import { useProject } from "@/hooks/project-queries";
import { getOrgScope, useOrgScope, whenOrgScopeReady, withOrgFailure } from "@/lib/orgScope";
import { toast } from "@/lib/toast-manager";
import type { CoverageTarget, FileCoverage, TestFlake } from "@/services/controlPlane/tests/types";
import { coverageRequest, coverageTreeOf, fileCoverageOf, fileCoverageRequest, summaryRequest, testDetailOf, testRequest } from "@/services/controlPlane/tests/wire";

import { ASK_AGENT_HINT, ASK_AGENT_LABEL, useAskReliant } from "../Observability/AskReliant/AskReliant";
import { useAskAgentMode } from "../Observability/AskReliant/askAgentMode";
import type { AskSubject } from "../Observability/AskReliant/askReliantPrompt";
import { agentAvailability, coverageSubject, defaultCoverageTarget, filesToRead, flakyTestSubject, MAX_UNCOVERED_FILES, type AgentAvailability } from "./testsAgent";

/** Reads stay fresh this long, as the pages' own (test-queries LIVE / SETTLED). */
const LIVE_MS = 30_000;
const SETTLED_MS = 5 * 60_000;

/** Whether an agent can start for this console project. */
export function useTestsAgentAvailability(projectId: string | null | undefined): AgentAvailability {
  const { mode } = useAskAgentMode();
  const project = useProject(projectId);
  return agentAvailability({ mode, projectId, project });
}

/** An agent action, without its control: whether it can run (and why not), how to run it, and the dialog it may open. */
export interface AgentAction {
  availability: AgentAvailability;
  start: () => void;
  busy: boolean;
  /** Handoff mode: the action opens the "Ask an agent" sheet rather than starting a chat. */
  handoff: boolean;
  /** Render it once, beside the control: the handoff sheet or the project chooser. */
  dialog: ReactNode;
}

function useAgentAction({ project, projectId, build }: { project: string; projectId: string | null | undefined; build: () => Promise<AskSubject> }): AgentAction {
  const availability = useTestsAgentAvailability(projectId);
  const { ask, busy, dialog, mode } = useAskReliant({ forgeProject: project });
  const [building, setBuilding] = useState(false);
  const run = useCallback(async () => {
    setBuilding(true);
    let subject: AskSubject;
    try {
      subject = await build();
    } catch (error) {
      void toast.error(error);
      return;
    } finally {
      setBuilding(false);
    }
    await ask(subject, undefined, "fix");
  }, [ask, build]);
  return { availability, start: () => void run(), busy: building || busy, handoff: mode === "handoff", dialog };
}

const FIX_HINT = "Start an agent on it now, in a new chat on its own workspace and git branch: it makes the test deterministic or deletes it, runs it 50 times under -race, and removes its quarantine entry.";

export const FIX_LABEL = "Ask an agent to fix";

/** The fix action's hint: what choosing it does. */
export function fixHint(action: Pick<AgentAction, "handoff">): string {
  return action.handoff ? ASK_AGENT_HINT : FIX_HINT;
}

/** "Ask an agent to fix": the test's failure signatures come from its own read (GetTest), cached when its page or row made it. */
export function useFixFlakyTest({ project, projectId, flake, defaultBranch }: { project: string; projectId: string | null | undefined; flake: TestFlake; defaultBranch?: string }): AgentAction {
  const org = useOrgScope();
  const client = useQueryClient();
  const build = useCallback(async (): Promise<AskSubject> => {
    // Never read unscoped: wait for the org (the button can be pressed before it resolves).
    const { orgId } = org.ready ? org : await whenOrgScopeReady();
    const testId = flake.test.id;
    // Refused without a control plane, as the pages' reads are (onControlPlane).
    refuseWithoutControlPlane(client);
    // The same generated reads (and so cache entries) as the pages' hooks; fetchQuery answers the message, mapped here.
    const detail = testDetailOf(await client.fetchQuery(withOrgFailure(onControlPlane(testsServiceQueries.getTest(testRequest({ orgId, project, testId }), { staleTime: LIVE_MS })), getOrgScope())));
    // The default branch the flake gate measures from: the summary's (cached when Flaky tests read it). Without it, the prompt says "<default branch>".
    const branch =
      defaultBranch ??
      (await client
        .fetchQuery(withOrgFailure(onControlPlane(testsServiceQueries.getTestSummary(summaryRequest({ orgId, project }), { staleTime: LIVE_MS })), getOrgScope()))
        .then((summary) => summary.branch || undefined)
        .catch(() => undefined));
    return flakyTestSubject(detail.flake ?? flake, detail, branch);
  }, [client, defaultBranch, flake, org, project]);
  return useAgentAction({ project, projectId, build });
}

/** "Ask an agent to fix", as a button: disabled, its reason in a tooltip, when no agent can start here. */
export function FixFlakyTestButton({
  project,
  projectId,
  flake,
  defaultBranch,
  variant = "outline",
}: {
  project: string;
  projectId: string | null | undefined;
  flake: TestFlake;
  defaultBranch?: string;
  variant?: "primary" | "outline";
}) {
  const action = useFixFlakyTest({ project, projectId, flake, defaultBranch });
  const reason = useId();
  const icon = <Bot className="h-3.5 w-3.5" />;
  if (!action.availability.available) {
    return (
      <Tooltip content={action.availability.reason} triggerClassName="inline-flex">
        <span className="inline-flex">
          <Button variant={variant} size="sm" leftIcon={icon} disabled aria-describedby={reason} data-testid="tests-fix-with-agent">
            {FIX_LABEL}
          </Button>
          <span id={reason} className="sr-only" data-testid="tests-fix-with-agent-reason">
            {action.availability.reason}
          </span>
        </span>
      </Tooltip>
    );
  }
  return (
    <>
      <Button variant={variant} size="sm" leftIcon={icon} onClick={action.start} loading={action.busy} title={fixHint(action)} data-testid="tests-fix-with-agent" data-mode={action.handoff ? "handoff" : "chat"}>
        {FIX_LABEL}
      </Button>
      {action.dialog}
    </>
  );
}

/** The coverage action's button: its reason beside it when it cannot start. */
function AgentButton({ project, projectId, label, icon, hint, testId, build }: { project: string; projectId: string | null | undefined; label: ReactNode; icon: ReactNode; hint: string; testId: string; build: () => Promise<AskSubject> }) {
  const action = useAgentAction({ project, projectId, build });
  const reason = useId();
  if (!action.availability.available) {
    return (
      <span className="inline-flex flex-wrap items-center gap-2">
        <Button variant="outline" size="sm" leftIcon={icon} disabled aria-describedby={reason} title={action.availability.reason} data-testid={testId}>
          {label}
        </Button>
        <span id={reason} className="text-meta text-ink-muted" data-testid={`${testId}-reason`}>
          {action.availability.reason}
        </span>
      </span>
    );
  }
  return (
    <>
      <Button
        variant="outline"
        size="sm"
        leftIcon={action.handoff ? <Bot className="h-3.5 w-3.5" /> : icon}
        onClick={action.start}
        loading={action.busy}
        title={action.handoff ? ASK_AGENT_HINT : hint}
        data-testid={testId}
        data-mode={action.handoff ? "handoff" : "chat"}
      >
        {action.handoff ? ASK_AGENT_LABEL : label}
      </Button>
      {action.dialog}
    </>
  );
}

/**
 * "Raise coverage of <pkg>": the package's listing (GetCoverage, combined, no
 * lens) and the line coverage of its lowest-covered files (GetFileCoverage),
 * as numbers. `currentPercent` names the target in the hint before anything is read.
 */
export function RaiseCoverageButton({
  project,
  projectId,
  pkg,
  target,
  currentPercent,
  defaultBranch,
}: {
  project: string;
  projectId: string | null | undefined;
  pkg: string;
  target: CoverageTarget;
  currentPercent: number | null;
  defaultBranch?: string;
}) {
  const org = useOrgScope();
  const client = useQueryClient();
  const build = useCallback(async (): Promise<AskSubject> => {
    // Never read unscoped: wait for the org (the button can be pressed before it resolves).
    const { orgId } = org.ready ? org : await whenOrgScopeReady();
    const query = { target, path: pkg };
    // Refused without a control plane, as the pages' reads are (onControlPlane).
    refuseWithoutControlPlane(client);
    const tree = coverageTreeOf(await client.fetchQuery(withOrgFailure(onControlPlane(testsServiceQueries.getCoverage(coverageRequest({ orgId, project, ...query }), { staleTime: SETTLED_MS })), getOrgScope())));
    const paths = filesToRead(tree).slice(0, MAX_UNCOVERED_FILES);
    // Read in parallel, each started here, after the org is known (not in a callback of its own).
    const reads: Promise<FileCoverage>[] = [];
    for (const path of paths) {
      reads.push(
        client.fetchQuery(withOrgFailure(onControlPlane(testsServiceQueries.getFileCoverage(fileCoverageRequest({ orgId, project, target, path }), { staleTime: SETTLED_MS })), getOrgScope())).then(fileCoverageOf)
      );
    }
    const files = await Promise.all(reads);
    return coverageSubject({ pkg, tree, files, defaultBranch });
  }, [client, defaultBranch, org, pkg, project, target]);
  const hint = `Start an agent on it now, in a new chat on its own workspace and git branch: it adds tests that assert behaviour until ${pkg} reaches ${defaultCoverageTarget(currentPercent)}% combined, and runs the flake gate on them.`;
  return (
    <AgentButton
      project={project}
      projectId={projectId}
      label={
        <>
          Raise coverage of <span className="font-mono">{pkg}</span>
        </>
      }
      icon={<Sparkles className="h-3.5 w-3.5" />}
      hint={hint}
      testId="tests-coverage-raise-button"
      build={build}
    />
  );
}
