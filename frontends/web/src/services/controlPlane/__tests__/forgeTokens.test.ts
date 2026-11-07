// @vitest-environment node
/**
 * The forge shell's personal-token client (forgeTokens.ts), through the real
 * generated client and request/response types over a mock transport.
 *
 * What is pinned: every rpc carries the active org, scopes and expiry reach
 * the wire as chosen, revoked and ephemeral credentials are never listed, a
 * refused mutation stays an error, and the scope labels disclose what each
 * scope really authorizes.
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
  FORGE_TOKEN_SCOPES,
  listForgeTokens,
  revokeForgeToken,
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

    expect(res).toEqual({ secret: "rlt_secret" });
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
});
