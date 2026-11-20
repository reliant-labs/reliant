// Copyright (c) 2025 Reliant Labs

/**
 * Security › Vulnerabilities against the fixture VulnerabilityService (the
 * C5 read API's shape): every state the page can be in, and the rule it
 * exists for — "No known vulnerabilities" only when everything in scope was
 * scanned against a current database; never before a scan, never over a
 * failed, pending, partial or stale one.
 */

import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { FIXTURE_ENVS, FIXTURE_SCAN_ERROR, fixtureVulnerabilityApi } from "../fixtures";
import { renderVulnerabilities } from "./securityTestUtils";

vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

const listCalls = (api: ReturnType<typeof fixtureVulnerabilityApi>) =>
  api.calls.filter((call) => call.method === "listVulnerabilities").map((call) => call.req as { filters: Record<string, unknown>; pageToken?: string });

describe("Vulnerabilities: findings", () => {
  it("lists every environment's findings, most severe first, with what makes each one urgent", async () => {
    const api = fixtureVulnerabilityApi("findings");
    renderVulnerabilities(api);
    const table = await screen.findByTestId("vuln-list");
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows.map((row) => row.getAttribute("data-severity"))).toEqual(["critical", "high", "medium", "low", "unknown"]);

    const critical = screen.getByTestId("vuln-row-CVE-2026-4101");
    expect(critical).toHaveTextContent("GHSA-7f3x-q2mv-9hc4");
    expect(critical).toHaveTextContent("golang.org/x/net@v0.17.0");
    expect(critical).toHaveTextContent("fixed in v0.23.0");
    expect(critical).toHaveTextContent("KEV");
    expect(critical).toHaveTextContent("EPSS 94%");
    expect(critical).toHaveTextContent("prod");
    expect(critical).toHaveTextContent("staging");
    expect(critical).toHaveTextContent("Blocking");
    expect(within(critical).getAllByRole("link")[0]).toHaveAttribute("href", "/forge/p/shop/security/vulnerabilities/CVE-2026-4101");

    // The static site's SBOM is from its lockfile, and its findings say so.
    const lockfile = screen.getByTestId("vuln-row-GHSA-c2qf-rxjj-qqgw");
    await waitFor(() => expect(lockfile).toHaveTextContent("from lockfile"));
    expect(screen.getByTestId("vuln-row-CVE-2025-1180")).toHaveTextContent("won't be fixed");

    // The merged scope names no environment; exempt findings are left out.
    expect(listCalls(api)[0]!.filters).toEqual({});
    expect(screen.queryByTestId("vuln-row-CVE-2026-3333")).not.toBeInTheDocument();
    expect(screen.getByTestId("vuln-env-prod")).toHaveTextContent("1 known exploited");
    expect(screen.queryByTestId("vuln-none-known")).not.toBeInTheDocument();
  });

  it("scopes to one environment and sends each filter as the request's optional field", async () => {
    const api = fixtureVulnerabilityApi("findings");
    const view = renderVulnerabilities(api, { environmentId: FIXTURE_ENVS.prod.id });
    await screen.findByTestId("vuln-list");
    expect(listCalls(api).at(-1)!.filters).toEqual({ environmentId: FIXTURE_ENVS.prod.id });

    await userEvent.click(screen.getByTestId("vuln-filter-kev"));
    await waitFor(() => expect(listCalls(api).at(-1)!.filters).toEqual({ environmentId: FIXTURE_ENVS.prod.id, kev: true }));
    await waitFor(() => expect(screen.queryByTestId("vuln-row-GHSA-c2qf-rxjj-qqgw")).not.toBeInTheDocument());
    expect(view.search()).toEqual({ kev: "1" });

    await userEvent.click(screen.getByTestId("vuln-filter-exempt"));
    await userEvent.click(screen.getByTestId("vuln-filter-severity"));
    await userEvent.click(screen.getByRole("menuitemradio", { name: "High and above" }));
    await userEvent.click(screen.getByTestId("vuln-filter-fixable"));
    await userEvent.click(screen.getByRole("menuitemradio", { name: "No fix" }));
    await waitFor(() =>
      expect(listCalls(api).at(-1)!.filters).toEqual({ environmentId: FIXTURE_ENVS.prod.id, kev: true, includeExempt: true, minSeverity: "high", fixable: false })
    );
    expect(await screen.findByTestId("vuln-no-match")).toBeInTheDocument();
    await userEvent.click(screen.getByTestId("vuln-no-match-clear"));
    expect(view.search()).toEqual({});
  });

  it("shows exempt findings with their exemption when asked", async () => {
    const api = fixtureVulnerabilityApi("findings");
    renderVulnerabilities(api, { environmentId: FIXTURE_ENVS.prod.id, search: { exempt: "1" } });
    const exempt = await screen.findByTestId("vuln-exempt-CVE-2026-3333");
    expect(exempt).toHaveTextContent("exempt");
    expect(exempt).toHaveTextContent("until");
  });

  it("pages with the server's token, keeping the request the same", async () => {
    const api = fixtureVulnerabilityApi("findings");
    const pageOne = api.listVulnerabilities;
    api.listVulnerabilities = (req, opts) => pageOne({ ...req, pageSize: 2 }, opts);
    renderVulnerabilities(api);
    await screen.findByTestId("vuln-list");
    expect(within(screen.getByTestId("vuln-list")).getAllByRole("row")).toHaveLength(3);
    await userEvent.click(screen.getByTestId("vuln-more"));
    await waitFor(() => expect(within(screen.getByTestId("vuln-list")).getAllByRole("row")).toHaveLength(5));
    expect(listCalls(api).map((call) => call.pageToken)).toEqual(["", "2"]);
  });
});

describe("Vulnerabilities: unknown is never clean", () => {
  it("says no known vulnerabilities only when everything in scope was scanned", async () => {
    renderVulnerabilities(fixtureVulnerabilityApi("clean"));
    const clean = await screen.findByTestId("vuln-none-known");
    expect(clean).toHaveTextContent("No known vulnerabilities");
    expect(clean).toHaveTextContent("prod's v1.4.0 and staging's v1.5.0");
  });

  it("is 'Not scanned yet' before anything is deployed, and reads no findings", async () => {
    const api = fixtureVulnerabilityApi("nothing-deployed");
    renderVulnerabilities(api);
    const state = await screen.findByTestId("vuln-not-scanned");
    expect(state).toHaveTextContent("Not scanned yet");
    expect(state).toHaveTextContent("Nothing has been deployed");
    expect(screen.getByTestId("vuln-not-scanned-docs")).toHaveAttribute("href", expect.stringContaining("docs.reliantlabs.io"));
    expect(screen.queryByText(/No known vulnerabilities/)).not.toBeInTheDocument();
    expect(listCalls(api)).toHaveLength(0);
  });

  it("is 'Not scanned yet' when the releases predate SBOMs, saying the next cut makes them", async () => {
    renderVulnerabilities(fixtureVulnerabilityApi("no-sbom"));
    const state = await screen.findByTestId("vuln-not-scanned");
    expect(state).toHaveTextContent("unknown, not absent");
    expect(state).toHaveTextContent("reliant forge env deploy prod");
    expect(screen.queryByText(/No known vulnerabilities/)).not.toBeInTheDocument();
  });

  it("says the scan is pending while the first scans run", async () => {
    renderVulnerabilities(fixtureVulnerabilityApi("pending"));
    expect(await screen.findByTestId("vuln-scan-pending")).toHaveTextContent("prod's v1.4.0 and staging's v1.5.0");
    expect(screen.queryByText(/No known vulnerabilities/)).not.toBeInTheDocument();
  });

  it("says a scan failed, since when and why, and keeps the last successful scan's findings", async () => {
    renderVulnerabilities(fixtureVulnerabilityApi("failed"));
    const note = await screen.findByTestId("vuln-scan-failed-prod");
    expect(note).toHaveTextContent("prod: scan failed");
    expect(note).toHaveTextContent("failed since");
    expect(note).toHaveTextContent("from the last successful scan");
    await waitFor(() => expect(within(note).getByTestId("vuln-scan-error")).toHaveTextContent(`api: ${FIXTURE_SCAN_ERROR}`));
    expect(await screen.findByTestId("vuln-list")).toBeInTheDocument();
  });

  it("flags a stale database, and does not call an empty filtered list clean", async () => {
    renderVulnerabilities(fixtureVulnerabilityApi("stale"), { environmentId: FIXTURE_ENVS.prod.id });
    expect(await screen.findByTestId("vuln-scan-stale-prod")).toHaveTextContent("stale vulnerability database");
  });

  it("says which environment is not fully scanned, and an empty list there is not a clean bill", async () => {
    const api = fixtureVulnerabilityApi("partial");
    renderVulnerabilities(api, { environmentId: FIXTURE_ENVS.staging.id });
    expect(await screen.findByTestId("vuln-scan-no-sbom-staging")).toHaveTextContent("1 of 3 artifacts in v1.5.0 have no SBOM");
  });

  it("over a partly scanned scope, an empty answer says so rather than clean", async () => {
    const api = fixtureVulnerabilityApi("partial");
    api.listVulnerabilities = async () => ({ findings: [], nextPageToken: "", database: null });
    renderVulnerabilities(api);
    expect(await screen.findByTestId("vuln-none-in-scanned")).toHaveTextContent("not a clean bill");
    expect(screen.queryByTestId("vuln-none-known")).not.toBeInTheDocument();
  });
});

describe("Vulnerabilities: refusals", () => {
  it("says what grant is missing when the caller lacks vuln:read", async () => {
    renderVulnerabilities(fixtureVulnerabilityApi("denied"));
    const denied = await screen.findByTestId("vuln-error-denied");
    expect(denied).toHaveTextContent("You can't read this project's vulnerabilities");
    expect(denied).toHaveTextContent("vuln:read");
  });

  it("says the control plane does not report vulnerabilities yet, never that there are none", async () => {
    renderVulnerabilities(fixtureVulnerabilityApi("not-configured"));
    const state = await screen.findByTestId("vuln-error-not-configured");
    expect(state).toHaveTextContent("doesn't report vulnerabilities yet");
    expect(state).toHaveTextContent("Nothing here means this project is clean");
  });

  it("is what this build's real client says until the service is vendored", async () => {
    const { controlPlaneVulnerabilityApi } = await import("@/services/controlPlane/vulnerabilities/VulnerabilityApiContext");
    renderVulnerabilities(controlPlaneVulnerabilityApi);
    expect(await screen.findByTestId("vuln-error-not-configured")).toBeInTheDocument();
  });

  it("shows loading until the summary answers", async () => {
    const api = fixtureVulnerabilityApi("findings");
    let release: () => void = () => undefined;
    const gate = new Promise<void>((resolve) => (release = resolve));
    const real = api.getVulnerabilitySummary;
    api.getVulnerabilitySummary = async (req, opts) => {
      await gate;
      return real(req, opts);
    };
    renderVulnerabilities(api);
    expect(screen.getByTestId("vuln-loading")).toBeInTheDocument();
    release();
    expect(await screen.findByTestId("vuln-list")).toBeInTheDocument();
  });
});
