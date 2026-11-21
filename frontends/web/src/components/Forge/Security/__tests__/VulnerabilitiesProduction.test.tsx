// Copyright (c) 2025 Reliant Labs

/**
 * The Vulnerabilities page with no VulnerabilityApiProvider: the production
 * client, the shared control-plane transport and the page together, with only
 * fetch faked. What a real control plane can answer must never read as clean:
 * not a release with no artifacts, and not a control plane whose reads answer
 * Unimplemented (control-plane before #3161 implements them, or one without
 * the service mounted).
 */

import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { browserAuthTokenProvider, setAuthTokenProvider } from "@/api/authProvider";

import { VulnerabilitiesView } from "../VulnerabilitiesPage";
import type { VulnSearch } from "../vulnModel";

vi.mock("@/services/controlPlane/config", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/config")>()),
  CONTROL_PLANE_API_URL: "https://admin.example.test",
  hasControlPlane: true,
}));

vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

const json = (body: object, status = 200) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
const methodOf = (url: RequestInfo | URL) => String(url).split("/").at(-1);

beforeEach(() => setAuthTokenProvider({ getToken: async () => "test-token", hasSession: async () => true, refresh: async () => null }));
afterEach(() => {
  setAuthTokenProvider(browserAuthTokenProvider);
  vi.unstubAllGlobals();
});

function renderProduction(fetchRpc: (input: RequestInfo | URL) => Promise<Response>, search: VulnSearch = {}) {
  vi.stubGlobal("fetch", vi.fn(fetchRpc));
  // No VulnerabilityApiProvider: the context's default is the production client.
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <VulnerabilitiesView scope={{ project: "shop" }} search={search} onSearch={() => undefined} advisoryHref={(id) => `/advisory/${id}`} sbomsHref={() => "/sboms"} />
    </QueryClientProvider>
  );
}

it("says the control plane doesn't report vulnerabilities yet while its reads answer Unimplemented", async () => {
  renderProduction(async () => json({ code: "unimplemented", message: "VulnerabilityService.GetVulnerabilitySummary is not implemented" }, 501));
  const state = await screen.findByTestId("vuln-error-not-configured");
  expect(state).toHaveTextContent("doesn't report vulnerabilities yet");
  expect(screen.queryByTestId("vuln-none-known")).not.toBeInTheDocument();
});

it("says the same of a control plane without the service", async () => {
  renderProduction(async () => new Response("Not Found", { status: 404 }));
  expect(await screen.findByTestId("vuln-error-not-configured")).toBeInTheDocument();
  expect(screen.queryByTestId("vuln-none-known")).not.toBeInTheDocument();
});

it("does not call a release that answers with no artifacts clean", async () => {
  const fetchRpc = vi.fn(async (url: RequestInfo | URL) => {
    switch (methodOf(url)) {
      case "GetVulnerabilitySummary":
        return json({
          environments: [
            {
              environmentId: "env-1",
              environmentName: "prod",
              releaseId: "rel-1",
              releaseVersion: "v1.0.0",
              scanState: "VULNERABILITY_SCAN_STATE_SCANNED",
              coverage: { artifacts: 1, withSbom: 1, scanned: 1 },
              lastScannedAt: "2026-11-20T00:00:00Z",
            },
          ],
          database: { scanner: "grype", stale: false, builtAt: "2026-11-20T00:00:00Z" },
        });
      case "GetReleaseVulnerabilities":
        return json({ releaseId: "rel-1", releaseVersion: "v1.0.0", artifacts: [] });
      default:
        return json({ findings: [] });
    }
  });
  renderProduction(fetchRpc, { release: "v1.0.0" });
  expect(await screen.findByTestId("vuln-none-in-scanned")).toHaveTextContent("not a clean bill");
  expect(screen.queryByTestId("vuln-none-known")).not.toBeInTheDocument();
  expect(fetchRpc.mock.calls.map(([url]) => methodOf(url))).toEqual(expect.arrayContaining(["GetVulnerabilitySummary", "ListVulnerabilities", "GetReleaseVulnerabilities"]));
});
