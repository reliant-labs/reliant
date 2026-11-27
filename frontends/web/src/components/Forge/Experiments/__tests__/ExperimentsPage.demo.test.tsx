// Copyright (c) 2025 Reliant Labs

/**
 * The Experiments pages on the public demo: the demo bundle's connect module
 * (lib/connect.demo.ts, swapped in for lib/connect.ts as
 * vite-plugin-demo-bundle does) over the demo's transport, which serves the
 * shop pack's ChangesService (fixtures/shop/handlers/changes.ts). The list
 * holds the story's changes, a change opens with its delivery, results and
 * history, and every write control is off, as the demo's read-only shell
 * (ObsReadOnlyProvider) says.
 */

import type { ReactNode } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/connect", async () => await import("@/lib/connect.demo"));
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
vi.mock("@/services/controlPlane/orgs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/orgs")>()),
  currentOrgId: async () => "org-acme",
}));
vi.mock("../../Console/consoleContext", () => ({
  useForgeConsole: () => ({
    forgeProject: "shop",
    env: "all",
    roster: {
      isLoading: false,
      envs: [
        { name: "dev", lifecycle: "deployed", live: { id: "env-dev", name: "dev" }, forge: null, source: "backend" },
        { name: "prod", lifecycle: "deployed", live: { id: "env-prod", name: "prod" }, forge: null, source: "backend" },
      ],
    },
    setEnv: vi.fn(),
    href: (target: { kind: string; changeId?: string }) => (target.changeId ? `/forge/p/shop/experiments/${target.changeId}` : "/forge/p/shop/experiments"),
  }),
}));
vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children }: { href: string; children: ReactNode }) => <a href={href}>{children}</a>,
}));
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
  useSearch: () => ({}),
  useParams: () => ({}),
}));

import { screen, within } from "@testing-library/react";

import { installShellTransport, setTransport } from "@/lib/connect.demo";
import { resetDemoNowForTests } from "@/fixtures/shop/shopStory";
import { renderWithQuery } from "@/test/renderWithQuery";

import { createDemoTransport } from "../../Demo/demoTransport";
import { ObsReadOnlyProvider } from "../../Observability/obsReadOnly";
import { ChangeDetail } from "../ChangeDetail";
import { ExperimentsPage } from "../ExperimentsPage";

const ENVS = [
  { id: "env-dev", name: "dev" },
  { id: "env-prod", name: "prod" },
];

const demo = (ui: ReactNode) => renderWithQuery(<ObsReadOnlyProvider value={{ readOnly: true, reason: "Read-only demo" }}>{ui}</ObsReadOnlyProvider>);

beforeAll(() => resetDemoNowForTests(Date.UTC(2026, 10, 10, 15, 0, 0)));
afterAll(() => resetDemoNowForTests());

beforeEach(() => {
  setTransport(null);
  installShellTransport(createDemoTransport({ source: "fixtures", url: "" }));
});

afterEach(() => {
  installShellTransport(null as never);
});

describe("Experiments on the demo", () => {
  it("lists the shop's changes in every environment, and offers no new one", async () => {
    demo(<ExperimentsPage />);
    const table = await screen.findByRole("table", { name: "Changes" });
    const rows = within(table).getAllByRole("row").slice(1);
    const cells = (row: HTMLElement) => within(row).getAllByRole("cell");
    expect(rows.map((row) => [within(row).getByRole("link").textContent, cells(row)[1].textContent, cells(row)[2].textContent])).toEqual([
      ["banner.free_shipping", "prod", "Off"],
      ["checkout.payment_retry", "dev", "Preview"],
      ["checkout.payment_retry", "prod", "Everyone"],
      ["confirmation.order_status", "prod", "Rolling out"],
      ["express-checkout", "prod", "Experiment"],
    ]);
    expect(within(screen.getByTestId("change-row-confirmation.order_status")).getByText("25% of users get on")).toBeInTheDocument();
    expect(screen.queryByText("Experiments aren't available here yet")).toBeNull();
    expect(screen.getByRole("button", { name: /New change/ })).toBeDisabled();
  });

  it("opens the A/B test with its delivery, a readout the new version wins, and its history", async () => {
    demo(<ChangeDetail changeId="chg-express-checkout-prod" envs={ENVS} backHref="/forge/p/shop/experiments" />);
    expect(await screen.findByTestId("results-verdict")).toHaveAttribute("data-verdict", "treatment_better");
    expect(screen.getByTestId("results-verdict")).toHaveTextContent("The new version is better(on)");
    expect(screen.getByTestId("results-arm-control")).toHaveTextContent("33.3%");
    expect(screen.getByTestId("results-arm-on")).toHaveTextContent("37.1%");
    expect(await screen.findByTestId("change-delivery-state")).toHaveTextContent("Delivered");
    expect(await screen.findByTestId("change-history")).toHaveTextContent("jon@example.com launched it, approved in the console");
    // Every write is off: the kill switch, the ramp and the relaunch.
    expect(screen.getByRole("button", { name: /Stop/ })).toBeDisabled();
    for (const button of within(screen.getByTestId("who-actions")).getAllByRole("button")) expect(button).toBeDisabled();
    expect(screen.getByTestId("who-read-only")).toHaveTextContent("Read-only demo");
  });

  it("opens the incident's mitigation: everyone gets off, and there is no experiment to read", async () => {
    demo(<ChangeDetail changeId="chg-payment-retry-prod" envs={ENVS} backHref="/forge/p/shop/experiments" />);
    expect(await screen.findByText("Everyone gets off")).toBeInTheDocument();
    expect(await screen.findByTestId("results-no-experiment")).toBeInTheDocument();
    expect(await screen.findByTestId("change-history")).toHaveTextContent("maya@example.com rolled it out, approved in the console");
  });

  it("reads the finished test's final readout: no clear difference", async () => {
    demo(<ChangeDetail changeId="chg-free-shipping-prod" envs={ENVS} backHref="/forge/p/shop/experiments" />);
    expect(await screen.findByTestId("results-verdict")).toHaveAttribute("data-verdict", "inconclusive");
    expect(screen.getByTestId("results-window")).toHaveTextContent("Final.");
  });

  it("says a change the story has none of no longer exists", async () => {
    demo(<ChangeDetail changeId="chg-nope" envs={ENVS} backHref="/forge/p/shop/experiments" />);
    expect(await screen.findByText("Couldn't open this change")).toBeInTheDocument();
  });
});
