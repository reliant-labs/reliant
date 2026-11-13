// @vitest-environment node
/**
 * The forge shell's personal-token client (forgeTokens.ts), through the real
 * generated client and request/response types over a mock transport.
 *
 * What is pinned: every rpc carries the active org, scopes and expiry reach
 * the wire as chosen, revoked and ephemeral credentials are never listed, a
 * refused mutation stays an error, the scope labels disclose what each
 * scope really authorizes, every scope the server accepts unbound is offered,
 * and a registry-only scope (sourcemaps:write) is minted as an org token.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "https://admin.example.test",
  hasControlPlane: true,
}));

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import {
  createForgeToken,
  FORGE_TOKEN_ENV,
  FORGE_TOKEN_SCOPE_GROUPS,
  FORGE_TOKEN_SCOPES,
  listForgeTokens,
  listOrgForgeTokens,
  revokeForgeToken,
  revokeOrgForgeToken,
} from "../forgeTokens";

const ORG = "org_acme";
const CREATED = new Date("2026-10-01T00:00:00Z");

afterEach(() => setTransport(null));

describe("listForgeTokens", () => {
  it("asks for the active org's tokens", async () => {
    const seen = vi.fn(() => ({ tokens: [] }));
    setTransport(mockTransport({ "AccessTokenService.ListMyTokens": seen }));

    await listForgeTokens(ORG);

    expect(seen).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG }));
  });

  it("lists live tokens only: a revoked or ephemeral credential is never shown", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyTokens": {
          response: {
            tokens: [
              { id: "t_live", name: "ci-deploy", displayPrefix: "rlt_ab", scopes: ["deploy:write"], createdAt: timestampFromDate(CREATED) },
              { id: "t_revoked", name: "old", revokedAt: timestampFromDate(CREATED) },
              { id: "t_ephemeral", name: "session", ephemeral: true },
            ],
          },
        },
      }),
    );

    const tokens = await listForgeTokens(ORG);

    expect(tokens).toEqual([
      {
        id: "t_live",
        name: "ci-deploy",
        prefix: "rlt_ab",
        scopes: ["deploy:write"],
        allScopes: false,
        createdAt: CREATED,
        expiresAt: undefined,
        lastUsedAt: undefined,
      },
    ]);
  });

  it("a refused list is an error, not an empty list", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyTokens": () => {
          throw new ConnectError("denied", Code.PermissionDenied);
        },
      }),
    );
    await expect(listForgeTokens(ORG)).rejects.toMatchObject({ code: Code.PermissionDenied });
  });
});

describe("createForgeToken", () => {
  it("sends the org, name, scopes and an expiry the chosen days from now", async () => {
    const now = Date.parse("2026-10-01T00:00:00Z");
    const seen = vi.fn(() => ({ secret: "rlt_secret" }));
    setTransport(mockTransport({ "AccessTokenService.CreateMyToken": seen }));

    const res = await createForgeToken({ orgId: ORG, name: "ci", scopes: ["deploy:read", "deploy:write"], expiresInDays: 90, nowMs: now });

    expect(res).toEqual({ secret: "rlt_secret", kind: "personal" });
    expect(seen).toHaveBeenCalledWith(
      expect.objectContaining({
        orgId: ORG,
        name: "ci",
        scopes: ["deploy:read", "deploy:write"],
        expiresAt: expect.objectContaining({ seconds: BigInt(now / 1000 + 90 * 86_400) }),
      }),
    );
  });

  it("a token that never expires sends no expiry", async () => {
    const seen = vi.fn(() => ({ secret: "rlt_secret" }));
    setTransport(mockTransport({ "AccessTokenService.CreateMyToken": seen }));

    await createForgeToken({ orgId: ORG, name: "ci", scopes: ["deploy:read"], expiresInDays: null });

    const [req] = seen.mock.calls[0] as unknown as [{ expiresAt?: unknown }];
    expect(req.expiresAt).toBeUndefined();
  });

  it("a refused create rejects (no secret is returned)", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.CreateMyToken": () => {
          throw new ConnectError("denied", Code.PermissionDenied);
        },
      }),
    );
    await expect(createForgeToken({ orgId: ORG, name: "ci", scopes: ["deploy:read"], expiresInDays: 30 })).rejects.toMatchObject({
      code: Code.PermissionDenied,
    });
  });
});

describe("createForgeToken with sourcemaps:write", () => {
  it("mints an org automation token through CreateToken, never CreateMyToken", async () => {
    const org = vi.fn(() => ({ secret: "rlat_maps" }));
    const mine = vi.fn(() => ({ secret: "rlat_mine" }));
    setTransport(mockTransport({ "AccessTokenService.CreateToken": org, "AccessTokenService.CreateMyToken": mine }));

    const res = await createForgeToken({ orgId: ORG, name: "desktop-maps", scopes: ["sourcemaps:write"], expiresInDays: 365 });

    expect(res).toEqual({ secret: "rlat_maps", kind: "org" });
    expect(org).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, name: "desktop-maps", scopes: ["sourcemaps:write"] }));
    expect(mine).not.toHaveBeenCalled();
  });

  it("refuses to combine it with another scope, without calling the server", async () => {
    const org = vi.fn(() => ({ secret: "x" }));
    const mine = vi.fn(() => ({ secret: "x" }));
    setTransport(mockTransport({ "AccessTokenService.CreateToken": org, "AccessTokenService.CreateMyToken": mine }));

    await expect(
      createForgeToken({ orgId: ORG, name: "ci", scopes: ["sourcemaps:write", "deploy:write"], expiresInDays: null }),
    ).rejects.toThrow(/cannot be combined/);
    expect(org).not.toHaveBeenCalled();
    expect(mine).not.toHaveBeenCalled();
  });
});

describe("listOrgForgeTokens", () => {
  it("lists the org's live automation tokens only: no person's token, revoked or ephemeral one", async () => {
    const seen = vi.fn(() => ({
      tokens: [
        { id: "t_maps", name: "desktop-maps", displayPrefix: "rlat_m", scopes: ["sourcemaps:write"] },
        { id: "t_person", name: "laptop", actingUserId: "u_1", scopes: ["deploy:read"] },
        { id: "t_revoked", name: "old", revokedAt: timestampFromDate(CREATED) },
        { id: "t_ephemeral", name: "session", ephemeral: true },
      ],
    }));
    setTransport(mockTransport({ "AccessTokenService.ListTokens": seen }));

    const tokens = await listOrgForgeTokens(ORG);

    expect(seen).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG }));
    expect(tokens.map((t) => t.id)).toEqual(["t_maps"]);
  });

  it("revokes an org token through RevokeToken in the active org", async () => {
    const seen = vi.fn(() => ({}));
    setTransport(mockTransport({ "AccessTokenService.RevokeToken": seen }));

    await revokeOrgForgeToken(ORG, "t_maps");

    expect(seen).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, id: "t_maps" }));
  });
});

describe("revokeForgeToken", () => {
  it("revokes the token in the active org", async () => {
    const seen = vi.fn(() => ({}));
    setTransport(mockTransport({ "AccessTokenService.RevokeMyToken": seen }));

    await revokeForgeToken(ORG, "t_live");

    expect(seen).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, id: "t_live" }));
  });

  it("a refused revoke rejects", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.RevokeMyToken": () => {
          throw new ConnectError("not found", Code.NotFound);
        },
      }),
    );
    await expect(revokeForgeToken(ORG, "t_gone")).rejects.toMatchObject({ code: Code.NotFound });
  });
});

describe("what the token UI tells a person", () => {
  it("names the variable the forge CLI reads by default", () => {
    // forge internal/cloud/credential.go DefaultTokenEnv.
    expect(FORGE_TOKEN_ENV).toBe("FORGE_CONTROL_PLANE_TOKEN");
  });

  it("discloses that secret:read pulls local environments' secret VALUES", () => {
    const label = FORGE_TOKEN_SCOPES.find((s) => s.scope === "secret:read")?.label ?? "";
    expect(label).toMatch(/metadata/i);
    expect(label).toMatch(/values/i);
    expect(label).toMatch(/local environments/i);
  });

  it("offers obs:read and sourcemaps:write, which forge's CI skills need", () => {
    const offered = FORGE_TOKEN_SCOPES.map((s) => s.scope);
    expect(offered).toContain("obs:read");
    expect(offered).toContain("sourcemaps:write");
  });

  it("offers every scope of forge's catalog except the three that must be bound to a resource", () => {
    // forge pkg/accesstoken/scope.go AllScopes. A scope forge adds fails this
    // test until it is offered here or listed as binding-only.
    const catalog = [
      "deploy:read", "deploy:write", "deploy:config:read", "billing:read", "cluster:manage", "token:read",
      "token:write", "reliant:api", "daemon:connect", "llm:invoke", "proxy:port", "mcp:connector",
      "daemon:resume", "daemon:manage", "secret:read", "secret:write", "domain:read", "domain:write",
      "identity:read", "identity:manage", "obs:read", "obs:triage", "obs:write", "db:query", "storage:read",
      "runs:start", "runs:read", "runs:control", "triggers:fire", "triggers:manage", "inbox:read",
      "inbox:manage", "approvals:decide", "questions:answer", "sourcemaps:write",
    ];
    const bindingOnly = ["proxy:port", "mcp:connector", "daemon:resume"];
    const offered = FORGE_TOKEN_SCOPES.map((s) => s.scope);

    expect(new Set(offered).size).toBe(offered.length);
    expect([...offered].sort()).toEqual(catalog.filter((s) => !bindingOnly.includes(s)).sort());
  });

  it("describes every scope and titles every group", () => {
    for (const group of FORGE_TOKEN_SCOPE_GROUPS) {
      expect(group.title).not.toBe("");
      for (const { scope, label } of group.scopes) expect(label, scope).not.toBe("");
    }
  });
});
