// Copyright (c) 2025 Reliant Labs

/**
 * THE RELEASE ROW NEVER OVERLAPS ITSELF.
 *
 * The bug this pins: forge's versions are long
 * (`20261005.202711-1f9bf6721d7d`). The row put the version as plain text in
 * a fixed `w-28` column, so it wrapped onto two lines, the "current" pill was
 * drawn over the wrap, and the next row's date ran into it.
 *
 * jsdom does no layout, so these tests pin the STRUCTURE that makes overlap
 * impossible rather than measuring pixels:
 *
 *   - the version is on one line (`whitespace-nowrap`) with a truncating head
 *     and a pinned tail (the sha), and its full text in `title`;
 *   - the pill is a sibling AFTER the version in the same shrinkable head
 *     cell, never positioned over it;
 *   - the head and the date WRAP as whole pieces (flex-wrap) rather than
 *     squeezing into each other, and the date itself never wraps;
 *   - each image ref truncates on its own, with the full ref in `title`.
 */

import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";

import type { CloudPromotion } from "@/services/forge/cloudEnvs";
import type { LiveEnv } from "@/services/forge/live";

import { LiveReleases } from "../LiveReleases";

const LONG = "20261005.202711-1f9bf6721d7d";

const ENV: LiveEnv = {
  id: "cp-prod",
  name: "prod",
  project: "hounders",
  kind: "persistent",
  declaredShape: null,
  declaredBy: null,
  release: LONG,
  releaseProvenance: null,
  promotedByActor: "ci",
  promotedByUserId: "",
  phase: "succeeded",
  observed: { state: "not-reported" },
  drift: { state: "not-reported" },
  driftDetail: "",
  provenance: "",
  holds: [],
};

function promotion(id: string, version: string, createdAt: string): CloudPromotion {
  return {
    id,
    releaseVersion: version,
    kind: "promote",
    fromEnvironmentId: "",
    promotedByUserId: "",
    promotedByActor: "ci",
    note: "",
    createdAt,
    artifacts: [
      {
        name: "us-central1-docker.pkg.dev/reliant-labs-475814/hounders/hounders-web",
        digest: "sha256:1f9bf6721d7d0123456789abcdef0123456789abcdef0123456789abcdef0123",
      },
    ],
  };
}

const PROMOTIONS = [
  promotion("p2", LONG, "2026-10-05T20:27:11.000Z"),
  promotion("p1", "20261005.011748-0a1b2c3d4e5f", "2026-10-04T21:17:48.000Z"),
];

function renderRows() {
  return render(<LiveReleases env={ENV} promotions={PROMOTIONS} isLoading={false} error={null} />);
}

describe("a release row with a long version", () => {
  it("keeps the version on one line, truncating the head and pinning the sha", () => {
    renderRows();
    const head = screen.getByTestId("release-head-p2");
    const version = head.querySelector("[data-release-version]") as HTMLElement;

    expect(version).toHaveAttribute("data-release-version", LONG);
    expect(version).toHaveAttribute("title", LONG);
    expect(version.className).toMatch(/whitespace-nowrap/);
    expect(version.className).toMatch(/min-w-0/);
    // Clips its own overflow, so the pinned tail cannot paint under the pill.
    expect(version.className).toMatch(/overflow-hidden/);
    // The whole string is in the DOM — find-in-page and screen readers see it.
    expect(version.textContent).toBe(LONG);

    const [truncated, tail] = Array.from(version.children) as HTMLElement[];
    expect(truncated?.className).toMatch(/\btruncate\b/);
    expect(tail?.className).toMatch(/shrink-0/);
    expect(tail?.textContent).toBe(LONG.slice(-8));
  });

  it("offers the full version to copy", () => {
    renderRows();
    const head = screen.getByTestId("release-head-p2");
    expect(within(head).getByRole("button", { name: `Copy release version ${LONG}` })).toBeInTheDocument();
  });

  it("puts the current pill AFTER the version in the same head cell, not over it", () => {
    renderRows();
    const head = screen.getByTestId("release-head-p2");
    const pill = screen.getByTestId("release-current-p2");
    expect(head.className).toMatch(/min-w-0/);
    // The pill drops below the version before either is squeezed.
    expect(head.className).toMatch(/flex-wrap/);
    expect(pill.parentElement).toBe(head);
    expect(pill.className).toMatch(/shrink-0/);
    // Document order: version wrapper precedes the pill.
    const versionWrapper = head.firstElementChild as HTMLElement;
    expect(versionWrapper.compareDocumentPosition(pill) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(pill.className).not.toMatch(/absolute/);
    // Only the newest is current.
    expect(screen.queryByTestId("release-current-p1")).not.toBeInTheDocument();
  });

  it("keeps the date out of the version cell, on one line, wrapping whole when there is no room", () => {
    renderRows();
    const row = screen.getByTestId("promotion-p1");
    expect(row.className).toMatch(/flex-wrap/);
    expect(screen.getByTestId("release-head-p1").className).toMatch(/basis-56/);
    const date = screen.getByTestId("release-date-p1");
    expect(date.tagName).toBe("TIME");
    expect(date.className).toMatch(/whitespace-nowrap/);
    expect(date.className).toMatch(/shrink-0/);
    expect(date).toHaveAttribute("dateTime", "2026-10-04T21:17:48.000Z");
    // The date is NOT inside the version cell, where it used to collide.
    expect(screen.getByTestId("release-head-p1").contains(date)).toBe(false);
  });

  it("shows image refs in short digest form, each truncating, full ref in the title", () => {
    renderRows();
    const images = within(screen.getByTestId("promotion-p2")).getByRole("list", { name: "Images" });
    const ref = within(images).getAllByRole("listitem")[0] as HTMLElement;
    expect(ref.textContent).toMatch(/hounders-web@1f9bf6721d7d$/);
    expect(ref.className).toMatch(/\btruncate\b/);
    expect(ref).toHaveAttribute("title", expect.stringContaining("sha256:1f9bf6721d7d0123456789abcdef"));
  });

  it("lists releases only — no observation rows", () => {
    const { container } = renderRows();
    expect(container.querySelector('[data-entry="observation"]')).toBeNull();
    expect(container.querySelectorAll('[data-entry="promotion"]')).toHaveLength(2);
  });
});
