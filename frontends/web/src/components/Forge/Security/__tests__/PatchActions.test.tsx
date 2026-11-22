// Copyright (c) 2025 Reliant Labs

/**
 * Patch and Patch with agent on the Vulnerabilities table and on an
 * advisory's page: when they are enabled, the reason they are not, the exact
 * run they start (ChatService.RunNow of builtin://vuln-patch in the
 * repository's project, with vuln-patch's own input names), and the link to
 * that run once it has started.
 */

import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { FIXTURE_ENVS, FIXTURE_PROJECT, fixtureVulnerabilityApi } from "../fixtures";
import { VulnerabilityView } from "../VulnerabilityPage";
import type { VulnPatchAccess } from "../vulnPatchModel";
import { renderVulnerabilities, renderWith } from "./securityTestUtils";

const runNow = vi.hoisted(() => vi.fn());
vi.mock("@/hooks/chat-actions", () => ({ runNow }));

vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

const READY: VulnPatchAccess = { state: "ready", projectId: "proj-1", projectPath: "services/shop" };

beforeEach(() => {
  runNow.mockReset();
  runNow.mockResolvedValue({ chat: { id: "chat-9", projectId: "proj-1" }, workflow_id: "wf-9", run_id: "run-9" });
});

const patchButton = () => screen.getByTestId("vuln-patch-deterministic");
const agentButton = () => screen.getByTestId("vuln-patch-agent");
const reason = () => screen.getByTestId("vuln-patch-reason");
const select = async (advisoryId: string) => userEvent.click(within(screen.getByTestId(`vuln-row-${advisoryId}`)).getByRole("checkbox"));

describe("Patch on the Vulnerabilities table", () => {
  it("is disabled until a fixable finding is selected, and says why", async () => {
    renderVulnerabilities(fixtureVulnerabilityApi("findings"), { environmentId: FIXTURE_ENVS.prod.id, patch: { access: READY } });
    await screen.findByTestId("vuln-list");
    expect(patchButton()).toBeDisabled();
    expect(agentButton()).toBeDisabled();
    expect(reason()).toHaveTextContent("Select the findings to patch.");

    await select("CVE-2025-1180"); // won't be fixed
    expect(patchButton()).toBeDisabled();
    expect(reason()).toHaveTextContent("None of the selected findings has a fixed version to move to.");

    await select("CVE-2026-4101");
    expect(patchButton()).toBeEnabled();
    expect(agentButton()).toBeEnabled();
    expect(screen.queryByTestId("vuln-patch-reason")).not.toBeInTheDocument();
    expect(screen.getByTestId("vuln-patch-scope")).toHaveTextContent("CVE-2026-4101 in prod; 1 selected has no fix and is left out");
  });

  it("starts vuln-patch in the repository's project with exactly its inputs, then links to the run", async () => {
    renderVulnerabilities(fixtureVulnerabilityApi("findings"), { environmentId: FIXTURE_ENVS.prod.id, patch: { access: READY } });
    await screen.findByTestId("vuln-list");
    await select("CVE-2026-4101");
    await select("GHSA-c2qf-rxjj-qqgw");
    await select("CVE-2025-9912"); // no fix yet: left out
    await userEvent.click(patchButton());

    expect(runNow).toHaveBeenCalledTimes(1);
    expect(runNow).toHaveBeenCalledWith({
      projectId: "proj-1",
      workflow: "builtin://vuln-patch",
      message: "Patch CVE-2026-4101, GHSA-c2qf-rxjj-qqgw in prod.",
      inputs: { project: "services/shop", env: "prod", advisories: ["CVE-2026-4101", "GHSA-c2qf-rxjj-qqgw"], patch_mode: "deterministic" },
    });
    expect(await screen.findByTestId("vuln-patch-started")).toHaveTextContent("Patch started.");
    expect(screen.getByTestId("vuln-patch-run-link")).toHaveAttribute("href", "/workflows/runs/chat-9?project=proj-1");
  });

  it("Patch with agent sends patch_mode agent", async () => {
    renderVulnerabilities(fixtureVulnerabilityApi("findings"), { environmentId: FIXTURE_ENVS.prod.id, patch: { access: READY } });
    await screen.findByTestId("vuln-list");
    await select("GHSA-c2qf-rxjj-qqgw");
    await userEvent.click(agentButton());
    expect(runNow).toHaveBeenCalledWith({
      projectId: "proj-1",
      workflow: "builtin://vuln-patch",
      message: "Patch GHSA-c2qf-rxjj-qqgw in prod with an agent.",
      inputs: { project: "services/shop", env: "prod", advisories: ["GHSA-c2qf-rxjj-qqgw"], patch_mode: "agent" },
    });
    expect(await screen.findByTestId("vuln-patch-run-link")).toHaveAttribute("href", "/workflows/runs/chat-9?project=proj-1");
  });

  it("says why a start failed, and links to no run", async () => {
    runNow.mockRejectedValueOnce(new Error("no machine can run this project"));
    renderVulnerabilities(fixtureVulnerabilityApi("findings"), { environmentId: FIXTURE_ENVS.prod.id, patch: { access: READY } });
    await screen.findByTestId("vuln-list");
    await select("CVE-2026-4101");
    await userEvent.click(patchButton());
    expect(await screen.findByTestId("vuln-patch-error")).toHaveTextContent("The patch did not start: no machine can run this project");
    expect(screen.queryByTestId("vuln-patch-run-link")).not.toBeInTheDocument();
  });

  it("is disabled with the project's reason whatever is selected", async () => {
    const blocked: VulnPatchAccess = { state: "blocked", reason: "Connect GitHub to patch: a patch pushes a branch and opens its pull request with your GitHub account." };
    renderVulnerabilities(fixtureVulnerabilityApi("findings"), { environmentId: FIXTURE_ENVS.prod.id, patch: { access: blocked } });
    await screen.findByTestId("vuln-list");
    await select("CVE-2026-4101");
    expect(patchButton()).toBeDisabled();
    expect(agentButton()).toBeDisabled();
    expect(reason()).toHaveTextContent("Connect GitHub to patch");
    expect(patchButton()).toHaveAttribute("title", blocked.reason);
  });

  it("asks for one environment when the scope is every environment", async () => {
    renderVulnerabilities(fixtureVulnerabilityApi("findings"), { patch: { access: READY } });
    await screen.findByTestId("vuln-list");
    await select("CVE-2026-4101");
    expect(patchButton()).toBeDisabled();
    expect(reason()).toHaveTextContent("Choose one environment in the top bar");
  });

  it("offers no selection or actions where patching is not offered", async () => {
    renderVulnerabilities(fixtureVulnerabilityApi("findings"), { environmentId: FIXTURE_ENVS.prod.id });
    await screen.findByTestId("vuln-list");
    expect(screen.queryByTestId("vuln-patch")).not.toBeInTheDocument();
    expect(within(screen.getByTestId("vuln-row-CVE-2026-4101")).queryByRole("checkbox")).not.toBeInTheDocument();
  });
});

describe("Patch on an advisory's page", () => {
  const renderAdvisory = (advisoryId: string, patch: { access: VulnPatchAccess; env: string | null; environmentId: string | null }) =>
    renderWith(
      fixtureVulnerabilityApi("findings"),
      <VulnerabilityView
        project={FIXTURE_PROJECT}
        advisoryId={advisoryId}
        backHref="/forge/p/shop/security/vulnerabilities"
        releaseHref={(v) => `/forge/p/shop/releases/${v}`}
        patch={patch}
      />
    );

  it("patches the advisory in the scoped environment", async () => {
    renderAdvisory("CVE-2026-4101", { access: READY, env: "prod", environmentId: FIXTURE_ENVS.prod.id });
    await screen.findByTestId("vuln-advisory");
    await waitFor(() => expect(agentButton()).toBeEnabled());
    await userEvent.click(agentButton());
    expect(runNow).toHaveBeenCalledWith({
      projectId: "proj-1",
      workflow: "builtin://vuln-patch",
      message: "Patch CVE-2026-4101 in prod with an agent.",
      inputs: { project: "services/shop", env: "prod", advisories: ["CVE-2026-4101"], patch_mode: "agent" },
    });
    expect(await screen.findByTestId("vuln-patch-run-link")).toHaveAttribute("href", "/workflows/runs/chat-9?project=proj-1");
  });

  it("is disabled with a reason when the advisory has no fix there", async () => {
    renderAdvisory("CVE-2025-1180", { access: READY, env: "prod", environmentId: FIXTURE_ENVS.prod.id });
    await screen.findByTestId("vuln-advisory");
    expect(patchButton()).toBeDisabled();
    expect(reason()).toHaveTextContent("No fixed version of it exists for what prod runs.");
  });

  it("asks for one environment when the scope is every environment", async () => {
    renderAdvisory("CVE-2026-4101", { access: READY, env: null, environmentId: null });
    await screen.findByTestId("vuln-advisory");
    expect(patchButton()).toBeDisabled();
    expect(reason()).toHaveTextContent("Choose one environment in the top bar");
  });
});
