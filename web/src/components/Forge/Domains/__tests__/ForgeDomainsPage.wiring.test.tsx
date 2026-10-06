// Copyright (c) 2025 Reliant Labs

/**
 * END-TO-END THROUGH THE REAL LAYERS: the page renders, the hooks call the
 * real service module, and the service module calls a real Connect client —
 * only the TRANSPORT is a stub.
 *
 * This is the test that would have caught the wiring being wrong rather than
 * the components being wrong. Everything else in this directory renders a
 * component against object literals, which proves the pixels and proves
 * nothing about whether `ListDomains` is even reachable from this app. Here
 * the request that leaves `services/forge/domains.ts` is inspected, so a
 * field sent under the wrong name — or an RPC nobody wired — fails here
 * instead of in production.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createRouterTransport } from "@connectrpc/connect";

import {
  DeployCustomDomainState,
  DomainSource,
} from "@/gen/controlplane/controlplane/v1/deploy_pb";
import { DomainService } from "@/gen/controlplane/services/domain/v1/domain_pb";

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://127.0.0.1:8090",
}));

vi.mock("@tanstack/react-router", () => ({
  useSearch: () => ({ project: "proj-1" }),
}));

vi.mock("@/store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) =>
    selector({ currentProject: { id: "proj-1", name: "shop" } }),
}));

// By default no daemon and no cloud env list — the page must render the
// org's domains regardless, which is the property that matters: a domain is
// not a project's, so a sleeping daemon cannot blank this screen. The target
// test below fills these in.
const envState = vi.hoisted(() => ({
  envs: [] as unknown[],
  statuses: new Map<string, unknown>(),
}));
vi.mock("@/hooks/forge-queries", () => ({
  useForgeEnvironments: () => ({ envs: envState.envs }),
  useCloudEnvStatuses: () => envState.statuses,
}));

/** Every request the stub transport received, so the wire shape is assertable. */
const calls: { rpc: string; req: unknown }[] = [];

/** The apex domain exactly as control-plane's GetDomain returns it. */
const PENDING_APEX = {
  id: "dom-1",
  hostname: "example.com",
  state: DeployCustomDomainState.PENDING_DNS,
  source: DomainSource.EXTERNAL,
  requiredRecords: [
    { type: "A", name: "example.com", value: "34.63.203.181" },
    {
      type: "TXT",
      name: "_reliant-challenge.example.com",
      value: "reliant-verify-8f3a91c2e7b04d56",
    },
  ],
  lastError: "",
};

const transport = createRouterTransport(({ service }) => {
  service(DomainService, {
    listDomains(req) {
      calls.push({ rpc: "ListDomains", req });
      return { domains: [PENDING_APEX] };
    },
    createDomain(req) {
      calls.push({ rpc: "CreateDomain", req });
      return { domain: { ...PENDING_APEX, id: "dom-2", hostname: req.hostname } };
    },
    verifyDomain(req) {
      calls.push({ rpc: "VerifyDomain", req });
      return { domain: PENDING_APEX };
    },
  });
});

vi.mock("@/services/controlPlane/client", () => ({
  getControlPlaneClient: vi.fn(),
}));

import { getControlPlaneClient } from "@/services/controlPlane/client";
import { createClient } from "@connectrpc/connect";
import { ForgeDomainsPage } from "../ForgeDomainsPage";

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <ForgeDomainsPage />
    </QueryClientProvider>
  );
}

beforeEach(() => {
  calls.length = 0;
  envState.envs = [];
  envState.statuses = new Map();
  vi.mocked(getControlPlaneClient).mockImplementation(((service: never) =>
    createClient(service, transport)) as never);
});

describe("the Domains page, through the real service layer", () => {
  it("calls ListDomains and renders what came back", async () => {
    renderPage();

    await waitFor(() => {
      expect(screen.getByTestId("domain-row-example.com")).toBeInTheDocument();
    });
    expect(calls.map((c) => c.rpc)).toContain("ListDomains");
    expect(screen.getByTestId("domain-state-pending-dns")).toBeInTheDocument();
    // Unbound is stated, not left blank.
    expect(screen.getByText(/not serving anything/i)).toBeInTheDocument();
  });

  it("expands to the records the server actually sent", async () => {
    const user = userEvent.setup();
    renderPage();

    await waitFor(() => expect(screen.getByTestId("domain-row-example.com")).toBeInTheDocument());
    await user.click(screen.getByRole("button", { expanded: false }));

    const table = await screen.findByTestId("dns-records-table");
    expect(table).toHaveTextContent("34.63.203.181");
    expect(table).toHaveTextContent("reliant-verify-8f3a91c2e7b04d56");
  });

  it("sends the hostname to CreateDomain under the name the server expects", async () => {
    const user = userEvent.setup();
    renderPage();

    await waitFor(() => expect(screen.getByTestId("domain-row-example.com")).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: /add domain/i }));
    await user.type(await screen.findByLabelText(/hostname/i), "api.example.com");
    // The dialog's own submit, not the page header's.
    const dialog = screen.getByRole("dialog");
    await user.click(
      Array.from(dialog.querySelectorAll("button")).find(
        (b) => b.textContent?.trim() === "Add domain"
      ) as HTMLElement
    );

    await waitFor(() => {
      const created = calls.find((c) => c.rpc === "CreateDomain");
      expect(created).toBeDefined();
      expect((created!.req as { hostname: string }).hostname).toBe("api.example.com");
    });
  });

  it("offers as targets what the control plane runs that can serve HTTP — no daemon needed", async () => {
    // A cloud env the daemon never described: only the control plane knows it.
    envState.envs = [
      {
        name: "prod",
        where: "cloud",
        forge: null,
        cloud: { id: "env-prod", name: "prod", project: "shop", kind: "persistent" },
      },
    ];
    envState.statuses = new Map([
      [
        "env-prod",
        {
          isLoading: false,
          error: null,
          data: {
            verdict: "converged",
            currentPromotion: null,
            workloads: [
              { name: "web", tier: "static" },
              { name: "api", tier: "backend", url: "https://api-abc.reliant.run" },
              { name: "jobs", tier: "backend" },
              { name: "db", tier: "database" },
            ],
          },
        },
      ],
    ]);
    const user = userEvent.setup();
    renderPage();

    await waitFor(() => expect(screen.getByTestId("domain-row-example.com")).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: /add domain/i }));

    const target = await screen.findByLabelText(/^target$/i);
    expect(Array.from(target.querySelectorAll("option")).map((o) => o.textContent)).toEqual([
      "Choose a service or static site…",
      "api — service",
      "web — static site",
    ]);
  });

  it("sends the domain id to VerifyDomain when the user checks DNS", async () => {
    const user = userEvent.setup();
    renderPage();

    await waitFor(() => expect(screen.getByTestId("domain-row-example.com")).toBeInTheDocument());
    await user.click(screen.getByRole("button", { expanded: false }));
    await user.click(await screen.findByRole("button", { name: /check dns now/i }));

    await waitFor(() => {
      const verified = calls.find((c) => c.rpc === "VerifyDomain");
      expect(verified).toBeDefined();
      expect((verified!.req as { domainId: string }).domainId).toBe("dom-1");
    });
  });
});
