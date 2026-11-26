// Copyright (c) 2026 Reliant Labs

/**
 * The Overview's Vulnerabilities card against the fixture VulnerabilityService:
 * each state it can be in, and the rule it keeps — "No known vulnerabilities"
 * only when every environment it covers is fully scanned against a current
 * database and nothing was found.
 */

import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";

import { FIXTURE_PROJECT, fixtureVulnerabilityApi, type VulnScenario } from "../fixtures";
import { VulnerabilitiesCard } from "../VulnerabilitiesCard";
import { renderWith } from "./securityTestUtils";

vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

const HREF = "/forge/p/shop/security/vulnerabilities";

function renderCard(api: ReturnType<typeof fixtureVulnerabilityApi> | VulnScenario, envName: string | null = null) {
  const resolved = typeof api === "string" ? fixtureVulnerabilityApi(api) : api;
  renderWith(resolved, <VulnerabilitiesCard project={FIXTURE_PROJECT} envName={envName} href={HREF} />);
  return resolved;
}

/** The scenario's summary with every environment's findings zeroed: what the card then says hangs on coverage alone. */
function noFindings(scenario: VulnScenario) {
  const api = fixtureVulnerabilityApi(scenario);
  const real = api.getVulnerabilitySummary;
  const zero = { critical: 0, high: 0, medium: 0, low: 0, negligible: 0, unknown: 0 };
  api.getVulnerabilitySummary = async (req) => {
    const summary = await real(req);
    return { ...summary, environments: summary.environments.map((env) => ({ ...env, counts: zero, kev: 0, fixable: 0, blocking: 0 })) };
  };
  return api;
}

describe("Overview vulnerabilities card", () => {
  it("shows each deployed environment's C/H/M/L counts, KEV, fixable and last scan, with one link to the page", async () => {
    renderCard("findings");
    const prod = await screen.findByTestId("overview-vulns-env-prod");
    expect(within(prod).getByTestId("overview-vulns-counts")).toHaveTextContent("C 1H 1M 1L 1+1 other");
    expect(within(prod).getByTestId("overview-vulns-kev-prod")).toHaveTextContent("1 KEV");
    expect(prod).toHaveTextContent("2 fixable");
    expect(prod).toHaveTextContent("v1.4.0");
    expect(within(prod).getByTestId("overview-vulns-scanned")).toHaveTextContent(/^scanned .+ ago$/);
    const staging = screen.getByTestId("overview-vulns-env-staging");
    expect(within(staging).getByTestId("overview-vulns-counts")).toHaveTextContent("C 1H 0M 1L 0");
    // dev was never promoted: nothing to say about it.
    expect(screen.queryByTestId("overview-vulns-env-dev")).not.toBeInTheDocument();
    expect(screen.getAllByRole("link")).toHaveLength(1);
    expect(screen.getByTestId("overview-vulns-link")).toHaveAttribute("href", HREF);
    expect(screen.queryByText("No known vulnerabilities")).not.toBeInTheDocument();
  });

  it("narrows to the top bar's environment", async () => {
    renderCard("findings", "staging");
    expect(await screen.findByTestId("overview-vulns-env-staging")).toBeInTheDocument();
    expect(screen.queryByTestId("overview-vulns-env-prod")).not.toBeInTheDocument();
  });

  it("says no known vulnerabilities only when everything is scanned against a current database and nothing was found", async () => {
    renderCard("clean");
    expect(await screen.findByTestId("overview-vulns-none-known")).toHaveTextContent("No known vulnerabilities");
    expect(screen.getByTestId("overview-vulns-none-known")).toHaveTextContent("against a database built");
  });

  it.each(["nothing-deployed", "no-sbom", "pending"] as const)("is not scanned yet before any scan (%s), never clean", async (scenario) => {
    renderCard(scenario);
    expect(await screen.findByTestId("overview-vulns-not-scanned")).toHaveTextContent("Not scanned yet");
    expect(screen.queryByText("No known vulnerabilities")).not.toBeInTheDocument();
  });

  it("over a partly scanned scope with nothing found, is clean only where coverage is full", async () => {
    renderCard(noFindings("partial"));
    const staging = await screen.findByTestId("overview-vulns-env-staging");
    expect(staging).toHaveTextContent("Nothing found in what was scanned · not scanned");
    expect(staging).not.toHaveTextContent("No known vulnerabilities");
    expect(screen.getByTestId("overview-vulns-env-prod")).toHaveTextContent("No known vulnerabilities");
    expect(screen.queryByTestId("overview-vulns-none-known")).not.toBeInTheDocument();
  });

  it("over a stale database, never calls an environment with nothing found clean", async () => {
    renderCard(noFindings("stale"));
    const prod = await screen.findByTestId("overview-vulns-env-prod");
    expect(prod).toHaveTextContent("Nothing found in what was scanned · stale database");
    // staging's own scan succeeded, but against the same stale database.
    expect(screen.getByTestId("overview-vulns-env-staging")).not.toHaveTextContent("No known vulnerabilities");
    expect(screen.queryByTestId("overview-vulns-none-known")).not.toBeInTheDocument();
  });

  it("says what falls short beside the counts it has", async () => {
    renderCard("failed");
    const prod = await screen.findByTestId("overview-vulns-env-prod");
    expect(prod).toHaveTextContent("scan failed");
    expect(within(prod).getByTestId("overview-vulns-counts")).toBeInTheDocument();
  });

  it("says the control plane doesn't report vulnerabilities yet when the service answers Unimplemented", async () => {
    renderCard("not-configured");
    const state = await screen.findByTestId("overview-vulns-error-not-configured");
    expect(state).toHaveTextContent("This control plane doesn't report vulnerabilities yet");
    expect(state).toHaveTextContent("Nothing here means this project is clean");
  });

  it("says which grant is missing when the caller lacks vuln:read", async () => {
    renderCard("denied");
    const state = await screen.findByTestId("overview-vulns-error-denied");
    expect(state).toHaveTextContent("You can't read this project's vulnerabilities");
    expect(state).toHaveTextContent("vuln:read");
  });

  it("offers a retry when the control plane did not answer", async () => {
    const api = fixtureVulnerabilityApi("findings");
    const real = api.getVulnerabilitySummary;
    let fail = true;
    // retry-after 0: the hook's own retries of a transport blip (vulnRetry) run at once.
    const blip = () => new ConnectError("upstream reset", Code.Unavailable, new Headers({ "retry-after": "0" }));
    api.getVulnerabilitySummary = (req) => (fail ? Promise.reject(blip()) : real(req));
    renderCard(api);
    expect(await screen.findByTestId("overview-vulns-error-unavailable")).toBeInTheDocument();
    fail = false;
    await userEvent.click(screen.getByTestId("overview-vulns-retry"));
    expect(await screen.findByTestId("overview-vulns-env-prod")).toBeInTheDocument();
  });

  it("shows loading until the summary answers", async () => {
    const api = fixtureVulnerabilityApi("findings");
    api.getVulnerabilitySummary = () => new Promise(() => undefined);
    renderCard(api);
    expect(await screen.findByTestId("overview-vulns-loading")).toBeInTheDocument();
    expect(screen.queryByText("No known vulnerabilities")).not.toBeInTheDocument();
  });
});
