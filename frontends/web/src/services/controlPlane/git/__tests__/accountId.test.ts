import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import { GetGitCredentialResponseSchema } from "@/gen/controlplane/services/git_credential/v1/git_credential_pb";
import { readAccountId } from "../accountId";

describe("readAccountId", () => {
  it("reads the GitHub user id control-plane reports for the account", () => {
    const res = create(GetGitCredentialResponseSchema, { hasToken: true, accountLogin: "OctoCat", accountId: "583231" });
    expect(readAccountId(res)).toBe("583231");
  });

  it("finds nothing when control-plane could not resolve the account", () => {
    // A login and no id: "Me" must not be offered from the login.
    expect(readAccountId(create(GetGitCredentialResponseSchema, { hasToken: true, accountLogin: "OctoCat" }))).toBeUndefined();
  });

  it("accepts only a real GitHub user id", () => {
    for (const bad of ["", "0", "octocat", "-1", "1.5", " 583231", "0583231"]) {
      expect(readAccountId({ accountId: bad }), bad).toBeUndefined();
    }
  });
});
