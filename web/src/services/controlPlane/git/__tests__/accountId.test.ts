import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import { GetGitCredentialResponseSchema } from "@/gen/controlplane/services/git_credential/v1/git_credential_pb";
import { readAccountId } from "../accountId";

describe("readAccountId", () => {
  it("finds nothing in today's control-plane response, which has no account id", () => {
    // Today: a login and no id. "Me" must not be offered from the login.
    expect(readAccountId(create(GetGitCredentialResponseSchema, { hasToken: true, accountLogin: "OctoCat" }))).toBeUndefined();
  });

  it("reads the id once the response carries one, and only a real GitHub user id", () => {
    expect(readAccountId({ accountLogin: "OctoCat", accountId: "583231" })).toBe("583231");
    for (const bad of ["", "0", "octocat", 583231, null]) {
      expect(readAccountId({ accountId: bad }), String(bad)).toBeUndefined();
    }
  });
});
