import { beforeEach, describe, expect, it, vi } from "vitest";
import { TokenKind } from "@/gen/reliant/v1/token_pb";

// The environments data layer's daemon-token functions are thin wrappers over
// reliant.v1.TokenService. These pin the translation each one does, since the
// unified service's shapes differ from the retired DaemonTokenService's
// (`info.id` instead of a top-level `tokenId`, `id` instead of `token_id`).

const listTokens = vi.fn();
const createToken = vi.fn();
const revokeToken = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    token: () => ({ listTokens, createToken, revokeToken }),
  },
}));
vi.mock("../client", () => ({ getControlPlaneClient: vi.fn() }));

import { createDaemonToken, listDaemonTokens, revokeDaemonToken } from "../environments";

describe("environments daemon tokens", () => {
  beforeEach(() => {
    listTokens.mockReset();
    createToken.mockReset();
    revokeToken.mockReset();
  });

  it("listDaemonTokens asks for DAEMON tokens and returns them as-is", async () => {
    // Mutation caught: omitting the kind filter.
    const tokens = [{ id: "t1", name: "n", tokenPrefix: "rlat_abc" }];
    listTokens.mockResolvedValue({ tokens });
    await expect(listDaemonTokens()).resolves.toBe(tokens);
    expect(listTokens).toHaveBeenCalledWith({ kind: TokenKind.DAEMON });
  });

  it("createDaemonToken mints kind DAEMON and reads the id from info", async () => {
    // Mutation caught: reading a top-level tokenId (always "" on the new
    // response) or dropping the kind.
    createToken.mockResolvedValue({ token: "rlat_raw", info: { id: "tok-9" } });
    await expect(createDaemonToken("laptop")).resolves.toEqual({ token: "rlat_raw", tokenId: "tok-9" });
    expect(createToken).toHaveBeenCalledWith({ name: "laptop", kind: TokenKind.DAEMON });
  });

  it("revokeDaemonToken sends the id field", async () => {
    // Mutation caught: sending the retired token_id / tokenId field.
    revokeToken.mockResolvedValue({});
    await revokeDaemonToken("tok-9");
    expect(revokeToken).toHaveBeenCalledWith({ id: "tok-9" });
  });
});
