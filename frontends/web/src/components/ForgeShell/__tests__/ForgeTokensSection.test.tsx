import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "https://admin.example.test",
  hasControlPlane: true,
}));

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { SENSITIVE_SCOPES } from "@/services/controlPlane/scopeCatalog";
import { useActiveOrgStore } from "@/store/activeOrgStore";

import { ForgeTokensSection } from "../ForgeTokensSection";

/**
 * Settings › API tokens: the secret is shown once after a create the server
 * accepted and never after one it refused, every rpc carries the active org,
 * and the instructions name the variable the forge CLI actually reads.
 */

const ORG = "org_acme";

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ForgeTokensSection />
    </QueryClientProvider>,
  );
}

function createToken(name = "ci-deploy") {
  fireEvent.change(screen.getByTestId("forge-token-name"), { target: { value: name } });
  fireEvent.click(screen.getByRole("button", { name: /create token/i }));
}

/** Pick who the token acts as, then Custom: the scopes one by one. */
function chooseCustom(kind: "Personal" | "Organization" = "Personal") {
  fireEvent.click(screen.getByRole("radio", { name: kind }));
  fireEvent.click(screen.getByRole("radio", { name: "Custom" }));
}

function radio(name: string): HTMLInputElement {
  return screen.getByRole("radio", { name }) as HTMLInputElement;
}

beforeEach(() => {
  useActiveOrgStore.setState({ userId: "u_1", chosenId: ORG, effectiveId: ORG });
});

afterEach(() => {
  setTransport(null);
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null });
});

describe("ForgeTokensSection", () => {
  it("shows the secret once after a successful create, with the right CLI instructions", async () => {
    const createSeen = vi.fn(() => ({ secret: "rlt_shown_once" }));
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyTokens": { response: { tokens: [] } },
        "AccessTokenService.CreateMyToken": createSeen,
      }),
    );
    renderSection();
    await screen.findByText("No tokens yet.");

    createToken();

    const box = await screen.findByTestId("forge-token-secret");
    expect(box.textContent).toContain("rlt_shown_once");
    const usage = screen.getByTestId("forge-token-usage").textContent ?? "";
    expect(usage).toContain("FORGE_CONTROL_PLANE_TOKEN");
    expect(usage).not.toMatch(/\bFORGE_TOKEN\b/);
    expect(createSeen).toHaveBeenCalledWith(
      expect.objectContaining({ orgId: ORG, name: "ci-deploy", scopes: [], bundles: ["developer"] }),
    );
  });

  it("a refused create shows an error and never a secret", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyTokens": { response: { tokens: [] } },
        "AccessTokenService.CreateMyToken": () => {
          throw new ConnectError("denied", Code.PermissionDenied);
        },
      }),
    );
    renderSection();
    await screen.findByText("No tokens yet.");

    createToken();

    expect(await screen.findByText(/could not create the token/i)).toBeTruthy();
    expect(screen.queryByTestId("forge-token-secret")).toBeNull();
  });

  it("discloses secret:read's access to local secret values before creating", async () => {
    setTransport(mockTransport({ "AccessTokenService.ListMyTokens": { response: { tokens: [] } } }));
    renderSection();
    await screen.findByText("No tokens yet.");
    chooseCustom();

    expect(await screen.findByText(/pull the secret VALUES of local environments/i)).toBeTruthy();
  });

  it("a personal token's Custom lists every scope group but the upload-only scopes, sensitive ones included", async () => {
    setTransport(mockTransport({ "AccessTokenService.ListMyTokens": { response: { tokens: [] } } }));
    renderSection();
    await screen.findByText("No tokens yet.");
    chooseCustom();

    for (const group of [
      "deploy", "secrets", "domains", "observability", "security", "tests", "changes", "data", "identity", "billing", "admin",
      "reliant", "machines",
    ]) {
      expect(screen.getByTestId(`forge-token-group-${group}`)).toBeTruthy();
    }
    for (const scope of [
      "obs:read", "obs:triage", "obs:write", "db:query", "runs:start", "billing:read", "vuln:read", "tests:read",
      "changes:write", "changes:launch", "secret:write", "token:write",
    ]) {
      expect(screen.getByRole("checkbox", { name: scope })).toBeTruthy();
    }
    expect(screen.queryByRole("checkbox", { name: "proxy:port" })).toBeNull();
    expect(screen.queryByTestId("forge-token-group-sourcemaps")).toBeNull();
    expect(screen.queryByRole("checkbox", { name: "sourcemaps:write" })).toBeNull();
    expect(screen.queryByRole("checkbox", { name: "tests:write" })).toBeNull();
  });

  it("an organization token's Custom offers only the upload-only scopes", async () => {
    setTransport(mockTransport({ "AccessTokenService.ListMyTokens": { response: { tokens: [] } } }));
    renderSection();
    await screen.findByText("No tokens yet.");
    chooseCustom("Organization");

    expect(screen.getByTestId("forge-token-group-sourcemaps")).toBeTruthy();
    expect(screen.getByRole("checkbox", { name: "sourcemaps:write" })).toBeTruthy();
    expect(screen.getByRole("checkbox", { name: "tests:write" })).toBeTruthy();
    expect(screen.queryByRole("checkbox", { name: "deploy:write" })).toBeNull();
    expect(screen.queryByRole("checkbox", { name: "db:query" })).toBeNull();
    // Nothing is picked for it, so there is nothing to create yet.
    fireEvent.change(screen.getByTestId("forge-token-name"), { target: { value: "maps" } });
    expect((screen.getByRole("button", { name: /create token/i }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("mints a personal token with obs:read alongside the chosen scopes", async () => {
    const createSeen = vi.fn(() => ({ secret: "rlt_obs" }));
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyTokens": { response: { tokens: [] } },
        "AccessTokenService.CreateMyToken": createSeen,
      }),
    );
    renderSection();
    await screen.findByText("No tokens yet.");

    chooseCustom();
    fireEvent.click(screen.getByRole("checkbox", { name: "obs:read" }));
    createToken("ci-gate");

    await screen.findByTestId("forge-token-secret");
    expect(createSeen).toHaveBeenCalledWith(
      expect.objectContaining({ orgId: ORG, name: "ci-gate", scopes: ["deploy:read", "deploy:write", "obs:read"], bundles: [] }),
    );
  });

  it("sourcemaps:write stands alone and mints an org token through CreateToken", async () => {
    const orgCreate = vi.fn(() => ({ secret: "rlat_maps" }));
    const myCreate = vi.fn(() => ({ secret: "rlt_mine" }));
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyTokens": { response: { tokens: [] } },
        "AccessTokenService.CreateToken": orgCreate,
        "AccessTokenService.CreateMyToken": myCreate,
      }),
    );
    renderSection();
    await screen.findByText("No tokens yet.");

    chooseCustom("Organization");
    fireEvent.click(screen.getByRole("checkbox", { name: "sourcemaps:write" }));
    expect(screen.getByTestId("forge-token-org-note")).toBeTruthy();
    createToken("desktop-maps");

    expect((await screen.findByTestId("forge-token-secret")).textContent).toContain("rlat_maps");
    expect(orgCreate).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, name: "desktop-maps", scopes: ["sourcemaps:write"] }));
    expect(myCreate).not.toHaveBeenCalled();
  });

  it("tests:write stands alone and mints an org token through CreateToken", async () => {
    const orgCreate = vi.fn(() => ({ secret: "rlat_tests" }));
    const myCreate = vi.fn(() => ({ secret: "rlt_mine" }));
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyTokens": { response: { tokens: [] } },
        "AccessTokenService.CreateToken": orgCreate,
        "AccessTokenService.CreateMyToken": myCreate,
      }),
    );
    renderSection();
    await screen.findByText("No tokens yet.");

    chooseCustom("Organization");
    fireEvent.click(screen.getByRole("checkbox", { name: "sourcemaps:write" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "tests:write" }));
    expect((screen.getByRole("checkbox", { name: "sourcemaps:write" }) as HTMLInputElement).checked).toBe(false);
    expect(screen.getByTestId("forge-token-org-note")).toBeTruthy();
    createToken("pr-tests");

    expect((await screen.findByTestId("forge-token-secret")).textContent).toContain("rlat_tests");
    expect(orgCreate).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, name: "pr-tests", scopes: ["tests:write"] }));
    expect(myCreate).not.toHaveBeenCalled();
  });

  it("switching back to a personal token drops an upload-only scope and starts from the developer bundle", async () => {
    setTransport(mockTransport({ "AccessTokenService.ListMyTokens": { response: { tokens: [] } } }));
    renderSection();
    await screen.findByText("No tokens yet.");

    chooseCustom("Organization");
    fireEvent.click(screen.getByRole("checkbox", { name: "sourcemaps:write" }));
    fireEvent.click(screen.getByRole("radio", { name: "Personal" }));

    expect(radio("Developer bundle").checked).toBe(true);
    expect(screen.queryByTestId("forge-token-org-note")).toBeNull();
    fireEvent.click(screen.getByRole("radio", { name: "Custom" }));
    expect(screen.queryByRole("checkbox", { name: "sourcemaps:write" })).toBeNull();
    expect((screen.getByRole("checkbox", { name: "deploy:write" }) as HTMLInputElement).checked).toBe(true);
  });

  describe("scope bundles", () => {
    it("a personal token starts with the developer bundle, and offers ci and admin too", async () => {
      setTransport(mockTransport({ "AccessTokenService.ListMyTokens": { response: { tokens: [] } } }));
      renderSection();
      await screen.findByText("No tokens yet.");

      expect(radio("Personal").checked).toBe(true);
      expect(radio("Developer bundle").checked).toBe(true);
      expect(radio("CI bundle")).toBeTruthy();
      expect(radio("Admin bundle")).toBeTruthy();
      expect(radio("Custom").checked).toBe(false);
    });

    it("an organization token starts with the ci bundle and never offers developer", async () => {
      setTransport(mockTransport({ "AccessTokenService.ListMyTokens": { response: { tokens: [] } } }));
      renderSection();
      await screen.findByText("No tokens yet.");

      fireEvent.click(screen.getByRole("radio", { name: "Organization" }));

      expect(radio("CI bundle").checked).toBe(true);
      expect(radio("Admin bundle")).toBeTruthy();
      expect(screen.queryByRole("radio", { name: "Developer bundle" })).toBeNull();
      expect(screen.getByTestId("forge-token-org-note")).toBeTruthy();
    });

    it("mints an organization ci token through CreateToken, naming the bundle and no scope", async () => {
      const orgCreate = vi.fn(() => ({ secret: "rlat_ci" }));
      const myCreate = vi.fn(() => ({ secret: "rlt_mine" }));
      setTransport(
        mockTransport({
          "AccessTokenService.ListMyTokens": { response: { tokens: [] } },
          "AccessTokenService.CreateToken": orgCreate,
          "AccessTokenService.CreateMyToken": myCreate,
        }),
      );
      renderSection();
      await screen.findByText("No tokens yet.");

      fireEvent.click(screen.getByRole("radio", { name: "Organization" }));
      createToken("org-ci");

      expect((await screen.findByTestId("forge-token-secret")).textContent).toContain("rlat_ci");
      expect(orgCreate).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, name: "org-ci", scopes: [], bundles: ["ci"] }));
      expect(myCreate).not.toHaveBeenCalled();
    });

    it("shows what the chosen bundle resolves to, and that no sensitive scope is ever in it", async () => {
      setTransport(mockTransport({ "AccessTokenService.ListMyTokens": { response: { tokens: [] } } }));
      renderSection();
      await screen.findByText("No tokens yet.");

      const shown = () =>
        Array.from(screen.getByTestId("forge-token-bundle-scopes").querySelectorAll("li")).map((li) => li.textContent);

      // developer (the default): a person's own authority, not secret:read.
      expect(shown()).toEqual(expect.arrayContaining(["deploy:write", "reliant:api", "runs:start", "obs:read"]));
      expect(shown()).not.toContain("secret:read");

      for (const bundle of ["CI bundle", "Developer bundle", "Admin bundle"]) {
        fireEvent.click(screen.getByRole("radio", { name: bundle }));
        const listed = shown();
        expect(listed.length, bundle).toBeGreaterThan(0);
        for (const sensitive of SENSITIVE_SCOPES) expect(listed, `${bundle} lists ${sensitive}`).not.toContain(sensitive);
      }
      // admin carries secret:read; ci holds only the org's CI scopes.
      expect(shown()).toContain("secret:read");
      fireEvent.click(screen.getByRole("radio", { name: "CI bundle" }));
      expect(shown()).toEqual(["deploy:read", "deploy:write", "vuln:read", "tests:write"]);

      const note = screen.getByTestId("forge-token-sensitive-note").textContent ?? "";
      expect(note).toMatch(/never part of a bundle/i);
      expect(note).toMatch(/choose Custom and pick it explicitly/i);
      for (const sensitive of SENSITIVE_SCOPES) expect(note).toContain(sensitive);
    });

    it("lists each token's bundles next to the server's resolved scopes", async () => {
      setTransport(
        mockTransport({
          "AccessTokenService.ListMyTokens": {
            response: {
              tokens: [
                { id: "t_dev", name: "laptop", displayPrefix: "rlt_dv", bundles: ["developer"], scopes: ["deploy:read", "reliant:api"] },
                { id: "t_plain", name: "ci-deploy", displayPrefix: "rlt_ab", scopes: ["deploy:write"] },
              ],
            },
          },
        }),
      );
      renderSection();

      const row = await screen.findByTestId("forge-token-t_dev");
      expect(screen.getByTestId("forge-token-t_dev-bundles").textContent).toContain("Developer bundle");
      expect(row.textContent).toContain("deploy:read, reliant:api");
      expect(screen.queryByTestId("forge-token-t_plain-bundles")).toBeNull();
      expect(screen.getByTestId("forge-token-t_plain").textContent).toContain("deploy:write");
    });
  });

  it("shows the org's automation tokens to an admin, and revokes them with RevokeToken", async () => {
    const revokeSeen = vi.fn(() => ({}));
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyTokens": { response: { tokens: [] } },
        "AccessTokenService.ListTokens": {
          response: { tokens: [{ id: "t_maps", name: "desktop-maps", displayPrefix: "rlat_m", scopes: ["sourcemaps:write"] }] },
        },
        "AccessTokenService.RevokeToken": revokeSeen,
      }),
    );
    renderSection();

    const row = await screen.findByTestId("forge-org-token-t_maps");
    fireEvent.click(row.querySelector("button") as HTMLButtonElement);

    await waitFor(() => expect(revokeSeen).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, id: "t_maps" })));
  });

  it("hides the org tokens section from a member the server refuses", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyTokens": { response: { tokens: [] } },
        "AccessTokenService.ListTokens": () => {
          throw new ConnectError("admin only", Code.PermissionDenied);
        },
      }),
    );
    renderSection();
    await screen.findByText("No tokens yet.");

    expect(screen.queryByTestId("forge-org-tokens")).toBeNull();
  });

  it("lists only live tokens and revokes in the active org", async () => {
    const revokeSeen = vi.fn(() => ({}));
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyTokens": {
          response: {
            tokens: [
              { id: "t_live", name: "ci-deploy", displayPrefix: "rlt_ab", scopes: ["deploy:write"] },
              { id: "t_ephemeral", name: "session", ephemeral: true },
            ],
          },
        },
        "AccessTokenService.RevokeMyToken": revokeSeen,
      }),
    );
    renderSection();

    const row = await screen.findByTestId("forge-token-t_live");
    expect(screen.queryByTestId("forge-token-t_ephemeral")).toBeNull();

    fireEvent.click(row.querySelector("button") as HTMLButtonElement);

    await waitFor(() => expect(revokeSeen).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, id: "t_live" })));
  });
});
