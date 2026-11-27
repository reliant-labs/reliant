// Copyright (c) 2026 Reliant Labs

/**
 * The /gtm route guard (GtmShell): a page renders only on the server's yes,
 * for the org it said yes for. While the answer loads nothing is drawn; a no,
 * a failed read, or a tab with no org gets the app's not-found page (the
 * home), as an unknown route does.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import type { OrgScope } from "@/lib/orgScope";
import { homePath } from "@/lib/paths";
import { mockTransport, type MockHandlers } from "@/lib/test-utils";

import { GtmShell, useGtmOrgId } from "../GtmShell";

const scope = vi.hoisted(() => ({ current: { ready: true, orgId: "org-1", error: null, retry: () => {} } as OrgScope }));
vi.mock("@/lib/orgScope", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/orgScope")>()),
  useOrgScope: () => scope.current,
}));

// The not-found page is a redirect home; record where it goes instead of routing.
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  Navigate: ({ to }: { to: string }) => <div data-testid="not-found" data-to={to} />,
}));
// The area chrome needs a router; the guard is what is under test.
vi.mock("../../../Layout/AreaShell", () => ({
  AreaShell: ({ children }: { children: ReactNode }) => <div data-testid="area-shell">{children}</div>,
}));

let client: QueryClient;
afterEach(() => {
  cleanup();
  client.clear();
  scope.current = { ready: true, orgId: "org-1", error: null, retry: () => {} };
});

function Page() {
  return <p data-testid="page">org {useGtmOrgId()}</p>;
}

function renderGuarded(handlers: MockHandlers) {
  setTransport(mockTransport(handlers));
  client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  render(
    <QueryClientProvider client={client}>
      <GtmShell page="ideas">
        <Page />
      </GtmShell>
    </QueryClientProvider>,
  );
}

describe("GtmShell, the /gtm route guard", () => {
  it("renders the page on yes, in the org the gate said yes for", async () => {
    renderGuarded({ "GtmService.GetGtmAccess": { response: { enabled: true } } });
    expect(await screen.findByTestId("page")).toHaveTextContent("org org-1");
    expect(screen.queryByTestId("not-found")).not.toBeInTheDocument();
  });

  it("renders NotFound when the server says no", async () => {
    renderGuarded({ "GtmService.GetGtmAccess": { response: { enabled: false } } });
    expect(await screen.findByTestId("not-found")).toHaveAttribute("data-to", homePath());
    expect(screen.queryByTestId("page")).not.toBeInTheDocument();
  });

  it("renders NotFound when the read fails", async () => {
    renderGuarded({ "GtmService.GetGtmAccess": { error: new ConnectError("not found", Code.NotFound) } });
    expect(await screen.findByTestId("not-found")).toBeInTheDocument();
    expect(screen.queryByTestId("page")).not.toBeInTheDocument();
  });

  it("draws nothing while the answer loads", async () => {
    const asked = vi.fn();
    renderGuarded({ "GtmService.GetGtmAccess": () => (asked(), new Promise(() => {})) });
    await waitFor(() => expect(asked).toHaveBeenCalled());
    expect(screen.queryByTestId("page")).not.toBeInTheDocument();
    expect(screen.queryByTestId("not-found")).not.toBeInTheDocument();
  });

  it("renders NotFound, asking nothing, when the tab names no org", async () => {
    scope.current = { ready: true, orgId: null, error: null, retry: () => {} };
    const asked = vi.fn(() => ({ enabled: true }));
    renderGuarded({ "GtmService.GetGtmAccess": asked });
    expect(await screen.findByTestId("not-found")).toBeInTheDocument();
    await act(() => new Promise((resolve) => setTimeout(resolve, 0)));
    expect(asked).not.toHaveBeenCalled();
  });
});
