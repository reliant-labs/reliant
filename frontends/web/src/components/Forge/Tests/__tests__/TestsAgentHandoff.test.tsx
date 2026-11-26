// Copyright (c) 2026 Reliant Labs

/**
 * The Tests agent buttons in handoff mode (the forge shell, a demo), with the
 * real Ask Reliant: like every Ask Reliant surface there, one "Ask an agent"
 * button that opens the handoff sheet with the subject's prompt and its forge
 * commands, and needs no Reliant project, so it is never disabled for want of one.
 */

import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";

import { AskAgentModeProvider } from "../../Observability/AskReliant/askAgentMode";
import { fixtureCoverageSource } from "../coverageFixtures";
import { FIXTURE_NOW, FIXTURE_PROJECT, fixtureTests } from "../fixtures";
import { TestsCoverageView } from "../TestsCoveragePage";
import { TestsFlakyTestView } from "../TestsFlakyTestPage";
import { CoverageSourceProvider } from "@/services/coverageSource/CoverageSourceContext";
import { renderWithTests } from "./testsTestUtils";

vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

const MODULE = "github.com/acme/shopfront";
const SYNC_JOB = `go:${MODULE}/internal/jobs:TestSyncJob`;

const handoff = (children: ReactNode) => (
  <AskAgentModeProvider mode="handoff" cli="forge">
    {children}
  </AskAgentModeProvider>
);

describe("Tests agent buttons in handoff mode", () => {
  it("Fix this flaky test is Ask an agent, opening the sheet with the test's prompt and commands, with no Reliant project", async () => {
    renderWithTests(fixtureTests("report"), handoff(<TestsFlakyTestView project={FIXTURE_PROJECT} projectId={null} testId={SYNC_JOB} runHref={(id) => `/runs/${id}`} now={FIXTURE_NOW} />));
    const button = await screen.findByTestId("tests-fix-with-agent");
    expect(button).toBeEnabled();
    expect(button).toHaveTextContent("Ask an agent");
    expect(screen.queryByTestId("tests-fix-with-agent-reason")).not.toBeInTheDocument();
    fireEvent.click(button);

    const sheet = await screen.findByTestId("ask-agent-sheet");
    expect(sheet).toHaveAttribute("data-subject-kind", "flaky-test");
    const prompt = within(sheet).getByTestId("ask-agent-prompt");
    expect(prompt).toHaveTextContent(`Flaky test ${SYNC_JOB}`);
    expect(prompt).toHaveTextContent("go test -race -count=50 -run '^TestSyncJob$'");
    // The reader's own agent: it is told to branch, not that it has a workspace.
    expect(prompt).toHaveTextContent("Do the work on a new branch.");
    expect(sheet).toHaveTextContent("forge test flakes gate --base origin/main");
  });

  it("Raise coverage is Ask an agent too, its sheet carrying the package's figures and the coverage command", async () => {
    renderWithTests(
      fixtureTests(),
      handoff(
        <CoverageSourceProvider fetch={fixtureCoverageSource()}>
          <TestsCoverageView scope={{ project: FIXTURE_PROJECT, environmentId: undefined }} search={{ path: "internal/billing" }} hrefFor={() => "#"} onSelect={() => {}} />
        </CoverageSourceProvider>
      )
    );
    const raise = await screen.findByTestId("tests-coverage-raise");
    await screen.findAllByTestId("tests-coverage-row");
    const button = within(raise).getByRole("button");
    expect(button).toBeEnabled();
    expect(button).toHaveTextContent("Ask an agent");
    fireEvent.click(button);

    const sheet = await screen.findByTestId("ask-agent-sheet");
    expect(sheet).toHaveAttribute("data-subject-kind", "coverage");
    expect(within(sheet).getByTestId("ask-agent-prompt")).toHaveTextContent("Raise the test coverage of internal/billing");
    expect(within(sheet).getByTestId("ask-agent-prompt")).toHaveTextContent("Target: 60% combined.");
    expect(sheet).toHaveTextContent("forge test coverage internal/billing --by file");
  });
});
