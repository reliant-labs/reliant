// Copyright (c) 2026 Reliant Labs

/**
 * The Tests pages' states, each worded for what it means to the reader
 * (tests/errors.ts testsErrorKind) and drawn in the console's state frames, so
 * they look like every other state there.
 *
 * A control plane without TestsService is a state, not an error: "this
 * control plane doesn't store test runs yet" (isTestsServiceMissing). And no
 * state, and no empty figure, ever reads as "all tests pass": an empty record
 * says so, with the commands that fill it.
 */

import type { ReactNode } from "react";
import { AlertTriangle, FlaskConical, Lock, RefreshCw, SearchX } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { ProjectEmptyCta } from "@/components/EmptyState/ProjectEmptyCta";
import { obsErrorMessage } from "@/services/controlPlane/observability/errors";
import { testsErrorKind, type TestsErrorKind } from "@/services/controlPlane/tests/errors";

import { CommandLine } from "../GettingStarted";
import { StateFrame } from "../Observability/ObsStates";

const code = (text: string) => <code className="font-mono text-ink">{text}</code>;

const RETRY_BUTTON = "min-h-[44px] min-w-[44px] sm:min-h-0 sm:min-w-0";

const KIND_ICON: Record<TestsErrorKind, ReactNode> = {
  "not-configured": <FlaskConical className="h-4 w-4" />,
  denied: <Lock className="h-4 w-4" />,
  "not-found": <SearchX className="h-4 w-4" />,
  "bad-token": <AlertTriangle className="h-4 w-4" />,
  unavailable: <AlertTriangle className="h-4 w-4" />,
};

function errorCopy(kind: TestsErrorKind, subject: string): { title: string; body: ReactNode } {
  switch (kind) {
    case "not-configured":
      return {
        title: "This control plane doesn't store test runs yet",
        body: (
          <>
            It has no test service yet, so this page has nothing to read. Runs you record are kept on the machine that ran them until it does:{" "}
            {code("reliant forge test report")} reads them there.
          </>
        ),
      };
    case "denied":
      return {
        title: "You can't read this project's tests",
        body: (
          <>
            Test runs need the {code("tests:read")} grant in this organization; everyone who can read deploys has it. An organization admin can give it to you.
          </>
        ),
      };
    case "not-found":
      return { title: `${subject} isn't in this project`, body: "No run, release or test you can read in this project has it." };
    case "bad-token":
      return { title: "That page of results can't be opened here", body: "A page link belongs to one set of filters. Start from the first page." };
    case "unavailable":
      return { title: `Couldn't read ${subject}`, body: "The control plane did not answer." };
  }
}

/** A failed read, in words. `subject` names what was being read: "this project's tests", "run 01JD…". */
export function TestsError({ error, subject, onRetry, retrying, testId = "tests-error" }: { error: unknown; subject: string; onRetry?: () => void; retrying?: boolean; testId?: string }) {
  const kind = testsErrorKind(error);
  const copy = errorCopy(kind, subject);
  const canRetry = onRetry && (kind === "unavailable" || kind === "bad-token");
  return (
    <div data-testid={testId} data-kind={kind}>
      <StateFrame
        testId={`${testId}-${kind}`}
        tone={kind === "not-configured" || kind === "not-found" ? "neutral" : "error"}
        icon={KIND_ICON[kind]}
        title={copy.title}
        actions={
          canRetry ? (
            <Button variant="outline" size="sm" className={RETRY_BUTTON} leftIcon={<RefreshCw className="h-3.5 w-3.5" />} onClick={onRetry} disabled={retrying}>
              {retrying ? "Retrying…" : "Retry"}
            </Button>
          ) : undefined
        }
      >
        <p>{copy.body}</p>
        {kind === "unavailable" && <p className="break-words font-mono text-meta">{obsErrorMessage(error)}</p>}
      </StateFrame>
    </div>
  );
}

/**
 * Nothing recorded yet: what the section will show, and the commands that
 * record the first run. forge never runs a suite of its own: `test run` runs
 * the project's Taskfile lanes, and `test capture` wraps any lane command.
 */
export function TestsNoRunsYet({ projectId, branch, testId = "tests-no-runs" }: { projectId?: string | null; branch?: string; testId?: string }) {
  return (
    <ProjectEmptyCta
      testId={testId}
      icon={<FlaskConical />}
      title="No test runs yet"
      projectId={projectId}
      deploy={false}
      learn="tests"
    >
      <p>
        Coverage per lane, pass rate and flaky tests for every commit, branch and release appear here once forge records a run
        {branch ? (
          <>
            {" "}
            on {code(branch)}
          </>
        ) : null}
        .
      </p>
      <ol className="space-y-3 pt-2" aria-label="Record a run" data-testid={`${testId}-steps`}>
        <li className="space-y-1">
          <p>From your checkout, run the project&apos;s own test lanes and upload the result:</p>
          <CommandLine command="reliant forge test run && reliant forge test report --upload" />
        </li>
        <li className="space-y-1">
          <p>In CI, wrap each lane&apos;s command, then upload once all lanes have run:</p>
          <CommandLine command="reliant forge test capture --lane unit -- task test:short" />
          <CommandLine command="reliant forge test report --upload" />
        </li>
      </ol>
    </ProjectEmptyCta>
  );
}

/** A Tests page that is coming (W2, W3): what it will show, and where the data is meanwhile. Never fake rows. */
export function TestsPageComing({ title, shows, children, testId }: { title: string; shows: string; children?: ReactNode; testId: string }) {
  return (
    <ProjectEmptyCta testId={testId} icon={<FlaskConical />} title={`${title} is coming`} deploy={false} learn="tests">
      <p>{shows}</p>
      {children}
    </ProjectEmptyCta>
  );
}
