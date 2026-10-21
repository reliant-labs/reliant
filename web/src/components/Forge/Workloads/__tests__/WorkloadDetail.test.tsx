// Copyright (c) 2025 Reliant Labs

/**
 * One workload against the fixtures: the header and its env-naming actions
 * (D1), Restart for hosted envs only (D6), Scale as the declared line plus
 * "Open PR" (D6), the pods, the Config tab (env var names only), the explorer
 * tabs pre-filtered to the workload, a Job's page, and the not-found and error
 * states.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";

import { createRepoMetaFixture, createWorkloadsFixtureApi } from "../__fixtures__/workloadFixtures";
import { eventsInOrder } from "../WorkloadDetail";
import { renderWorkload } from "./workloadsTestUtils";

vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

const ready = async () => waitFor(() => expect(screen.getByTestId("workload-detail")).toHaveAttribute("data-state", "ready"));

describe("a workload", () => {
  it("heads the page with its verdict, environment, kind, replicas and release", async () => {
    renderWorkload("reliant-temporal-worker");
    await ready();
    const page = screen.getByTestId("workload-detail");
    expect(within(page).getByRole("heading", { level: 1 })).toHaveTextContent("reliant-temporal-worker");
    expect(page).toHaveTextContent("Crash-looping");
    expect(page).toHaveTextContent("StatefulSet · 0/2 ready");
    expect(page).toHaveTextContent("restarted 14x, last exit OOMKilled");
    // The breadcrumb leads back to the Overview.
    expect(within(page).getByRole("link", { name: "Overview" })).toHaveAttribute("href", "/overview");
  });

  it("names the environment on Restart, confirms with a neutral primary button, and shows the environment the server echoed", async () => {
    const onRestart = vi.fn().mockResolvedValue({ environmentId: "env-prod", environmentName: "prod", workload: "reliant-temporal-worker", requestedAt: new Date().toISOString() });
    renderWorkload("reliant-temporal-worker", { actions: { onRestart } });
    await ready();
    const button = screen.getByTestId("workload-restart");
    expect(button).toHaveTextContent("Restart in prod");
    await userEvent.click(button);
    const dialog = screen.getByRole("dialog", { name: "Restart reliant-temporal-worker in prod?" });
    const confirm = within(dialog).getByRole("button", { name: "Restart in prod" });
    // D8: the action is the primary (accent) button, never a status colour.
    expect(confirm.className).toContain("bg-accent");
    expect(confirm.className).not.toMatch(/warning|danger/);
    await userEvent.click(confirm);
    expect(onRestart).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.getByTestId("workload-restart-requested")).toHaveTextContent("Restart requested in prod"));
  });

  it("says why a restart did not happen, in the server's words without the protocol prefix", async () => {
    const onRestart = vi.fn().mockRejectedValue(new ConnectError("the caller may not write deployments", Code.PermissionDenied));
    renderWorkload("admin-server", { actions: { onRestart } });
    await ready();
    await userEvent.click(screen.getByTestId("workload-restart"));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Restart in prod" }));
    const failed = await screen.findByTestId("workload-action-failed");
    expect(failed).toHaveTextContent("The restart in prod was not requested: the caller may not write deployments");
    expect(failed.textContent).not.toContain("[permission_denied]");
  });

  it("offers no restart where Reliant does not host the environment, and says why", async () => {
    renderWorkload("admin-server", { actions: { unavailableReason: "Restart and Stop are offered for environments Reliant hosts." }, hosted: null });
    await ready();
    expect(screen.getByTestId("workload-restart")).toBeDisabled();
    expect(screen.queryByTestId("workload-stop")).toBeNull();
    expect(screen.getByTestId("workload-restart-unavailable")).toHaveTextContent("environments Reliant hosts");
  });

  it("stops a hosted workload in its environment, after a confirm that names it", async () => {
    const harness = renderWorkload("admin-server");
    await ready();
    await userEvent.click(screen.getByTestId("workload-stop"));
    expect(screen.getByTestId("workload-stop")).toHaveTextContent("Stop in prod");
    const dialog = screen.getByRole("dialog", { name: "Stop admin-server in prod?" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Stop in prod" }));
    expect(harness.actions.onSetRunState).toHaveBeenCalledWith("dep-admin-server", "suspended");
    expect(await screen.findByTestId("workload-run-state-requested")).toHaveTextContent("Stop requested in prod");
  });

  it("disables Restart on a stopped workload and offers Start in its place", async () => {
    const harness = renderWorkload("docs");
    await ready();
    expect(screen.getByTestId("workload-restart")).toBeDisabled();
    expect(screen.getByTestId("workload-restart")).toHaveAttribute("title", expect.stringMatching(/Start it first/));
    expect(screen.getByTestId("workload-start-first")).toHaveTextContent("Start it first");
    expect(screen.getByTestId("workload-run-state")).toHaveTextContent("Stopped by you");
    await userEvent.click(screen.getByTestId("workload-start"));
    await userEvent.click(within(screen.getByRole("dialog", { name: "Start docs in prod?" })).getByRole("button", { name: "Start in prod" }));
    expect(harness.actions.onSetRunState).toHaveBeenCalledWith("dep-docs", "running");
  });

  it("shows where a hosted workload serves, and the platform's last error while it is not serving", async () => {
    renderWorkload("reliant-temporal-worker");
    await ready();
    expect(screen.queryByTestId("workload-url")).toBeNull();
    expect(screen.getByTestId("workload-last-error")).toHaveTextContent("OOMKilled: exceeded its 1Gi memory limit");
  });

  it("says the workload runs the wrong version when the observed digest is not the published one — never 'declared'", async () => {
    renderWorkload("litellm");
    await ready();
    const header = screen.getByTestId("workload-hosted-facts");
    expect(within(header).getByText("wrong version")).toBeInTheDocument();
    expect(screen.getAllByText("wrong version").length).toBeGreaterThan(0);
    expect(screen.queryByText("declared")).toBeNull();
  });

  it("puts the digest and who updated it in the meta line", async () => {
    renderWorkload("admin-server");
    await ready();
    expect(screen.getByTestId("workload-url")).toHaveAttribute("href", "https://admin.reliantlabs.io");
    expect(screen.getByText("sha256:e9a0c1d2b3f4…")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId("workload-updated")).toHaveTextContent(/updated .* by /));
  });

  it("marks a rollback in the deploy history", async () => {
    renderWorkload("admin-server", { env: "e2e" });
    await ready();
    const history = await screen.findByTestId("workload-deploy-history");
    await waitFor(() => expect(within(history).getAllByRole("listitem").length).toBeGreaterThan(1));
    expect(history.querySelector("[data-rollback=true]")).not.toBeNull();
  });

  it("never scales from the console: it opens a pull request against the repository's default branch", async () => {
    renderWorkload("reliant-temporal-worker");
    await ready();
    await userEvent.click(screen.getByTestId("workload-scale"));
    const popover = screen.getByTestId("workload-scale-popover");
    expect(popover).toHaveTextContent("Replicas are declared in code");
    expect(popover).toHaveTextContent("deploy/kcl/prod/main.k");
    expect(popover).toHaveTextContent("replicas = 2");
    await waitFor(() =>
      expect(within(popover).getByTestId("workload-scale-open-pr")).toHaveAttribute("href", "https://github.com/reliant-labs/control-plane/edit/main/deploy/kcl/prod/main.k")
    );
    expect(within(popover).getByTestId("workload-scale-pr-note")).toHaveTextContent("main, reliant-labs/control-plane's default branch");
    // The view link is the declaration's own commit.
    expect(within(popover).getByTestId("workload-scale-view").getAttribute("href")).toMatch(/\/blob\/[0-9a-f]{40}\/deploy\/kcl\/prod\/main\.k$/);
  });

  it("does not guess a branch: with the default branch unknown it offers View at commit and Copy path only", async () => {
    renderWorkload("reliant-temporal-worker", { repoMeta: createRepoMetaFixture(null) });
    await ready();
    await userEvent.click(screen.getByTestId("workload-scale"));
    const popover = screen.getByTestId("workload-scale-popover");
    await waitFor(() => expect(within(popover).getByTestId("workload-scale-no-pr")).toHaveTextContent("could not learn reliant-labs/control-plane's default branch"));
    expect(within(popover).queryByTestId("workload-scale-open-pr")).toBeNull();
    expect(within(popover).getByTestId("workload-scale-view")).toBeInTheDocument();
    expect(within(popover).getByRole("button", { name: "Copy path" })).toBeInTheDocument();
  });

  it("lists its pods, saying when a node is not reported and when an age is a lower bound", async () => {
    renderWorkload("reliant-temporal-worker");
    await ready();
    const pods = screen.getByTestId("workload-pods");
    const [bad, other] = within(pods).getAllByRole("row").slice(1);
    expect(bad).toHaveTextContent("BackOff");
    expect(bad).toHaveTextContent("OOMKilled");
    expect(other).toHaveTextContent("not reported");
    expect(other).toHaveTextContent("≥24h");
  });

  it("shows the declared spec with environment variables by name only", async () => {
    renderWorkload("reliant-temporal-worker", { search: { tab: "config" } });
    await ready();
    const config = screen.getByTestId("workload-config");
    expect(config).toHaveTextContent("deploy/kcl/prod/main.k");
    expect(config).toHaveTextContent("request 500m · limit 1 core");
    expect(config).toHaveTextContent("limit 1Gi");
    expect(config).toHaveTextContent("8080/tcp");
    expect(screen.getByTestId("workload-config-env")).toHaveTextContent("DATABASE_URL");
  });

  it("says when a bundle recorded no spec, rather than calling the fields undeclared", async () => {
    renderWorkload("litellm", { search: { tab: "config" } });
    await ready();
    expect(screen.getByTestId("workload-config-partial")).toBeInTheDocument();
    expect(screen.getByTestId("workload-config")).toHaveTextContent("not recorded");
  });

  it("orders events warnings first, then newest first", () => {
    const at = (timeMs: number, type: string, reason: string) => ({ timeMs, type, reason, envId: "e", message: "", count: 1, objectKind: "", objectName: "", container: "" });
    expect(eventsInOrder([at(3, "Normal", "Pulled"), at(1, "Warning", "BackOff"), at(2, "Warning", "OOMKilling")]).map((e) => e.reason)).toEqual(["OOMKilling", "BackOff", "Pulled"]);
  });

  it("switches tabs through the URL, dropping an explorer's keys on leaving it", async () => {
    const harness = renderWorkload("admin-server", { search: { tab: "logs", q: "timeout", range: "6h" } });
    await ready();
    await userEvent.click(screen.getByRole("tab", { name: "Events" }));
    expect(harness.current()).toEqual({ tab: "events", range: "6h" });
  });
});

describe("a Job", () => {
  it("shows its runs instead of pods, and offers neither Restart nor Scale", async () => {
    renderWorkload("control-plane-migrate", { env: "e2e" });
    await ready();
    expect(screen.getByTestId("workload-detail")).toHaveAttribute("data-kind", "job");
    const runs = screen.getByTestId("workload-runs");
    expect(within(runs).getAllByRole("row")).toHaveLength(1 + 2);
    expect(runs).toHaveTextContent("Failed· BackoffLimitExceeded");
    expect(screen.queryByTestId("workload-restart")).toBeNull();
    expect(screen.queryByTestId("workload-scale")).toBeNull();
    expect(screen.queryByRole("tab", { name: "Profiles" })).toBeNull();
  });
});

describe("its states", () => {
  it("says a self-managed environment's declared workload is unobserved, not missing", async () => {
    renderWorkload("admin-server", { env: "staging" });
    await waitFor(() => expect(screen.getByTestId("workload-detail")).toHaveAttribute("data-state", "unobserved"));
    expect(screen.getByText("admin-server is declared in staging, and nothing has observed it")).toBeInTheDocument();
  });

  it("says a workload does not run in this environment, with the way back", async () => {
    renderWorkload("nonexistent");
    await waitFor(() => expect(screen.getByTestId("workload-detail")).toHaveAttribute("data-state", "not-found"));
    expect(screen.getByText("No workload named nonexistent in prod")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to Overview" })).toHaveAttribute("href", "/overview");
  });

  it("loads in the page's own shape", () => {
    renderWorkload("admin-server", { obs: createWorkloadsFixtureApi("loading") });
    expect(screen.getByTestId("workload-detail")).toHaveAttribute("data-state", "loading");
  });

  it("names what failed when telemetry cannot be read", async () => {
    renderWorkload("admin-server", { obs: createWorkloadsFixtureApi("error") });
    // Unavailable is retried once before the page gives up.
    await waitFor(() => expect(screen.getByTestId("workload-detail")).toHaveAttribute("data-state", "error"), { timeout: 5000 });
    expect(screen.getByRole("button", { name: /try again|retry/i })).toBeInTheDocument();
  });
});
