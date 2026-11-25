// Copyright (c) 2025 Reliant Labs

/**
 * The Experiments page on the public demo: the demo bundle's connect module
 * (lib/connect.demo.ts, swapped in for lib/connect.ts as
 * vite-plugin-demo-bundle does) over the demo's mock transport. The demo has
 * no ChangesService sample data, so every call is Unimplemented with
 * SAMPLE_DATA_UNAVAILABLE and the page says Experiments aren't available.
 */

import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/connect", async () => await import("@/lib/connect.demo"));
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
vi.mock("@/services/controlPlane/orgs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/orgs")>()),
  currentOrgId: async () => "org-1",
}));
vi.mock("../../Console/consoleContext", () => ({
  useForgeConsole: () => ({
    forgeProject: "shop",
    env: "all",
    roster: { isLoading: false, envs: [{ name: "prod", lifecycle: "deployed", live: { id: "env-prod", name: "prod" }, forge: null, source: "backend" }] },
    setEnv: vi.fn(),
    href: () => "/forge/p/shop/experiments",
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

import { screen } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";

import { ChangesService } from "@/gen/controlplane/services/changes/v1/changes_pb";
import { connectClient, installShellTransport, SAMPLE_DATA_UNAVAILABLE, setTransport } from "@/lib/connect.demo";
import { renderWithQuery } from "@/test/renderWithQuery";

import { createDemoTransport } from "../../Demo/demoTransport";
import { ExperimentsPage } from "../ExperimentsPage";

beforeEach(() => {
  setTransport(null);
  installShellTransport(createDemoTransport({ source: "fixtures", url: "" }));
});

afterEach(() => {
  installShellTransport(null as never);
});

describe("ExperimentsPage on the demo", () => {
  it("answers ChangesService with the demo's not-available error", async () => {
    const err = await connectClient(ChangesService)
      .listChanges({ orgId: "org-1" })
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ConnectError);
    expect((err as ConnectError).code).toBe(Code.Unimplemented);
    expect((err as ConnectError).rawMessage).toBe(SAMPLE_DATA_UNAVAILABLE);
  });

  it("says Experiments aren't available here", async () => {
    renderWithQuery(<ExperimentsPage />);
    expect(await screen.findByText("Experiments aren't available here yet")).toBeInTheDocument();
  });
});
