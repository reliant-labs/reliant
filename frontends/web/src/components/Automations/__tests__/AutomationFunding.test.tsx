// Copyright (c) 2025 Reliant Labs

/**
 * Which AI accounts an automation runs on (org-provider-accounts §11.5): the
 * row names the org's accounts or the owner's, the switch writes the stamp
 * back through UpdateTrigger with the rest of the definition unchanged, a
 * refused loosening shows the server's reason, and an automation of no org
 * shows nothing. The release banner names the person's automations that
 * still spend their own accounts, links to the oldest, and dismisses its
 * Inbox item.
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const toastError = vi.hoisted(() => vi.fn());
vi.mock("sonner", async (original) => {
  const actual = await original<typeof import("sonner")>();
  return { ...actual, toast: Object.assign(vi.fn(), actual.toast, { error: toastError }) };
});

import { InboxItemKind } from "@/gen/reliant/v1/inbox_pb";
import { ScheduleSourceSchema, UnattendedFunding } from "@/gen/reliant/v1/trigger_pb";
import type { Trigger } from "@/hooks/trigger-model";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { orgKeys } from "@/services/controlPlane/orgs";
import { AutomationFunding } from "../AutomationFunding";
import { AutomationsFundingBanner, automationsFundingMessage } from "../AutomationsFundingBanner";

let queryClient: QueryClient;

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

function trigger(overrides: Partial<Trigger> = {}): Trigger {
  return {
    id: "trg-1",
    name: "Nightly",
    projectId: "proj-1",
    enabled: true,
    workflow: "triage",
    presets: {},
    params: { model: { id: "mock" } },
    message: "go",
    daemonId: "daemon-1",
    noMachine: false,
    notifyOnComplete: false,
    actingIdentity: "owner",
    health: { status: "healthy", consecutiveFailures: 0, consecutiveSkips: 0, lastFailureDetail: "" },
    createdAt: "",
    updatedAt: "",
    source: { kind: "schedule", schedule: { cron: ["0 9 * * *"], timezone: "UTC", overlap: "skip" } },
    webhookManaged: false,
    orgId: "acme",
    unattendedFunding: "orgOnly",
    ...overrides,
  };
}

const wire = {
  id: "trg-1",
  enabled: true,
  name: "Nightly",
  projectId: "proj-1",
  daemonId: "daemon-1",
  source: { case: "schedule" as const, value: create(ScheduleSourceSchema, { cron: ["0 9 * * *"] }) },
};

beforeEach(() => {
  queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity }, mutations: { retry: false } } });
  queryClient.setQueryData(orgKeys.mine, {
    orgs: [{ id: "acme", name: "Acme", slug: "acme", role: "member", isOwner: false }],
    defaultOrgId: "acme",
  });
  toastError.mockReset();
});

afterEach(() => {
  cleanup();
  setTransport(null);
});

describe("AutomationFunding", () => {
  it("names the org's accounts, and switching it off writes ANY with the rest of the definition", async () => {
    const updates: Array<{ id: string; trigger: { unattendedFunding: number; name: string; message: string } }> = [];
    setTransport(
      mockTransport({
        "TriggerService.UpdateTrigger": (request: unknown) => {
          updates.push(request as (typeof updates)[number]);
          return { trigger: wire };
        },
      }),
    );
    render(<AutomationFunding trigger={trigger()} label="Nightly" />, { wrapper });

    expect(screen.getByTestId("automation-funding").textContent).toContain("Runs on Acme's accounts");
    await userEvent.click(screen.getByRole("switch", { name: "Nightly: Acme's accounts only" }));

    await waitFor(() => expect(updates).toHaveLength(1));
    expect(updates[0]!.id).toBe("trg-1");
    expect(updates[0]!.trigger.unattendedFunding).toBe(UnattendedFunding.ANY);
    expect(updates[0]!.trigger.name).toBe("Nightly");
    expect(updates[0]!.trigger.message).toBe("go");
    expect(toastError).not.toHaveBeenCalled();
  });

  it("names the owner's accounts when it may spend them, and switching it on writes ORG_ONLY", async () => {
    const updates: Array<{ trigger: { unattendedFunding: number } }> = [];
    setTransport(
      mockTransport({
        "TriggerService.UpdateTrigger": (request: unknown) => {
          updates.push(request as (typeof updates)[number]);
          return { trigger: wire };
        },
      }),
    );
    render(<AutomationFunding trigger={trigger({ unattendedFunding: "any" })} label="Nightly" />, { wrapper });

    expect(screen.getByTestId("automation-funding").textContent).toContain("Runs on your accounts");
    await userEvent.click(screen.getByRole("switch", { name: "Nightly: Acme's accounts only" }));
    await waitFor(() => expect(updates).toHaveLength(1));
    expect(updates[0]!.trigger.unattendedFunding).toBe(UnattendedFunding.ORG_ONLY);
  });

  it("a refused loosening shows the server's reason", async () => {
    setTransport(
      mockTransport({
        "TriggerService.UpdateTrigger": () => {
          throw new ConnectError("automations in this organization run on its own AI accounts only", Code.FailedPrecondition);
        },
      }),
    );
    render(<AutomationFunding trigger={trigger()} label="Nightly" />, { wrapper });
    await userEvent.click(screen.getByRole("switch", { name: "Nightly: Acme's accounts only" }));

    await waitFor(() => expect(toastError).toHaveBeenCalledTimes(1));
    expect(toastError.mock.calls[0]![1]).toEqual({ description: "automations in this organization run on its own AI accounts only" });
  });

  it("an automation of no org shows nothing", () => {
    setTransport(mockTransport({}));
    const { container } = render(<AutomationFunding trigger={trigger({ orgId: undefined })} label="Nightly" />, { wrapper });
    expect(container.textContent).toBe("");
  });
});

describe("AutomationsFundingBanner", () => {
  it("names the org and the count, links to the oldest automation, and dismisses the Inbox item", async () => {
    const dismissed: string[][] = [];
    setTransport(
      mockTransport({
        "InboxService.ListInbox": () => ({
          items: [
            {
              kind: InboxItemKind.AUTOMATIONS_ON_PERSONAL_ACCOUNTS,
              itemId: "automations_on_personal_accounts:acme:abc",
              payload: { case: "automationsOnPersonalAccounts", value: { orgId: "acme", count: 2, triggerIds: ["trg-old", "trg-new"] } },
            },
          ],
        }),
        "InboxService.DismissInboxItem": (request: unknown) => {
          dismissed.push((request as { itemIds: string[] }).itemIds);
          return {};
        },
      }),
    );
    render(<AutomationsFundingBanner />, { wrapper });

    const banner = await screen.findByTestId("automations-funding-banner");
    expect(banner.textContent).toContain("New automations in Acme run on Acme's accounts. 2 of yours still use your own accounts.");
    // Outside the forge-ui scope the info variant's accent is reliant's pale
    // hover fill, and the sentence is unreadable (primaryInkContrast proves
    // the scoped ink AA).
    expect(screen.getByRole("status").closest(".forge-ui")).not.toBeNull();
    expect(screen.getByRole("link", { name: "Review" }).getAttribute("href")).toContain("trg-old");

    await act(async () => {
      await userEvent.click(screen.getByRole("button", { name: /dismiss/i }));
    });
    await waitFor(() => expect(dismissed).toEqual([["automations_on_personal_accounts:acme:abc"]]));
  });

  it("shows nothing without such an item", async () => {
    setTransport(mockTransport({ "InboxService.ListInbox": () => ({ items: [] }) }));
    const { container } = render(<AutomationsFundingBanner />, { wrapper });
    await act(async () => {});
    expect(container.textContent).toBe("");
  });

  it("says one automation in the singular", () => {
    expect(automationsFundingMessage("Acme", 1)).toBe("New automations in Acme run on Acme's accounts. 1 of yours still uses your own accounts.");
    expect(automationsFundingMessage(undefined, 3)).toBe(
      "New automations in this organization run on the organization's accounts. 3 of yours still use your own accounts.",
    );
  });
});
