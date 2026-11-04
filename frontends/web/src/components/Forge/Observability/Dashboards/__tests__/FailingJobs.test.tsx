// Copyright (c) 2025 Reliant Labs

/**
 * The hub's failing-jobs card after a refetch fails. React Query keeps the last
 * data, so the card must decide what a failure on top of it means: a quota
 * refusal keeps the last result, marked paused; anything else (a denial after
 * access was revoked, an outage) shows no stale job at all.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";

import { quotaPausedError } from "@/services/controlPlane/observability/quota";
import type { WorkloadJob } from "@/services/controlPlane/observability/types";
import { FailingJobs } from "../DashboardsHub";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const job: WorkloadJob = {
  envId: "env-prod",
  envName: "prod",
  name: "shop-migrate-1",
  workload: "shop-migrate",
  result: "failed",
  reason: "BackoffLimitExceeded",
  releaseVersion: "",
  startedAtMs: null,
  finishedAtMs: null,
} as WorkloadJob;

function renderCard(error: unknown) {
  render(<FailingJobs listed={{ data: { jobs: [job], jobsTruncated: false }, isLoading: false, error }} env="prod" all={false} workloadHref={() => "/w"} />);
}

describe("FailingJobs after a read succeeded and its refetch failed", () => {
  it("hides the cached jobs when the refetch is denied", () => {
    renderCard(new ConnectError("denied", Code.PermissionDenied));
    expect(screen.getByTestId("hub-unavailable")).toHaveTextContent("You do not have access to job runs here.");
    expect(screen.queryByText("shop-migrate")).not.toBeInTheDocument();
  });

  it("hides the cached jobs on an outage", () => {
    renderCard(new ConnectError("down", Code.Unavailable));
    expect(screen.getByTestId("hub-unavailable")).toBeInTheDocument();
    expect(screen.queryByText("shop-migrate")).not.toBeInTheDocument();
  });

  it("keeps the last result on a quota refusal, marked paused", () => {
    renderCard(quotaPausedError({ limit: "queries", lane: "", resetsAtMs: Date.now() + 60_000, soft: false }));
    expect(screen.getByText("shop-migrate")).toBeInTheDocument();
    expect(screen.getByTestId("hub-jobs-paused")).toBeInTheDocument();
  });
});
