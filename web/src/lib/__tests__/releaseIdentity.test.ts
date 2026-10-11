import { describe, expect, it } from "vitest";

import { formatVersion, shortSha } from "../releaseIdentity";

// What the About page prints for a version stamp. The same function renders
// THIS bundle's baked identity and the server's Version RPC, so it has to read
// every stamp a deployed build can carry — including the old ones.
describe("formatVersion", () => {
  it("shows a release built at a tag as vX.Y.Z", () => {
    expect(formatVersion("1.8.1")).toBe("v1.8.1"); // release.yml / the resolver
    expect(formatVersion("v1.8.1")).toBe("v1.8.1"); // a raw tag
    expect(formatVersion("1.8.2-rc1")).toBe("v1.8.2-rc1");
  });

  it("shows a build at a commit as the next dev version plus the commit", () => {
    expect(formatVersion("1.8.2-dev+8bcfc6ee")).toBe("v1.8.2-dev+8bcfc6ee");
    expect(formatVersion("1.8.2-dev+8bcfc6ee.dirty")).toBe("v1.8.2-dev+8bcfc6ee.dirty");
    // A `git describe` stamp (control-plane's image builds before the
    // resolver) is still a real answer, shown as-is.
    expect(formatVersion("v1.7.16-392-g10c676b6")).toBe("v1.7.16-392-g10c676b6");
  });

  it("shows a bare commit as its short sha, never with a v", () => {
    expect(formatVersion("10c676b6f2fa66a4b13e68f2f995289a7245b8f0")).toBe("10c676b6");
    expect(formatVersion("8BCFC6EE")).toBe("8bcfc6ee");
    expect(formatVersion("10c676b")).toBe("10c676b");
  });

  it("falls back to the commit when the version names nothing", () => {
    // An unstamped Go binary built in a checkout still records vcs.revision.
    expect(formatVersion("unknown", "10c676b6f2fa66a4b13e68f2f995289a7245b8f0")).toBe("10c676b6");
    expect(formatVersion("", "abcdef1234567")).toBe("abcdef12");
  });

  it("returns null when nothing names the build — never a plausible guess", () => {
    // Prod's Version RPC answered exactly this; the page must not invent
    // "v1.0.0" (MobileAboutScreen once did) or render "vunknown".
    expect(formatVersion("unknown", "unknown")).toBeNull();
    expect(formatVersion("")).toBeNull();
    expect(formatVersion(undefined)).toBeNull();
    expect(formatVersion(null, null)).toBeNull();
    expect(formatVersion("(devel)")).toBeNull();
    expect(formatVersion("dev")).toBeNull(); // the Dockerfile's default stamp
    expect(formatVersion("unknown", "not-a-sha")).toBeNull();
  });
});

describe("shortSha", () => {
  it("abbreviates to 8 lowercase characters", () => {
    expect(shortSha("10C676B6F2FA66A4")).toBe("10c676b6");
  });
});
