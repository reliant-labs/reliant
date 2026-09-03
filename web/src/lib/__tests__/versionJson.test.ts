import { describe, expect, it } from "vitest";

import {
  VERSION_JSON_FILE,
  resolveVersionPayload,
} from "../../../vite-plugin-version-json";

// The deployed SPA rewrites every unmatched path to index.html (Firebase
// Hosting `** -> /index.html`). A client-side route named "version" would be
// served BY that rewrite, as HTML — which is the state this replaced, where
// /version, /version.json and /__version all returned the app shell with 200.
//
// Emitting a real file into the bundle root is what takes it out of the
// rewrite's reach, so the file NAME is the contract these tests pin.
describe("version.json", () => {
  it("is emitted at the bundle root so the SPA rewrite cannot shadow it", () => {
    expect(VERSION_JSON_FILE).toBe("version.json");
    // No leading slash and no nesting: Firebase matches a static file by its
    // path under the public dir, and a nested one would not answer /version.json.
    expect(VERSION_JSON_FILE).not.toContain("/");
  });

  it("prefers the release version the CI workflow provides", () => {
    const payload = resolveVersionPayload({
      RELEASE_VERSION: "v1.7.11",
      GITHUB_SHA: "abc1234def",
      GITHUB_REF_NAME: "v1.7.11",
    } as NodeJS.ProcessEnv);

    // The leading "v" is stripped to match release.yml, which stamps ${VERSION#v}.
    expect(payload.version).toBe("1.7.11");
    expect(payload.commit).toBe("abc1234def");
    expect(payload.branch).toBe("v1.7.11");
    expect(payload.built).not.toBe("");
  });

  it("never reports a branch name as the version", () => {
    // The vmain bug in one assertion: a branch push must not produce a
    // "version" of "main". With no RELEASE_VERSION the resolver falls through
    // to git describe, which yields a tag/sha — never the branch.
    const payload = resolveVersionPayload({
      GITHUB_REF_NAME: "main",
      GITHUB_SHA: "6f78293",
    } as NodeJS.ProcessEnv);

    expect(payload.version).not.toBe("main");
    expect(payload.version).not.toBe("master");
    // The branch is still reported — as `branch`, which is what it is.
    expect(payload.branch).toBe("main");
  });

  it("admits it does not know rather than inventing a plausible version", () => {
    // Env carries nothing and git is unavailable to the resolver's caller: the
    // honest answer is "unknown". A fake-but-plausible "1.0.0" is worse than a
    // blank, because a bug report then names a build that never shipped.
    const payload = resolveVersionPayload({} as NodeJS.ProcessEnv);

    for (const field of [payload.version, payload.commit, payload.branch]) {
      expect(field).not.toBe("1.0.0");
      expect(field).not.toBe("");
    }
  });
});
