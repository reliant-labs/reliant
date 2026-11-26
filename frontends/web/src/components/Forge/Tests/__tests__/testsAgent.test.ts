// Copyright (c) 2026 Reliant Labs

/**
 * The Tests agent subjects (testsAgent.ts) and their prompts
 * (askReliantPrompt.ts `flaky-test` and `coverage`), against the fixture
 * TestsService: what each chat is seeded with, that the coverage prompt
 * carries line numbers and never a line of source, and when the actions are
 * disabled.
 */

import { describe, expect, it } from "vitest";

import type { LineCoverage, TestFlake } from "@/services/controlPlane/tests/types";
import { testDetailOf } from "@/services/controlPlane/tests/wire";

import { askAgentCommands, askWorkspaceName, buildAskReliantPrompt, goRunPattern, MAX_SAMPLE_OUTPUT_CHARS } from "../../Observability/AskReliant/askReliantPrompt";
import { FIXTURE_REFUND_PATH, FIXTURE_REFUND_SOURCE, fixtureCoverage, fixtureFileCoverage } from "../coverageFixtures";
import { FIXTURE_PROJECT } from "../fixtures";
import { fixtureTestWire } from "../runFlakeFixtures";
import { AGENT_REASON, agentAvailability, coverageSubject, defaultCoverageTarget, filesToRead, flakyTestSubject, uncoveredRanges } from "../testsAgent";

const MODULE = "github.com/acme/shopfront";
const idOf = (dir: string, name: string) => `go:${MODULE}/${dir}:${name}`;
const COMMIT = "9f3c2a17be0d4c55a1f0e9d8c7b6a5f4e3d2c1b0";

async function syncJob() {
  return testDetailOf(fixtureTestWire("report", { orgId: null, project: FIXTURE_PROJECT, testId: idOf("internal/jobs", "TestSyncJob") }));
}

describe("agentAvailability", () => {
  it("in Reliant, needs a linked git repository and says why not; in handoff mode, needs no project", () => {
    expect(agentAvailability({ mode: "chat", projectId: "p1", project: { is_git_repo: true } })).toEqual({ available: true });
    // A project the list has not answered for yet is resolved again when the action starts.
    expect(agentAvailability({ mode: "chat", projectId: "p1", project: undefined })).toEqual({ available: true });
    // The forge shell and demos open the "Ask an agent" sheet, which touches no project.
    expect(agentAvailability({ mode: "handoff", projectId: null, project: undefined })).toEqual({ available: true });
    expect(agentAvailability({ mode: "handoff", projectId: "p1", project: { is_git_repo: false } })).toEqual({ available: true });
    expect(agentAvailability({ mode: "chat", projectId: null, project: undefined })).toEqual({ available: false, reason: AGENT_REASON.noProject });
    expect(agentAvailability({ mode: "chat", projectId: "p1", project: { is_git_repo: false } })).toEqual({ available: false, reason: AGENT_REASON.notGit });
  });
});

describe("the flaky-test subject", () => {
  it("seeds the chat with the test, its state, flake rate and n, signatures with output, quarantine, issue and the instruction", async () => {
    const detail = await syncJob();
    const subject = flakyTestSubject(detail.flake!, detail, "main");
    expect(subject).toMatchObject({ kind: "flaky-test", testId: idOf("internal/jobs", "TestSyncJob"), state: "flaky", runs: 412, confirmedEvents30d: 3, issueUrl: "https://github.com/acme/shopfront/issues/212" });
    expect(subject.signatures.length).toBeGreaterThan(0);

    const prompt = buildAskReliantPrompt(subject, { forgeProject: "shopfront", intent: "fix" });
    expect(prompt).toContain(`Flaky test ${idOf("internal/jobs", "TestSyncJob")} (forge project shopfront).`);
    expect(prompt).toContain("State: flaky. Flake rate 1.1% over 412 default-branch runs (a ranking, not a verdict); 3 confirmed flake events in 30 days.");
    expect(prompt).toContain("Issue: https://github.com/acme/shopfront/issues/212");
    expect(prompt).toContain("Not quarantined.");
    // The sample output, fenced, with the server's scrubbing said.
    expect(prompt).toContain("scrubbed of known secret shapes when it was recorded");
    expect(prompt).toContain(`~~~\n${detail.signatures[0].sampleOutput.trimEnd()}\n~~~`);
    // The instruction: fix or delete, never sleep/retry, verify under -race -count=50 and the flake gate, drop the quarantine entry.
    expect(prompt).toContain("make the test deterministic, or delete it");
    expect(prompt).toContain("Never add a sleep, a retry or a looser assertion");
    expect(prompt).toContain(`\`go test -race -count=50 -run '^TestSyncJob$' ${MODULE}/internal/jobs\` must pass 50 runs of 50`);
    expect(prompt).toContain("`reliant forge test flakes gate --base origin/main` passes");
    expect(prompt).toContain(`If it has a quarantine entry by then, remove it in the same change: \`reliant forge test quarantine remove ${idOf("internal/jobs", "TestSyncJob")}\``);
    // Its own instruction, not Investigate's or the generic fix's.
    expect(prompt).toContain("This chat has a workspace of its own on its own git branch");
    expect(prompt).not.toContain("root cause");
  });

  it("names a quarantine entry, with its expiry, as the one to remove", () => {
    const flake = {
      test: { id: idOf("internal/inventory", "TestInventoryReserve"), lang: "go", package: `${MODULE}/internal/inventory`, name: "TestInventoryReserve", file: "internal/inventory/reserve_test.go", line: 40 },
      state: "quarantined",
      score: 0.02,
      scoreRuns: 90,
      confirmedEvents30d: 2,
      signals: [],
      firstSeenMs: null,
      lastEventMs: null,
      quarantinedMs: null,
      quarantine: { path: "ci/quarantine/go/internal/inventory/TestInventoryReserve.yaml", issueUrl: "https://github.com/acme/shopfront/issues/9", reason: "races the clock", signature: "", expiresMs: Date.UTC(2026, 10, 1), overdue: true },
      issueUrl: "",
    } satisfies TestFlake;
    const subject = flakyTestSubject(flake, { signatures: [] });
    expect(subject.issueUrl).toBe("https://github.com/acme/shopfront/issues/9");
    const prompt = buildAskReliantPrompt(subject, { intent: "fix" });
    expect(prompt).toContain("Package github.com/acme/shopfront/internal/inventory, internal/inventory/reserve_test.go:40.");
    expect(prompt).toContain("Quarantined by ci/quarantine/go/internal/inventory/TestInventoryReserve.yaml (reason: races the clock), expired 2026-11-01 and overdue.");
    expect(prompt).toContain(`Remove its quarantine entry in the same change: \`reliant forge test quarantine remove ${idOf("internal/inventory", "TestInventoryReserve")}\``);
    expect(prompt).toContain("--base origin/<default branch>");
    expect(prompt).toContain("No failure output was recorded for it.");
    expect(askAgentCommands(subject).map((c) => c.verb)).toEqual(["go test", "test flakes gate", "test quarantine remove"]);
  });

  it("bounds and fences the sample output so it reads as data", async () => {
    const detail = await syncJob();
    const long = `${"x".repeat(MAX_SAMPLE_OUTPUT_CHARS + 500)}`;
    const subject = flakyTestSubject(detail.flake!, { signatures: [{ ...detail.signatures[0], sampleOutput: `~~~~ closing?\u001b[31m\n${long}` }] });
    const prompt = buildAskReliantPrompt(subject);
    expect(prompt).not.toContain("\u001b");
    expect(prompt).toContain("… (truncated)");
    expect(prompt).not.toContain("x".repeat(MAX_SAMPLE_OUTPUT_CHARS + 1));
    // A fence longer than any run of tildes inside it.
    expect(prompt).toContain("~~~~~\n~~~~ closing?");
  });

  it("runs a subtest by its anchored, regexp-quoted path", () => {
    expect(goRunPattern("TestWebhookSign/rotated key")).toBe("^TestWebhookSign$/^rotated_key$");
    expect(goRunPattern("TestParse/a.b(c)")).toBe("^TestParse$/^a\\.b\\(c\\)$");
    expect(askWorkspaceName(flakyTestSubject({ ...({} as TestFlake), test: { id: "go:x:TestWebhookSign/rotated key", lang: "go", package: "x", name: "TestWebhookSign/rotated key", file: "", line: 0 }, quarantine: null, issueUrl: "", state: "suspected", score: 0, scoreRuns: 0, confirmedEvents30d: 1 }, null), "ab12", "fix")).toBe(
      "fix-flaky-test-testwebhooksign-ab12"
    );
  });
});

const line = (n: number, statements: number, hits: Record<string, number>): LineCoverage => ({ line: n, statements, hits, partial: false, changed: false });

describe("the coverage subject", () => {
  it("collapses uncovered lines into ranges, skipping covered and statement-less lines", () => {
    expect(uncoveredRanges([line(3, 1, {}), line(4, 1, { unit: 0 }), line(5, 2, {}), line(7, 1, {}), line(8, 1, { e2e: 2 }), line(9, 0, {}), line(10, 1, {})])).toEqual([
      [3, 5],
      [7, 7],
      [10, 10],
    ]);
  });

  it("targets the next ten above where the package is", () => {
    expect(defaultCoverageTarget(58)).toBe(60);
    expect(defaultCoverageTarget(60)).toBe(70);
    expect(defaultCoverageTarget(96.4)).toBe(100);
    expect(defaultCoverageTarget(null)).toBe(50);
  });

  it("seeds the chat with the package, its combined and per-lane figures, uncovered line numbers, the target and the instruction — never source", async () => {
    const target = { gitCommit: COMMIT };
    const tree = await fixtureCoverage({ orgId: null, project: FIXTURE_PROJECT, target, path: "internal/billing" });
    const paths = filesToRead(tree);
    expect(paths[0]).toBe("internal/billing/tax.go");
    const files = await Promise.all(paths.map((path) => fixtureFileCoverage({ orgId: null, project: FIXTURE_PROJECT, target, path })));
    const subject = coverageSubject({ pkg: "internal/billing", tree, files, defaultBranch: "main" });
    expect(subject).toMatchObject({ kind: "coverage", package: "internal/billing", combined: 58, target: 60, moreFiles: 0, lanes: { unit: 51.2, integration: 30.1, e2e: 44 } });
    expect(subject.uncovered.find((file) => file.path === FIXTURE_REFUND_PATH)?.lines).toEqual([
      [17, 17],
      [23, 23],
      [32, 32],
    ]);

    const prompt = buildAskReliantPrompt(subject, { forgeProject: "shopfront", intent: "fix" });
    expect(prompt).toContain("Raise the test coverage of internal/billing (forge project shopfront), in the report of commit 9f3c2a17be0d.");
    expect(prompt).toContain("Now 58.0% combined (1,340 of 2,310 statements); by lane: unit 51.2%, integration 30.1%, e2e 44.0%.");
    expect(prompt).toContain("Target: 60% combined.");
    expect(prompt).toContain(`- ${FIXTURE_REFUND_PATH}: 17, 23, 32`);
    expect(prompt).toContain("Add tests that assert behaviour");
    expect(prompt).toContain("`task test -- ./internal/billing/...`");
    expect(prompt).toContain("`reliant forge test coverage internal/billing --json` must reach the target");
    expect(prompt).toContain("`reliant forge test flakes gate --base origin/main`");

    // No source: not one line of refund.go's code reaches the prompt or the subject.
    const serialized = JSON.stringify(subject);
    for (const code of FIXTURE_REFUND_SOURCE.split("\n").filter((text) => text.trim().length > 3)) {
      expect(prompt).not.toContain(code.trim());
      expect(serialized).not.toContain(code.trim());
    }
    expect(serialized).not.toMatch(/blob/i);
  });

  it("lists only the files it read, and names the command for the rest", async () => {
    const target = { gitCommit: COMMIT };
    const tree = await fixtureCoverage({ orgId: null, project: FIXTURE_PROJECT, target, path: "internal/billing" });
    const first = await fixtureFileCoverage({ orgId: null, project: FIXTURE_PROJECT, target, path: filesToRead(tree)[0] });
    const subject = coverageSubject({ pkg: "internal/billing", tree, files: [first] });
    expect(subject.moreFiles).toBe(2);
    const prompt = buildAskReliantPrompt(subject);
    expect(prompt).toContain("(and 2 more files: `reliant forge test coverage internal/billing --by file` lists them)");
    expect(askAgentCommands(subject).map((c) => c.command)).toEqual(["reliant forge test coverage internal/billing --by file", "reliant forge test flakes gate --base origin/<default branch>"]);
  });
});
