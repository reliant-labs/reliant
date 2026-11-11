// Copyright (c) 2025 Reliant Labs

/**
 * A release RECORDED by the team's own CI (`forge release record`), not built
 * by forge: it is labelled "recorded by CI" in the list and on its page, may be
 * version-only (no artifacts), and offers no promote or rollback — forge never
 * deploys it, and the control plane would refuse the write anyway.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { ReleasesApi } from "@/services/forge/ReleasesApiContext";

import { RECORDED_PROMOTE_REASON } from "../releaseParts";
import { RELEASES, createReleasesFixtureApi } from "../__fixtures__/releaseFixtures";
import { renderDetail, renderList } from "./releasesTestUtils";

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

// V[2] recorded by CI with no artifacts: a version-only record.
const RECORDED = RELEASES.map((release, index) => (index === 2 ? { ...release, recordOnly: true, artifacts: [] } : release));
const V = RECORDED.map((release) => release.version);

function recordedApi(): ReleasesApi {
  const api = createReleasesFixtureApi();
  return {
    ...api,
    listReleases: async () => RECORDED,
    getRelease: async (project, version) => {
      const got = await api.getRelease(project, version);
      return got && version === V[2] ? { ...got, release: RECORDED[2]! } : got;
    },
  };
}

describe("a release recorded by CI", () => {
  it("is labelled in the list, and its menu says why it cannot be promoted", async () => {
    const user = userEvent.setup();
    renderList(recordedApi());
    await waitFor(() => expect(screen.getByTestId("releases")).toHaveAttribute("data-state", "ready"));
    expect(within(screen.getByTestId(`release-row-${V[2]}`)).getByText("recorded by CI")).toBeInTheDocument();
    expect(within(screen.getByTestId(`release-row-${V[1]}`)).queryByText("recorded by CI")).not.toBeInTheDocument();

    await user.click(screen.getByTestId(`release-menu-${V[2]}`));
    expect(screen.getByTestId("release-menu-promote")).toBeDisabled();
    expect(screen.getByTestId("release-menu-promote")).toHaveAttribute("title", RECORDED_PROMOTE_REASON);
  });

  it("is labelled on its page, says it was recorded, and offers no promote or rollback", async () => {
    renderDetail(recordedApi(), V[2]!);
    await waitFor(() => expect(screen.getByTestId("release-detail")).toHaveAttribute("data-state", "ready"));
    expect(screen.getByText("recorded by CI")).toBeInTheDocument();
    expect(screen.getByText(/^recorded by \S+ from /)).toBeInTheDocument();
    expect(screen.queryByText(/^cut by /)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Promote…" })).toBeDisabled();
    expect(screen.getByTestId("release-promote-blocked")).toHaveTextContent(RECORDED_PROMOTE_REASON);
  });

  it("explains a version-only record's empty artifact list", async () => {
    renderDetail(recordedApi(), V[2]!, { search: { tab: "artifacts" } });
    await waitFor(() => expect(screen.getByTestId("release-detail")).toHaveAttribute("data-state", "ready"));
    expect(screen.getByTestId("release-artifacts")).toHaveTextContent("Version-only record");
  });
});
