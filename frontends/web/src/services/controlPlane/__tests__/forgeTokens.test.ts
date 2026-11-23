// @vitest-environment node
/**
 * The forge shell's personal-token client (forgeTokens.ts), through the real
 * generated client and request/response types over a mock transport.
 *
 * What is pinned: every rpc carries the active org, scopes and expiry reach
 * the wire as chosen, revoked and ephemeral credentials are never listed, a
 * refused mutation stays an error, the scope labels disclose what each
 * scope really authorizes, every scope the server accepts unbound is offered,
 * and an upload-only scope (sourcemaps:write, tests:write) is minted alone as
 * an org token.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { readdirSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

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
  isOrgTokenOnlyScope,
  listForgeTokens,
  listOrgForgeTokens,
  revokeForgeToken,
  revokeOrgForgeToken,
} from "../forgeTokens";

const ORG = "org_acme";

const MIGRATIONS = resolve(dirname(fileURLToPath(import.meta.url)), "../../../../../../db/migrations");

/**
 * forge's scope catalog (pkg/accesstoken AllScopes) as reliant vendors it:
 * the scope list of the newest migration that redefines the access_tokens
 * CHECK. Migrations apply in filename order, so the last one is the live set.
 */
function forgeScopeCatalog(): string[] {
  const newest = readdirSync(MIGRATIONS)
    .filter((f) => f.endsWith(".up.sql"))
    .sort()
    .map((f) => readFileSync(resolve(MIGRATIONS, f), "utf8"))
    .filter((sql) => /ADD CONSTRAINT access_tokens_scopes CHECK/.test(sql))
    .at(-1);
  if (!newest) throw new Error(`no access_tokens_scopes CHECK under ${MIGRATIONS}`);
  // Strip SQL comments first: a migration's header may quote the predicate
  // ("scopes <@ ARRAY[40 scopes]"), which would otherwise match before the DDL.
  const ddl = newest.replace(/--.*$/gm, "");
  const list = /scopes <@ ARRAY\[([\s\S]*?)\]/.exec(ddl)?.[1] ?? "";
  return [...list.matchAll(/'([^']+)'/g)].map((m) => m[1]);
}
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

describe("createForgeToken with tests:write", () => {
  it("mints an upload-only org token through CreateToken, never CreateMyToken", async () => {
    const org = vi.fn(() => ({ secret: "rlat_tests" }));
    const mine = vi.fn(() => ({ secret: "rlat_mine" }));
    setTransport(mockTransport({ "AccessTokenService.CreateToken": org, "AccessTokenService.CreateMyToken": mine }));

    const res = await createForgeToken({ orgId: ORG, name: "pr-tests", scopes: ["tests:write"], expiresInDays: 90 });

    expect(res).toEqual({ secret: "rlat_tests", kind: "org" });
    expect(org).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, name: "pr-tests", scopes: ["tests:write"] }));
    expect(mine).not.toHaveBeenCalled();
  });

  it("refuses to combine it with another scope, another org-token-only one included", async () => {
    const org = vi.fn(() => ({ secret: "x" }));
    const mine = vi.fn(() => ({ secret: "x" }));
    setTransport(mockTransport({ "AccessTokenService.CreateToken": org, "AccessTokenService.CreateMyToken": mine }));

    for (const scopes of [["tests:write", "tests:read"], ["tests:write", "sourcemaps:write"]]) {
      await expect(createForgeToken({ orgId: ORG, name: "ci", scopes, expiresInDays: null })).rejects.toThrow(/cannot be combined/);
    }
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

  it("offers every scope of forge's catalog except those bound to a resource or issued only to an OAuth client", () => {
    // forge pkg/accesstoken/scope.go AllScopes, as this repo vendors it: the
    // access_tokens CHECK, which internal/db/access_tokens_scopes_forge_test.go
    // and `forge lint --closed-sets` hold equal to AllScopes. A scope forge
    // adds fails this test until it is offered here or listed as not mintable.
    const catalog = forgeScopeCatalog();
    const bindingOnly = ["proxy:port", "mcp:connector", "daemon:resume"];
    // accesstoken.OAuthClientScopes: an authorization server issues these to
    // an MCP client, each as a token's only scope; a person never mints one.
    const oauthClientOnly = ["mcp:oauth", "mcp:forge"];
    const notMintable = [...bindingOnly, ...oauthClientOnly];
    const offered = FORGE_TOKEN_SCOPES.map((s) => s.scope);

    expect(catalog).toEqual(expect.arrayContaining(["vuln:read", "tests:read", "tests:write", "changes:write", "changes:launch"]));
    expect(new Set(offered).size).toBe(offered.length);
    expect([...offered].sort()).toEqual(catalog.filter((s) => !notMintable.includes(s)).sort());
  });

  it("mints tests:write and sourcemaps:write as org tokens only, and never vuln:read or tests:read", () => {
    expect(isOrgTokenOnlyScope("tests:write")).toBe(true);
    expect(isOrgTokenOnlyScope("sourcemaps:write")).toBe(true);
    for (const scope of ["vuln:read", "tests:read", "changes:write", "changes:launch", "deploy:read"]) {
      expect(isOrgTokenOnlyScope(scope), scope).toBe(false);
    }
  });

  it("says vuln:read is not part of deploy:read, and that changes:launch includes changes:write", () => {
    const security = FORGE_TOKEN_SCOPE_GROUPS.find((g) => g.id === "security");
    expect(security?.scopes.map((s) => s.scope)).toEqual(["vuln:read"]);
    expect(security?.description).toMatch(/not included in deploy:read/i);
    const launch = FORGE_TOKEN_SCOPES.find((s) => s.scope === "changes:launch")?.label ?? "";
    expect(launch).toMatch(/includes changes:write/);
  });

  it("describes every scope and titles every group", () => {
    for (const group of FORGE_TOKEN_SCOPE_GROUPS) {
      expect(group.title).not.toBe("");
      for (const { scope, label } of group.scopes) expect(label, scope).not.toBe("");
    }
  });
});
