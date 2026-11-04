// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { CloudEnv } from "@/services/forge/cloudEnvs";

import { appLabel, envIdsOf, orgEnvName, orgPickerEnvironments, orgPlaceOf, orgSelectionIsEmpty, type ObsPlace } from "../obsPlace";

const env = (id: string, project: string, name: string): CloudEnv => ({ id, project, name, kind: "persistent" });

const PLACE = orgPlaceOf([env("s-prod", "shop", "prod"), env("a-prod", "api", "prod"), env("a-dev", "api", "dev")]);
// "web" owns no environment: an app the URL can still name.
const ORG: ObsPlace = { kind: "org", environments: PLACE.environments, apps: [...PLACE.apps, "web"] };

describe("org naming", () => {
  it("names an env app/env, and a project-less one by its bare name", () => {
    expect(orgEnvName("shop", "prod")).toBe("shop/prod");
    expect(orgEnvName("", "scratch")).toBe("scratch");
  });

  it("labels a dashboard that belongs to no app", () => {
    expect(appLabel("")).toBe("Unassigned");
    expect(appLabel("shop")).toBe("shop");
  });

  it("builds the org place from the org's environments: app/env names and the owning apps, sorted", () => {
    expect(PLACE.environments.map((e) => [e.id, e.name, e.project])).toEqual([
      ["a-dev", "api/dev", "api"],
      ["a-prod", "api/prod", "api"],
      ["s-prod", "shop/prod", "shop"],
    ]);
    expect(PLACE.environments.every((e) => e.declared === null)).toBe(true);
    expect(PLACE.apps).toEqual(["api", "shop"]);
  });

  it("is empty for an org with no environments", () => {
    expect(orgPlaceOf([])).toEqual({ environments: [], apps: [] });
  });
});

describe("envIdsOf on an org place", () => {
  it("is empty (the whole org) with nothing picked and no apps", () => {
    expect(envIdsOf(ORG, undefined)).toEqual([]);
    expect(envIdsOf(ORG, [], [])).toEqual([]);
  });

  it("resolves picked env names to ids, even when two apps share an env name", () => {
    expect(envIdsOf(ORG, ["shop/prod", "api/dev"])).toEqual(["a-dev", "s-prod"]);
  });

  it("is every env of the selected apps when no env is picked", () => {
    expect(envIdsOf(ORG, undefined, ["api"])).toEqual(["a-dev", "a-prod"]);
    expect(envIdsOf(ORG, [], ["shop", "api"])).toEqual(["a-dev", "a-prod", "s-prod"]);
  });

  it("narrows picked envs to the selected apps, falling back to the apps' envs when none survive", () => {
    expect(envIdsOf(ORG, ["shop/prod", "api/dev"], ["api"])).toEqual(["a-dev"]);
    expect(envIdsOf(ORG, ["shop/prod"], ["api"])).toEqual(["a-dev", "a-prod"]);
  });

  it("ignores unknown picked names", () => {
    expect(envIdsOf(ORG, ["nope/prod"])).toEqual([]);
  });

  it("is empty for apps that own no environment, which orgSelectionIsEmpty flags", () => {
    expect(envIdsOf(ORG, undefined, ["web"])).toEqual([]);
    expect(orgSelectionIsEmpty(ORG, undefined, ["web"])).toBe(true);
    expect(orgSelectionIsEmpty(ORG, undefined, ["web", "api"])).toBe(false);
    expect(orgSelectionIsEmpty(ORG, undefined, undefined)).toBe(false);
    expect(orgSelectionIsEmpty(ORG, undefined, [])).toBe(false);
  });

  it("leaves the env and project places alone", () => {
    expect(envIdsOf({ kind: "env", env: { id: "e1", name: "prod" } }, ["x"], ["y"])).toEqual(["e1"]);
    const project: ObsPlace = { kind: "project", project: "shop", environments: [{ id: "p1", name: "prod" }] };
    expect(envIdsOf(project, ["prod"], ["ignored"])).toEqual(["p1"]);
    expect(orgSelectionIsEmpty(project, undefined, ["x"])).toBe(false);
  });
});

describe("orgPickerEnvironments", () => {
  it("offers every env, or those of the selected apps", () => {
    expect(orgPickerEnvironments(ORG, undefined)).toHaveLength(3);
    expect(orgPickerEnvironments(ORG, ["shop"]).map((e) => e.name)).toEqual(["shop/prod"]);
    expect(orgPickerEnvironments(ORG, ["web"])).toEqual([]);
  });
});
