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

  it("skips workloads with no declaration or no recorded remote, and takes the first that has one", () => {
    expect(sourceRepoOf([{ declared: null }, declared(""), declared("https://github.com/acme/shop")])?.fullName).toBe("acme/shop");
  });

  it("is null for a remote off GitHub, which the clone forms cannot take", () => {
    expect(sourceRepoOf([declared("https://gitlab.com/acme/shop.git")])).toBeNull();
    expect(sourceRepoOf([])).toBeNull();
  });
});
