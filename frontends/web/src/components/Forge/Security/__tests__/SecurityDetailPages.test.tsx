// Copyright (c) 2025 Reliant Labs

/**
 * One advisory's page and the SBOMs page, against the fixture
 * VulnerabilityService.
 */

import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";

import { FIXTURE_ENVS, FIXTURE_PROJECT, FIXTURE_SCAN_ERROR, fixtureVulnerabilityApi } from "../fixtures";
import { SbomsView } from "../SbomsPage";
import { VulnerabilityView } from "../VulnerabilityPage";
import { renderWith } from "./securityTestUtils";

vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

function renderAdvisory(api: ReturnType<typeof fixtureVulnerabilityApi>, advisoryId: string) {
  return renderWith(
    api,
    <VulnerabilityView project={FIXTURE_PROJECT} advisoryId={advisoryId} backHref="/forge/p/shop/security/vulnerabilities" releaseHref={(v) => `/forge/p/shop/releases/${v}`} />
  );
}

describe("one advisory", () => {
  it("shows what it is, where it is found, its history and each environment's gate verdict", async () => {
    renderAdvisory(fixtureVulnerabilityApi("findings"), "CVE-2026-4101");
    const advisory = await screen.findByTestId("vuln-advisory");
    expect(advisory).toHaveTextContent("Critical");
    expect(advisory).toHaveTextContent("CVSS 9.8");
    expect(advisory).toHaveTextContent("Known exploited (KEV)");
    expect(advisory).toHaveTextContent("GHSA-7f3x-q2mv-9hc4");
    expect(advisory).toHaveTextContent("HTTP/2 rapid reset");
    expect(within(screen.getByTestId("vuln-references")).getAllByRole("link")[0]).toHaveAttribute("href", "https://nvd.nist.gov/vuln/detail/CVE-2026-4101");

    const verdicts = screen.getByTestId("vuln-verdicts");
    // Environment names come from the summary once it answers.
    await waitFor(() => expect(verdicts).toHaveTextContent("prod"));
    expect(verdicts).toHaveTextContent("block");
    expect(verdicts).toHaveTextContent("enforce");
    expect(verdicts).toHaveTextContent("warn");

    const occurrences = screen.getByTestId("vuln-occurrences");
    expect(occurrences).toHaveTextContent("v1.4.0");
    expect(occurrences).toHaveTextContent("worker");
    expect(occurrences).toHaveTextContent("fixed in v0.23.0");
    expect(within(occurrences).getAllByRole("link")[0]).toHaveAttribute("href", "/forge/p/shop/releases/v1.4.0");

    expect(screen.getByTestId("vuln-history")).toHaveTextContent("v1.3.0");
    expect(screen.queryByTestId("vuln-exemptions")).not.toBeInTheDocument();
  });

  it("opens by an alias, and says which id the scanner matched on", async () => {
    renderAdvisory(fixtureVulnerabilityApi("findings"), "GHSA-aaaa-bbbb-cccc");
    const advisory = await screen.findByTestId("vuln-advisory");
    expect(advisory).toHaveTextContent("GHSA-aaaa-bbbb-cccc is an alias of CVE-2026-3333");
    expect(screen.getByTestId("vuln-exemptions")).toHaveTextContent("The static handler is not mounted");
  });

  it("says an advisory is not in the project, and a refusal names the grant", async () => {
    renderAdvisory(fixtureVulnerabilityApi("findings"), "CVE-1999-0001");
    expect(await screen.findByTestId("vuln-detail-error-not-found")).toHaveTextContent("CVE-1999-0001 isn't in this project");
  });

  it("names the grant on a refusal", async () => {
    renderAdvisory(fixtureVulnerabilityApi("denied"), "CVE-2026-4101");
    expect(await screen.findByTestId("vuln-detail-error-denied")).toHaveTextContent("vuln:read");
  });
});

describe("SBOMs", () => {
  const renderSboms = (api: ReturnType<typeof fixtureVulnerabilityApi>, props: { environmentId?: string; release?: string } = {}) =>
    renderWith(api, <SbomsView scope={{ project: FIXTURE_PROJECT, environmentId: props.environmentId }} release={props.release} releaseHref={(v) => `/releases/${v}`} />);

  it("lists each running release's artifacts, labelling a lockfile SBOM", async () => {
    renderSboms(fixtureVulnerabilityApi("findings"));
    const prod = await screen.findByTestId("sboms-release-v1.4.0");
    expect(prod).toHaveTextContent("prod");
    expect(await screen.findByTestId("sboms-release-v1.5.0")).toHaveTextContent("staging");
    await waitFor(() => expect(within(prod).getByTestId("sbom-web")).toHaveTextContent("from lockfile"));
    expect(within(prod).getByTestId("sbom-api")).toHaveTextContent("from image");
    expect(within(prod).getByTestId("sbom-api")).toHaveTextContent("cyclonedx-json@1.6");
    expect(within(prod).getByTestId("sboms-coverage")).toHaveTextContent("3 of 3 with an SBOM, 3 scanned");
  });

  it("lists an artifact with no SBOM as not scanned, and a failed scan with its error", async () => {
    renderSboms(fixtureVulnerabilityApi("partial"), { environmentId: FIXTURE_ENVS.staging.id });
    const api = await screen.findByTestId("sbom-api");
    expect(api).toHaveAttribute("data-state", "no-sbom");
    expect(api).toHaveTextContent("Not scanned");
    expect(api).toHaveTextContent("None recorded");

    renderSboms(fixtureVulnerabilityApi("failed"), { release: FIXTURE_ENVS.prod.release });
    await waitFor(() => expect(screen.getAllByTestId("sbom-api").some((row) => row.textContent?.includes(FIXTURE_SCAN_ERROR))).toBe(true));
  });

  it("is 'Not scanned yet' with nothing deployed", async () => {
    renderSboms(fixtureVulnerabilityApi("nothing-deployed"));
    expect(await screen.findByTestId("vuln-not-scanned")).toBeInTheDocument();
  });
});
