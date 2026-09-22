// Copyright (c) 2025 Reliant Labs

/**
 * THE VISUAL CONTRACT TESTS for the cluster-workload inventory.
 *
 * These pin the two properties the screen exists to guarantee, both of which
 * were violated by the screen it replaces:
 *
 *   1. IT SHOWS THE CLUSTER, NOT THE LAPTOP. The old screen rendered the
 *      env-status report's `services` array — host processes — so prod showed
 *      two local dev servers while the cluster ran sixteen workloads. The
 *      fixtures carry BOTH arrays at their real sizes, so a regression that
 *      reaches for the wrong one changes a count the tests read.
 *
 *   2. UNKNOWN IS NOT EMPTY AND NOT GREEN. An unreachable cluster still lists
 *      every workload, so a renderer that branches on `workloads.length`
 *      rather than on `status` produces a full, confident, WRONG table. That
 *      is the subtlest available failure and it gets the most assertions.
 *
 * Assertions are on RENDERED DISTINCTIONS, not merely on attributes — a
 * regression that kept `data-posture` honest while painting unknown rows as
 * measured would sail through an attribute-only test — so the treatments are
 * compared against each other rather than against hardcoded class strings.
 */

import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { WorkloadInventory } from "../WorkloadInventory";
import { classifyForgeResponse, type ForgeOutcome } from "@/services/forge/topology";
import type { ForgeEnvStatusReport } from "@/services/forge/status";
import { ForgeReachability } from "@/gen/reliant/v1/forge_pb";
import type { ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";

import {
  deploysNothing,
  devTwoClusters,
  noWorkloadsKey,
  prodReachable,
  prodUnreachable,
  PROD_UNREACHABLE_ERROR,
} from "./fixtures";

function meta(overrides: Partial<ForgeReportMeta> = {}): ForgeReportMeta {
  return {
    isForgeProject: true,
    supported: true,
    forgeVersion: "v0.9.1",
    unsupportedReason: "",
    exitCode: 0,
    reachability: ForgeReachability.UNSPECIFIED,
    unreachableReason: "",
    ...overrides,
  } as ForgeReportMeta;
}

function renderInventory(report: ForgeEnvStatusReport, env = "prod") {
  const outcome: ForgeOutcome<ForgeEnvStatusReport> = {
    kind: "report",
    meta: meta(),
    report,
  };
  return render(
    <WorkloadInventory outcome={outcome} isLoading={false} env={env} projectName="control-plane" />
  );
}

describe("WorkloadInventory — it reports the cluster, not the laptop", () => {
  it("renders all 16 cluster workloads, not the 2 host services", () => {
    const report = prodReachable();
    // The fixture really does carry both, at the sizes that caused the bug.
    expect(report.services).toHaveLength(2);
    expect(report.workloads?.workloads).toHaveLength(16);

    renderInventory(report);

    const table = screen.getByTestId("workloads-table");
    // Every workload name is on screen…
    for (const workload of report.workloads!.workloads!) {
      expect(within(table).getByText(workload.name!)).toBeInTheDocument();
    }
    // …and the table really has 16 body rows, not 2. Counting rows rather
    // than finding the string "16" is what makes this fail if the renderer
    // reaches for `services` again: that array has two entries.
    expect(within(table).getAllByRole("row")).toHaveLength(16 + 1); // + header
    expect(screen.getByTestId("workloads-sentence")).toHaveTextContent("16 workloads");
    // None of the host-only facts leak onto this surface.
    expect(within(table).queryByText("8080")).not.toBeInTheDocument();
  });

  it("names the cluster and namespace the data was read from", () => {
    renderInventory(prodReachable());
    const scopes = screen.getByTestId("workload-scopes");
    expect(
      within(scopes).getByText("gke_reliant-labs-475814_us-central1_prod")
    ).toBeInTheDocument();
    expect(within(scopes).getByText("/control-plane-prod")).toBeInTheDocument();
    // And in the sentence, so the count is never stated without its location.
    expect(screen.getByTestId("workloads-sentence")).toHaveTextContent(
      "gke_reliant-labs-475814_us-central1_prod/control-plane-prod"
    );
  });
});

describe("WorkloadInventory — unknown is neither empty nor green", () => {
  it("lists all 16 workloads when the cluster could not be read", () => {
    renderInventory(prodUnreachable());

    // NOT an empty screen. This is the assertion that fails for any renderer
    // that treats an unreadable cluster as "nothing deployed".
    expect(screen.queryByTestId("workloads-empty")).not.toBeInTheDocument();
    const table = screen.getByTestId("workloads-table");
    expect(within(table).getByText("admin-api")).toBeInTheDocument();
    expect(within(table).getByText("zitadel")).toBeInTheDocument();

    expect(screen.getByTestId("workload-inventory")).toHaveAttribute(
      "data-posture",
      "undetermined"
    );
    expect(screen.getByTestId("workloads-sentence")).toHaveTextContent("16 workloads");
    expect(screen.getByTestId("workloads-sentence")).toHaveTextContent("could not read");
  });

  it("gives unknown rows a hollow dot where measured rows get a filled one", () => {
    const { unmount } = renderInventory(prodReachable());
    const measuredDot = screen.getAllByTestId("workloads-table")[0].querySelector("[data-status-dot]");
    const measuredClasses = measuredDot!.className;
    expect(measuredDot).toHaveAttribute("data-measured", "true");
    unmount();

    renderInventory(prodUnreachable());
    const unknownDot = screen
      .getByTestId("workloads-table")
      .querySelector("[data-status-dot]");
    expect(unknownDot).toHaveAttribute("data-measured", "false");

    // The distinction is compared BETWEEN the two, not against a literal:
    // the axis must survive greyscale, so it is fill vs. no fill.
    expect(unknownDot!.className).not.toEqual(measuredClasses);
    expect(measuredClasses).toMatch(/\bbg-(success|warning|destructive)\b/);
    expect(unknownDot!.className).toContain("bg-transparent");
    expect(unknownDot!.className).toContain("ring-");
  });

  it("shows kubectl's verbatim error and the count of things it could not see", () => {
    renderInventory(prodUnreachable());
    expect(screen.getByTestId("workload-scope-error")).toHaveTextContent(
      PROD_UNREACHABLE_ERROR
    );
    const scope = screen.getByTestId("workload-scope");
    expect(scope).toHaveAttribute("data-scope-status", "unknown");
    expect(within(scope).getByText("16 workloads")).toBeInTheDocument();
  });

  it("does not print a replica or restart count it never measured", () => {
    renderInventory(prodUnreachable());
    const table = screen.getByTestId("workloads-table");
    // ready_replicas is 0 in the document because nothing was READ. Rendering
    // "0/1" here is what makes an unread cluster look like a total outage.
    expect(table.querySelector('[data-replicas="counted"]')).toBeNull();
    expect(table.querySelectorAll('[data-replicas="unmeasured"]').length).toBe(16);
    expect(within(table).queryByText("0/1")).not.toBeInTheDocument();
    expect(within(table).queryByText("0/2")).not.toBeInTheDocument();
  });

  it("collapses one repeated finding into a single grouped entry", () => {
    renderInventory(prodUnreachable());
    // All 16 carry the same sentence. Sixteen copies would bury how many
    // distinct problems there actually are — which is one.
    const findings = screen.getAllByTestId("workload-finding");
    expect(findings).toHaveLength(1);
    expect(findings[0]).toHaveTextContent("16 workloads");
  });
});

describe("WorkloadInventory — the inventory is complete, and says so", () => {
  it("shows every workload with no pagination chrome claiming otherwise", () => {
    renderInventory(prodReachable());
    const table = screen.getByTestId("workloads-table");

    // All 16 on one page…
    expect(within(table).getAllByRole("row")).toHaveLength(17);
    // …and nothing suggesting a page two. "Rows per page: 10" above sixteen
    // rows is a claim the list is truncated, which is the same
    // false-shortfall the whole screen exists to end.
    expect(within(table).queryByText("Rows per page:")).not.toBeInTheDocument();
    expect(within(table).queryByRole("button", { name: "Next" })).not.toBeInTheDocument();
  });
});

describe("WorkloadInventory — a completed Job is not 'Running'", () => {
  it("labels an ephemeral workload Completed while a Deployment says Running", () => {
    renderInventory(prodReachable());
    const table = screen.getByTestId("workloads-table");

    // Both jobs completed and were reaped; neither is running.
    const completed = table.querySelectorAll('[data-ephemeral="true"]');
    expect(completed).toHaveLength(2);
    completed.forEach((el) => expect(el).toHaveTextContent("Completed"));

    // A Deployment that really is running still says so.
    expect(within(table).getAllByText("Running").length).toBe(14);
  });
});

describe("WorkloadInventory — an omitted replica count is not zero", () => {
  it("renders a Job without a 0/0 ratio", () => {
    renderInventory(prodReachable());
    const table = screen.getByTestId("workloads-table");

    // Both jobs are present…
    expect(within(table).getByText("control-plane-migrate-aaa90288d6")).toBeInTheDocument();
    // …and neither invents a ratio. "0/0" reads as a workload that should have
    // pods and has none.
    expect(within(table).queryByText("0/0")).not.toBeInTheDocument();
    expect(table.querySelectorAll('[data-replicas="no-replicas"]').length).toBe(2);

    // A real Deployment still shows its real ratio.
    expect(within(table).getAllByText("2/2").length).toBeGreaterThan(0);
  });
});

describe("WorkloadInventory — the empty states say which emptiness they are", () => {
  it("distinguishes 'deploys nothing' from 'forge did not report'", () => {
    const { unmount } = renderInventory(deploysNothing(), "e2e");
    expect(screen.getByTestId("workloads-empty")).toHaveAttribute(
      "data-empty-posture",
      "nothing"
    );
    expect(screen.getByTestId("workloads-sentence")).toHaveTextContent(
      "deploys nothing to Kubernetes"
    );
    unmount();

    renderInventory(noWorkloadsKey());
    expect(screen.getByTestId("workloads-empty")).toHaveAttribute(
      "data-empty-posture",
      "not-reported"
    );
    // The sentence must not let "not reported" read as "deploys nothing".
    expect(screen.getByTestId("workloads-sentence")).toHaveTextContent(
      "not a statement that prod deploys nothing"
    );
  });
});

describe("WorkloadInventory — multi-cluster environments say where each row is", () => {
  it("adds a per-row cluster column only when the env spans several scopes", () => {
    const { unmount } = renderInventory(prodReachable());
    // One scope: naming it once above the table is unambiguous, so no column.
    expect(
      within(screen.getByTestId("workloads-table")).queryByText("Cluster")
    ).not.toBeInTheDocument();
    unmount();

    renderInventory(devTwoClusters(), "dev");
    const table = screen.getByTestId("workloads-table");
    expect(within(table).getByText("Cluster")).toBeInTheDocument();
    expect(within(table).getByText("k3d-control-plane/control-plane-dev")).toBeInTheDocument();
    expect(within(table).getByText("k3d-cp-daemon/control-plane-dev")).toBeInTheDocument();
  });

  it("says a workload is unrouted rather than attributing it to a context", () => {
    renderInventory(devTwoClusters(), "dev");
    const table = screen.getByTestId("workloads-table");
    const unrouted = table.querySelector('[data-scope="unrouted"]');
    expect(unrouted).toBeInTheDocument();
    expect(unrouted).toHaveTextContent("Unrouted");
  });
});

describe("WorkloadInventory — pods", () => {
  it("explains an empty pod list in the workload's own terms", async () => {
    const user = userEvent.setup();
    renderInventory(prodReachable());

    await user.click(screen.getByTestId("workloads-toggle-pods"));
    const pods = screen.getByTestId("workload-pods");

    // A completed Job has no pods, and that is normal.
    expect(
      within(pods).getAllByText(/pods are expected to come and go/).length
    ).toBeGreaterThan(0);
    // A real pod name is shown for a running Deployment.
    expect(within(pods).getAllByTestId("workload-pod").length).toBeGreaterThan(0);
  });

  it("says an unknown workload's pods were not read, not that it has none", async () => {
    const user = userEvent.setup();
    renderInventory(prodUnreachable());

    await user.click(screen.getByTestId("workloads-toggle-pods"));
    const pods = screen.getByTestId("workload-pods");
    expect(within(pods).getAllByText(/could not reach/).length).toBe(16);
    expect(within(pods).queryAllByTestId("workload-pod")).toHaveLength(0);
  });
});

describe("WorkloadInventory — non-report outcomes", () => {
  it("renders the unreachable state rather than an empty inventory", () => {
    const outcome = classifyForgeResponse<ForgeEnvStatusReport>(
      meta({ reachability: ForgeReachability.UNREACHABLE }),
      ""
    );
    render(
      <WorkloadInventory outcome={outcome} isLoading={false} env="prod" projectName="control-plane" />
    );
    expect(screen.queryByTestId("workload-inventory")).not.toBeInTheDocument();
    expect(screen.queryByTestId("workloads-empty")).not.toBeInTheDocument();
  });

  it("renders the not-a-forge-project state", () => {
    const outcome = classifyForgeResponse<ForgeEnvStatusReport>(
      meta({ isForgeProject: false }),
      ""
    );
    render(
      <WorkloadInventory outcome={outcome} isLoading={false} env="prod" projectName="reliant" />
    );
    expect(screen.queryByTestId("workload-inventory")).not.toBeInTheDocument();
  });
});
