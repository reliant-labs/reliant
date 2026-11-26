// Copyright (c) 2026 Reliant Labs

/**
 * The Security and Tests pages render the demo's sample data as a real
 * state: findings, SBOMs, a report, and charge.go's source under its hits,
 * never an empty or "not available" state.
 *
 * The pages run as the demo runs them: no API provider and no scope, so
 * their reads (the generated VulnerabilityService hooks, controlPlaneTestApi,
 * fetchCoverageSource) read the shop pack over the app's transport.
 */

import type { ReactNode } from "react";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import type { Transport } from "@connectrpc/connect";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { renderVulnerabilities, renderWith } from "@/components/Forge/Security/__tests__/securityTestUtils";
import { SbomsView } from "@/components/Forge/Security/SbomsPage";
import { VulnerabilityView } from "@/components/Forge/Security/VulnerabilityPage";
import { TestsCoverageView } from "@/components/Forge/Tests/TestsCoveragePage";
import { TestsOverviewView } from "@/components/Forge/Tests/TestsOverviewPage";
import { setTransport } from "@/lib/connect";
import { renderWithTransport } from "@/lib/test-utils";

import { createShopTransport } from "..";
import { SHOP_ADVISORIES } from "../model/security";
import { SHOP_CHARGE_PATH } from "../model/tests";
import { demoNow, resetDemoNowForTests } from "../shopStory";

vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));
vi.mock("@/components/Forge/Observability/charts/UPlotChart", () => ({ UPlotChart: ({ label }: { label: string }) => <figure data-testid="chart">{label}</figure> }));
vi.mock("@/components/Forge/Observability/AskReliant/AskReliant", () => ({
  useAskReliant: () => ({ ask: vi.fn(async () => true), busy: false, starting: null, dialog: null, mode: "handoff" }),
}));

let transport: Transport;
beforeAll(() => {
  resetDemoNowForTests(Date.UTC(2026, 10, 10, 15, 0, 0));
  transport = createShopTransport();
});
afterAll(() => resetDemoNowForTests());

describe("the demo's Security pages", () => {
  // null: securityTestUtils renders with no fixture scope, so the pages read the app's transport, the shop pack.
  let restore = () => undefined as void;
  beforeAll(() => {
    restore = setTransport(transport);
  });
  afterAll(() => restore());

  it("lists shop's findings across its environments", async () => {
    renderVulnerabilities(null);
    const list = await screen.findByTestId("vuln-list");
    expect(list).toHaveTextContent("golang.org/x/net");
    expect(list).toHaveTextContent("path-to-regexp");
    expect(screen.getByTestId("vuln-env-prod")).toHaveTextContent("v1.8.3");
    expect(screen.getByTestId("vuln-env-dev")).toHaveTextContent("v1.8.4");
  });

  it("shows every running release's SBOMs, the storefront's from its lockfile", async () => {
    renderWith(null, <SbomsView scope={{ project: "shop", environmentId: undefined }} releaseHref={(v) => `/releases/${v}`} />);
    expect(await screen.findByTestId("sboms-release-v1.8.3")).toHaveTextContent("4 of 4 with an SBOM, 4 scanned");
    expect(await screen.findByTestId("sboms-release-v1.8.4")).toHaveTextContent("lockfile");
  });

  it("opens an advisory with its history, exemptions and gate verdicts", async () => {
    const exempt = SHOP_ADVISORIES.find((a) => a.exempt)!.advisory.id;
    renderWith(
      null,
      <VulnerabilityView project="shop" advisoryId={exempt} backHref="/forge/p/shop/security/vulnerabilities" releaseHref={(v) => `/forge/p/shop/releases/${v}`} />
    );
    expect(await screen.findByTestId("vuln-exemptions")).toHaveTextContent("SSH server");
    expect(screen.getByTestId("vuln-history")).toBeInTheDocument();
    expect(screen.getByTestId("vuln-verdicts")).toBeInTheDocument();
  });
});

/** A Tests page with no provider: its TestApi and coverage source are the real ones, over the pack's transport. */
const renderTests = (children: ReactNode) => renderWithTransport(<>{children}</>, { transport });

describe("the demo's Tests pages", () => {
  afterEach(() => setTransport(null));

  it("leads the overview with main's coverage, pass rate, the flaky test and the last run", async () => {
    const hrefs = { flaky: "/f", runs: "/r", run: (id: string) => `/r/${id}`, coverage: "/c" };
    renderTests(<TestsOverviewView scope={{ project: "shop", environmentId: undefined }} hrefs={hrefs} now={demoNow()} />);
    expect(await screen.findByTestId("tests-headline-coverage")).toHaveTextContent("73.0%");
    expect(screen.getByTestId("tests-headline-pass-rate")).toHaveTextContent("100");
    expect(screen.getByTestId("tests-headline-flaky")).toHaveTextContent("1");
    expect(screen.getByTestId("tests-headline-last-run")).toBeInTheDocument();
  });

  it("draws v1.8.4's charge.go under its hits, from the bundle", async () => {
    renderTests(<TestsCoverageView scope={{ project: "shop", environmentId: undefined }} search={{ path: SHOP_CHARGE_PATH }} hrefFor={() => "/c"} onSelect={() => undefined} />);
    const view = await screen.findByTestId("tests-coverage-source");
    expect(view).toHaveTextContent("chargeDeadline");
    expect(screen.queryByTestId("tests-coverage-no-source")).toBeNull();
  });
});
