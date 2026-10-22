// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { gitHubRepoSlug, singleGitHubRepo, withDefaultRepo } from "../oncallSetup";

describe("gitHubRepoSlug", () => {
  it.each([
    ["https://github.com/acme/shop.git", "acme/shop"],
    ["https://github.com/acme/shop", "acme/shop"],
    ["git@github.com:acme/shop.git", "acme/shop"],
    ["ssh://git@github.com/acme/shop.git", "acme/shop"],
    ["https://x-access-token@github.com/Acme/Shop/", "Acme/Shop"],
  ])("%s -> %s", (remote, slug) => {
    expect(gitHubRepoSlug(remote)).toBe(slug);
  });

  it.each(["https://gitlab.com/acme/shop.git", "", "github.com", "https://github.com/acme", "https://github.com/a/b/c"])(
    "refuses %s",
    (remote) => {
      expect(gitHubRepoSlug(remote)).toBeUndefined();
    },
  );
});

describe("singleGitHubRepo", () => {
  it("is the one repository, however many spellings of it there are", () => {
    expect(singleGitHubRepo(["git@github.com:acme/shop.git", "https://github.com/acme/shop", "https://gitlab.com/x/y"])).toBe("acme/shop");
  });
  it("is nothing for none or several", () => {
    expect(singleGitHubRepo([])).toBeUndefined();
    expect(singleGitHubRepo(["https://github.com/acme/a", "https://github.com/acme/b"])).toBeUndefined();
  });
});

describe("withDefaultRepo", () => {
  it("fills an unset or blank default_repo", () => {
    expect(withDefaultRepo({ model: "m" }, "acme/shop")).toEqual({ model: "m", default_repo: "acme/shop" });
    expect(withDefaultRepo({ default_repo: "  " }, "acme/shop")).toEqual({ default_repo: "acme/shop" });
  });
  it("keeps what the user chose, and returns the same object when nothing changes", () => {
    const chosen = { default_repo: "acme/other" };
    expect(withDefaultRepo(chosen, "acme/shop")).toBe(chosen);
    const params = {};
    expect(withDefaultRepo(params, undefined)).toBe(params);
  });
});
