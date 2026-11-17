// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { DeclaredWorkload } from "@/services/controlPlane/observability/types";

import { sourceRepoOf } from "../askReliantSource";

const declared = (sourceRepo: string) => ({ declared: { sourceRepo } as DeclaredWorkload });

describe("sourceRepoOf", () => {
  it("reads the GitHub repository a workload's declaration was rendered from", () => {
    expect(sourceRepoOf([declared("https://github.com/acme/shop.git")])).toMatchObject({
      fullName: "acme/shop",
      cloneUrl: "https://github.com/acme/shop.git",
    });
  });

  it("accepts an ssh remote", () => {
    expect(sourceRepoOf([declared("git@github.com:acme/shop.git")])?.fullName).toBe("acme/shop");
  });

  it("skips workloads with no declaration or no recorded remote", () => {
    expect(sourceRepoOf([{ declared: null }, declared(""), declared("https://github.com/acme/shop")])?.fullName).toBe("acme/shop");
  });

  it("answers when every workload with a GitHub source names the same repository, in any form", () => {
    expect(
      sourceRepoOf([declared("https://github.com/acme/shop.git"), declared("git@github.com:acme/shop.git"), declared("https://github.com/Acme/Shop")])?.fullName
    ).toBe("acme/shop");
  });

  it("is null when workloads were deployed from different repositories: neither is the app's code", () => {
    expect(sourceRepoOf([declared("https://github.com/acme/shop.git"), declared("https://github.com/acme/billing.git")])).toBeNull();
    expect(sourceRepoOf([{ declared: null }, declared("https://github.com/acme/billing"), declared("https://github.com/acme/shop")])).toBeNull();
  });

  it("is null with no recorded GitHub source", () => {
    expect(sourceRepoOf([])).toBeNull();
    expect(sourceRepoOf([{ declared: null }, declared("")])).toBeNull();
  });

  it("is null for a remote off GitHub, which the clone forms cannot take", () => {
    expect(sourceRepoOf([declared("https://gitlab.com/acme/shop.git")])).toBeNull();
  });

  it("is null when one workload came from GitHub and another from elsewhere: still two repositories", () => {
    expect(sourceRepoOf([declared("https://github.com/acme/shop.git"), declared("https://gitlab.com/acme/billing.git")])).toBeNull();
  });
});
