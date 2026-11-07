// Copyright (c) 2025 Reliant Labs

import { describe, expect, it, vi } from "vitest";

vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());
import { render, screen, waitFor, within } from "@testing-library/react";

import { createWorkloadsFixtureApi } from "../__fixtures__/workloadFixtures";
import { WorkloadReleases, RELEASES_SHOWN } from "../WorkloadReleases";
import type { WorkloadReleaseRow } from "../workloadModel";
import { WorkloadsProviders } from "./workloadsTestUtils";
import { RELEASES } from "../../Releases/__fixtures__/releaseFixtures";

const V = RELEASES.map((release) => release.version);
const ENV = { id: "env-prod", name: "prod" };

const row = (over: Partial<WorkloadReleaseRow>): WorkloadReleaseRow => ({
  id: "p",
  version: V[0]!,
  by: "sean",
  at: "2026-10-20T12:00:00Z",
  scoped: false,
  rollback: false,
  previousVersion: null,
  checks: null,
  bundleId: "",
  bundleDigest: "",
  resolvedArtifacts: {},
  ...over,
});

function renderPanel(rows: WorkloadReleaseRow[], state: { loading?: boolean; failed?: boolean } = {}) {
  return render(
    <WorkloadsProviders obs={createWorkloadsFixtureApi()}>
      <WorkloadReleases
        project="control-plane"
        env={ENV}
        workload="admin-server"
        rows={rows}
        loading={!!state.loading}
        failed={!!state.failed}
        releaseHref={(version) => `/releases/${version}`}
      />
    </WorkloadsProviders>
  );
}

describe("WorkloadReleases", () => {
  it("says so when no release moved the workload, and when the ledger could not be read", () => {
    const { unmount } = renderPanel([]);
    expect(screen.getByTestId("workload-history-releases")).toHaveTextContent("No promotion in the loaded history moved admin-server in prod.");
    unmount();
    renderPanel([], { failed: true });
    expect(screen.getByTestId("workload-history-releases")).toHaveTextContent("prod's promotions could not be read right now.");
  });

  it("shows who and when, the rollback and scoped marks, the image digest and what changed", async () => {
    renderPanel([
      row({ id: "a", version: V[0]!, previousVersion: V[1]!, rollback: true, resolvedArtifacts: { "admin-server": `sha256:${"a".repeat(64)}` } }),
      row({ id: "b", version: V[1]!, previousVersion: V[1]!, scoped: true }),
      row({ id: "c", version: V[2]!, previousVersion: null }),
    ]);
    const [first, second, third] = screen.getAllByTestId("workload-release-row");
    expect(first).toHaveAttribute("data-rollback");
    expect(within(first!).getByText("rollback")).toBeInTheDocument();
    expect(first).toHaveTextContent("sean");
    expect(within(first!).getByRole("link")).toHaveAttribute("href", `/releases/${V[0]}`);
    // The comparison is the server's, cut to this workload: the Deployment and its ConfigMap, not another workload's objects.
    await waitFor(() => expect(within(first!).getByRole("list", { name: "What changed in admin-server" })).toBeInTheDocument());
    const changed = within(first!).getByRole("list", { name: "What changed in admin-server" });
    expect(changed).toHaveTextContent("modified Deployment admin-server");
    expect(changed).toHaveTextContent("modified ConfigMap admin-server-config");
    expect(changed).not.toHaveTextContent("identity-gateway-egress");
    expect(first).toHaveTextContent("image");
    expect(within(second!).getByText("scoped")).toBeInTheDocument();
    expect(second).toHaveTextContent("again: the release did not change");
    expect(third).toHaveTextContent("No earlier promotion of admin-server is loaded to compare.");
  });

  it("lists the newest few and says how many more the ledger holds", () => {
    const rows = Array.from({ length: RELEASES_SHOWN + 3 }, (_, i) => row({ id: `r${i}`, version: V[0]!, previousVersion: V[0]! }));
    renderPanel(rows);
    expect(screen.getAllByTestId("workload-release-row")).toHaveLength(RELEASES_SHOWN);
    expect(screen.getByTestId("workload-releases-more")).toHaveTextContent(`The ${RELEASES_SHOWN} newest of ${RELEASES_SHOWN + 3}`);
  });
});
