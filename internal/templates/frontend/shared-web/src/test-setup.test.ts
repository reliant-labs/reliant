// yours: scaffolded once, never touched again — forge will not overwrite this file
// Proves the navigation stubs in test-setup.ts: they record, they do not throw,
// and everything else about window.location still behaves like jsdom's.
import { describe, expect, it } from "vitest";

describe("window.location in tests", () => {
  it("records navigation instead of attempting it", () => {
    window.location.assign("https://provider.example/start");
    expect(window.location.assign).toHaveBeenCalledWith("https://provider.example/start");
    window.location.replace("https://idp.example/logout");
    expect(window.location.replace).toHaveBeenCalledWith("https://idp.example/logout");
    window.location.href = "https://elsewhere.example/";
    expect(window.location.assign).toHaveBeenCalledWith("https://elsewhere.example/");
    window.location.reload();
    expect(window.location.reload).toHaveBeenCalledTimes(1);
    expect(window.location.pathname).toBe("/");
  });

  it("starts each test with no recorded calls", () => {
    expect(window.location.assign).not.toHaveBeenCalled();
  });

  it("still reads the real location and follows history and hash changes", () => {
    const origin = window.location.origin;
    window.history.pushState({}, "", "/notes?page=2");
    expect(window.location.pathname).toBe("/notes");
    expect(window.location.search).toBe("?page=2");
    const sameDocument = new URL("#top", window.location.href).href;
    window.location.assign(sameDocument);
    expect(window.location.hash).toBe("#top");
    expect(window.location.toString()).toBe(`${origin}/notes?page=2#top`);
    expect("href" in window.location).toBe(true);
    window.history.pushState({}, "", "/");
  });
});
