// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Pins the open-redirect guard on the sign-in screen's returnTo.

import { describe, expect, it } from "vitest";

import { safeReturnTo } from "./return-to";

describe("safeReturnTo", () => {
  it("keeps a page of this app, with its query and fragment", () => {
    expect(safeReturnTo("/projects/7?tab=a#top")).toBe("/projects/7?tab=a#top");
    expect(safeReturnTo(`${window.location.origin}/billing`)).toBe("/billing");
  });

  it("sends everything else home", () => {
    for (const hostile of [
      "//evil.com",
      "/\\evil.com",
      "\\\\evil.com",
      "/\t/evil.com",
      "/\n/evil.com",
      " //evil.com",
      "https://evil.com/",
      "javascript:alert(1)",
      "data:text/html,hi",
      // Dot segments and same-origin absolute URLs that resolve to a "//" path.
      "/.//evil.com",
      "/./\\evil.com",
      "/%2e//evil.com",
      "/a/..//evil.com",
      `${window.location.origin}//evil.com`,
    ]) {
      expect(safeReturnTo(hostile), JSON.stringify(hostile)).toBe("/");
    }
  });

  it("decodes nothing the URL parser would not: %5C and %09 from a link arrive decoded", () => {
    // searchParams.get() decodes the link's %5C to a backslash and %09 to a tab.
    const params = new URLSearchParams("a=/%5Cevil.com&b=/%09/evil.com");
    expect(safeReturnTo(params.get("a"))).toBe("/");
    expect(safeReturnTo(params.get("b"))).toBe("/");
  });

  it("is home for nothing at all", () => {
    expect(safeReturnTo(null)).toBe("/");
    expect(safeReturnTo("")).toBe("/");
  });
});
