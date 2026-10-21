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

import { createWorkloadsFixtureApi } from "../__fixtures__/workloadFixtures";
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

  it("names the environment on Restart, confirms, and shows the environment the server echoed", async () => {
    const onRestart = vi.fn().mockResolvedValue({ environmentId: "env-prod", environmentName: "prod", workload: "reliant-temporal-worker", requestedAt: new Date().toISOString() });
    renderWorkload("reliant-temporal-worker", { restart: { onRestart, unavailableReason: null } });
    await ready();
    const button = screen.getByTestId("workload-restart");
    expect(button).toHaveTextContent("Restart in prod");
    await userEvent.click(button);
    const dialog = screen.getByRole("dialog", { name: "Restart reliant-temporal-worker in prod?" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Restart in prod" }));
    expect(onRestart).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.getByTestId("workload-restart-requested")).toHaveTextContent("Restart requested in prod"));
  });

  it("says why a restart did not happen", async () => {
    const onRestart = vi.fn().mockRejectedValue(new ConnectError("permission denied", Code.PermissionDenied));
    renderWorkload("admin-server", { restart: { onRestart, unavailableReason: null } });
    await ready();
    await userEvent.click(screen.getByTestId("workload-restart"));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Restart in prod" }));
    await waitFor(() => expect(screen.getByTestId("workload-restart-failed")).toHaveTextContent("The restart in prod was not requested"));
  });

  it("offers no restart where Reliant does not host the environment, and says why", async () => {
    renderWorkload("admin-server", { restart: { onRestart: vi.fn(), unavailableReason: "Restart is offered for environments Reliant hosts." } });
    await ready();
    expect(screen.getByTestId("workload-restart")).toBeDisabled();
    expect(screen.getByTestId("workload-restart-unavailable")).toHaveTextContent("environments Reliant hosts");
  });

  it("never scales from the console: it shows the declared line and opens a pull request against it", async () => {
    renderWorkload("reliant-temporal-worker");
    await ready();
    await userEvent.click(screen.getByTestId("workload-scale"));
    const popover = screen.getByTestId("workload-scale-popover");
    expect(popover).toHaveTextContent("Replicas are declared in code");
    expect(popover).toHaveTextContent("deploy/kcl/prod/main.k");
    expect(popover).toHaveTextContent("replicas = 2");
    // The release was cut from main: GitHub's editor on that branch proposes the change as a PR.
    await waitFor(() =>
      expect(within(popover).getByTestId("workload-scale-open-pr")).toHaveAttribute("href", "https://github.com/reliant-labs/control-plane/edit/main/deploy/kcl/prod/main.k")
    );
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
