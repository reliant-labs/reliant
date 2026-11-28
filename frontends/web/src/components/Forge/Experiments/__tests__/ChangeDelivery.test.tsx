// Copyright (c) 2026 Reliant Labs

/**
 * A change's Delivery card against the fake ChangesService
 * (fakeChangesService.ts): what GetChangesDeliveryStatus is asked, and the
 * Saved / Waiting for delivery / Delivered states and their words. Plus, on
 * the change's page, that a write reads delivery again for the new revision.
 */

import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { renderWithQuery } from "@/test/renderWithQuery";
import type { Change } from "@/services/controlPlane/changes/changesTypes";

import { ChangeDelivery } from "../ChangeDelivery";
import { ChangeDetail } from "../ChangeDetail";
import { installFakeChangesService, type FakeChangesService, type FakeHandler } from "./fakeChangesService";

vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, className }: { href: string; children: ReactNode; className?: string }) => (
    <a href={href} className={className}>
      {children}
    </a>
  ),
}));
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useSearch: () => ({}),
}));

let installed: FakeChangesService | null = null;

function fakeService(handlers: Record<string, FakeHandler>) {
  installed?.restore();
  installed = installFakeChangesService(handlers);
  return installed;
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date("2026-11-20T12:00:00Z"));
});

afterEach(() => {
  vi.useRealTimers();
  installed?.restore();
  installed = null;
});

const change = { id: "chg-1", environmentId: "env-prod", revision: 7 } as Change;
const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString();

const site = (revision: string, deliveredAt = minutesAgo(3)) => ({
  kind: "CHANGE_DELIVERY_TARGET_KIND_STATIC_SITE",
  name: "shop-web",
  location: "/_forge/changes.json",
  revision,
  deliveredAt,
  current: revision === "42" || revision === "43",
});
const workloads = (revision: string, deliveredAt = minutesAgo(5)) => ({
  kind: "CHANGE_DELIVERY_TARGET_KIND_CONFIG_MAP",
  name: "shop-prod/reliant-forge-changes",
  location: "/etc/forge/changes/changes.json",
  revision,
  deliveredAt,
  current: revision === "42" || revision === "43",
});

function renderCard() {
  return renderWithQuery(<ChangeDelivery change={change} envName="prod" />);
}

describe("a change's Delivery card", () => {
  it("is Delivered, with when, once every target has the environment's current snapshot", async () => {
    const service = fakeService({ GetChangesDeliveryStatus: () => ({ revision: "42", etag: '"e42"', targets: [site("42", minutesAgo(3)), workloads("42", minutesAgo(5))] }) });
    renderCard();

    await waitFor(() => expect(screen.getByTestId("change-delivery-state")).toHaveTextContent("Delivered"));
    const card = screen.getByTestId("change-delivery");
    expect(card).toHaveTextContent("Revision 7 is saved and written to all 2 delivery targets in prod, 3m ago.");
    expect(card).toHaveTextContent("A page applies it on its next load.");
    expect(card).toHaveTextContent("A running workload picks it up about a minute after it is written, plus the SDK's 10-second re-read.");
    const row = screen.getByTestId("delivery-target-shop-web");
    expect(row).toHaveTextContent("Hosted site");
    expect(row).toHaveTextContent("/_forge/changes.json");
    expect(row).toHaveTextContent("Written 3m ago");
    expect(service.of("GetChangesDeliveryStatus")[0]!.body).toEqual({ environmentId: "env-prod" });
  });

  it("waits for delivery while a target is behind the environment's snapshot", async () => {
    fakeService({ GetChangesDeliveryStatus: () => ({ revision: "42", targets: [site("42"), workloads("41")] }) });
    renderCard();

    await waitFor(() => expect(screen.getByTestId("change-delivery-state")).toHaveTextContent("Waiting for delivery"));
    expect(screen.getByTestId("change-delivery")).toHaveTextContent("1 of 2 delivery targets in prod have the environment's latest changes so far.");
    const behind = screen.getByTestId("delivery-target-shop-prod/reliant-forge-changes");
    expect(within(behind).getByText("Waiting")).toBeInTheDocument();
    expect(behind).toHaveTextContent("Last written 5m ago");
  });

  it("shows Waiting on the card and target even when a non-current target has a higher revision", async () => {
    fakeService({ GetChangesDeliveryStatus: () => ({ revision: "42", targets: [{ ...site("43"), current: false }] }) });
    renderCard();

    await waitFor(() => expect(screen.getByTestId("change-delivery-state")).toHaveTextContent("Waiting for delivery"));
    expect(within(screen.getByTestId("delivery-target-shop-web")).getByText("Waiting")).toBeInTheDocument();
  });

  it("says no hosted app receives it when the environment has none", async () => {
    fakeService({ GetChangesDeliveryStatus: () => ({ revision: "42", targets: [] }) });
    renderCard();

    expect(await screen.findByText("Revision 7 is saved. No hosted apps in this environment receive it.")).toBeInTheDocument();
    expect(screen.getByTestId("change-delivery-state")).toHaveTextContent("Saved");
    expect(screen.queryByRole("list", { name: "Delivery targets" })).toBeNull();
  });

  it("stays Saved on a control plane that can't report delivery", async () => {
    // No handler: Unimplemented, as a control plane without the rpc answers.
    fakeService({});
    renderCard();

    expect(await screen.findByText("This control plane can't report delivery yet.")).toBeInTheDocument();
    expect(screen.getByTestId("change-delivery-state")).toHaveTextContent("Saved");
    expect(screen.getByTestId("change-delivery")).toHaveTextContent("Revision 7 is saved.");
  });
});

describe("Delivery on the change's page", () => {
  it("reads delivery again for the revision a write saves", async () => {
    const wire = (revision: string, mode = "CHANGE_MODE_EVERYONE") => ({
      id: "chg-1",
      orgId: "org-1",
      environmentId: "env-prod",
      key: "new-checkout",
      mode,
      unit: "CHANGE_UNIT_USER",
      everyoneVariant: "on",
      revision,
      declaration: { type: "bool", defaultVariant: "off", variants: [{ name: "off", valueJson: "false" }, { name: "on", valueJson: "true" }] },
    });
    let snapshot = "42";
    const service = fakeService({
      GetChange: () => ({ change: wire("7") }),
      ListChangeEvents: () => ({ events: [] }),
      StopChange: () => {
        snapshot = "43";
        return { change: wire("8", "CHANGE_MODE_OFF") };
      },
      GetChangesDeliveryStatus: () => ({ revision: snapshot, targets: [site(snapshot)] }),
    });
    renderWithQuery(<ChangeDetail changeId="chg-1" envs={[{ id: "env-prod", name: "prod" }]} backHref="/forge/p/shop/experiments" />);

    await waitFor(() => expect(screen.getByTestId("change-delivery")).toHaveTextContent("Revision 7 is saved and written to its one delivery target in prod"));
    await userEvent.click(screen.getByRole("button", { name: "Stop" }));
    await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Stop" }));

    await waitFor(() => expect(screen.getByTestId("change-delivery")).toHaveTextContent("Revision 8 is saved and written to its one delivery target in prod"));
    expect(service.of("GetChangesDeliveryStatus").length).toBeGreaterThanOrEqual(2);
  });
});
