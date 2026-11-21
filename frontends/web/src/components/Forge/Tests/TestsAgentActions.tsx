// Copyright (c) 2026 Reliant Labs

/**
 * The Tests pages' agent buttons: "Fix this flaky test" (Flaky tests, a
 * test's page) and "Raise coverage of <pkg>" (Coverage). Each builds its
 * AskReliant subject when clicked (testsAgent.ts), reading what the page has
 * not loaded yet through the same query keys the pages use, so a read the
 * page already made is not made again. It then starts an agent the way "Fix
 * with agent" does: a new chat on a workspace and git branch of its own,
 * sent at once (useAskReliant, intent "fix").
 *
 * Where there is no chat to start (the forge shell, a demo: handoff mode),
 * each is the house's one "Ask an agent" button instead, opening the handoff
 * sheet with the same subject: its prompt and the forge commands to run.
 *
 * Disabled, with the reason beside it, when no agent can start in Reliant
 * (agentAvailability): no Reliant project for this app, or a project that is
 * not a git repository.
 */

import { useCallback, useId, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Bot, Sparkles } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { testKeys } from "@/hooks/test-queries";
import { useProject } from "@/hooks/project-queries";
import { useOrgScope } from "@/lib/orgScope";
import { toast } from "@/lib/toast-manager";
import { useTestApi } from "@/services/controlPlane/tests/TestApiContext";
import type { CoverageTarget, TestFlake } from "@/services/controlPlane/tests/types";

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

interface AgentButtonProps {
  project: string;
  projectId: string | null | undefined;
  label: ReactNode;
  icon: ReactNode;
  hint: string;
  testId: string;
  /** The subject, built when clicked. */
  build: () => Promise<AskSubject>;
}

function AgentButton({ project, projectId, label, icon, hint, testId, build }: AgentButtonProps) {
  const availability = useTestsAgentAvailability(projectId);
  const { ask, busy, dialog, mode } = useAskReliant({ forgeProject: project });
  const handoff = mode === "handoff";
  const [building, setBuilding] = useState(false);
  const reason = useId();
  const onClick = useCallback(async () => {
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

  if (!availability.available) {
    return (
      <span className="inline-flex flex-wrap items-center gap-2">
        <Button variant="outline" size="sm" leftIcon={icon} disabled aria-describedby={reason} title={availability.reason} data-testid={testId}>
          {label}
        </Button>
        <span id={reason} className="text-meta text-ink-muted" data-testid={`${testId}-reason`}>
          {availability.reason}
        </span>
      </span>
    );
  }
  return (
    <>
      <Button
        variant="outline"
        size="sm"
        leftIcon={handoff ? <Bot className="h-3.5 w-3.5" /> : icon}
        onClick={() => void onClick()}
        loading={building || busy}
        title={handoff ? ASK_AGENT_HINT : hint}
        data-testid={testId}
        data-mode={mode}
      >
        {handoff ? ASK_AGENT_LABEL : label}
      </Button>
      {dialog}
    </>
  );
}

const FIX_HINT = "Start an agent on it now, in a new chat on its own workspace and git branch: it makes the test deterministic or deletes it, runs it 50 times under -race, and removes its quarantine entry.";

/** "Fix this flaky test": the test's failure signatures come from its own read (GetTest), cached when its page made it. */
export function FixFlakyTestButton({ project, projectId, flake, defaultBranch }: { project: string; projectId: string | null | undefined; flake: TestFlake; defaultBranch?: string }) {
  const api = useTestApi();
  const org = useOrgScope();
  const client = useQueryClient();
  const build = useCallback(async (): Promise<AskSubject> => {
    const testId = flake.test.id;
    const detail = await client.fetchQuery({
      queryKey: testKeys.test(org.orgId, project, testId),
      queryFn: ({ signal }) => api.getTest({ orgId: org.orgId, project, testId }, { signal }),
      staleTime: LIVE_MS,
    });
    // The default branch the flake gate measures from: the summary's (cached when Flaky tests read it). Without it, the prompt says "<default branch>".
    const branch =
      defaultBranch ??
      (await client
        .fetchQuery({
          queryKey: testKeys.summary(org.orgId, project, "", ""),
          queryFn: ({ signal }) => api.getTestSummary({ orgId: org.orgId, project }, { signal }),
          staleTime: LIVE_MS,
        })
        .then((summary) => summary.branch || undefined)
        .catch(() => undefined));
    return flakyTestSubject(detail.flake ?? flake, detail, branch);
  }, [api, client, defaultBranch, flake, org.orgId, project]);
  return <AgentButton project={project} projectId={projectId} label="Fix this flaky test" icon={<Bot className="h-3.5 w-3.5" />} hint={FIX_HINT} testId="tests-fix-with-agent" build={build} />;
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
  const api = useTestApi();
  const org = useOrgScope();
  const client = useQueryClient();
  const build = useCallback(async (): Promise<AskSubject> => {
    const query = { target, path: pkg };
    const tree = await client.fetchQuery({
      queryKey: testKeys.coverage(org.orgId, project, query),
      queryFn: ({ signal }) => api.getCoverage({ orgId: org.orgId, project, ...query }, { signal }),
      staleTime: SETTLED_MS,
    });
    const paths = filesToRead(tree).slice(0, MAX_UNCOVERED_FILES);
    const files = await Promise.all(
      paths.map((path) =>
        client.fetchQuery({
          queryKey: testKeys.fileCoverage(org.orgId, project, target, path, ""),
          queryFn: ({ signal }) => api.getFileCoverage({ orgId: org.orgId, project, target, path }, { signal }),
          staleTime: SETTLED_MS,
        })
      )
    );
    return coverageSubject({ pkg, tree, files, defaultBranch });
  }, [api, client, defaultBranch, org.orgId, pkg, project, target]);
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
