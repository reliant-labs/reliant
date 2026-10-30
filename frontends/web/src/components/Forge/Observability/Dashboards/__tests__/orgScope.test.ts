// Copyright (c) 2025 Reliant Labs
import { describe, expect, it } from "vitest";

import { ENV_ALL } from "../../../Console/envScope";
import { orgPlaceOf } from "../../obsPlace";
import { orgQueryScope, orgScopeLabel, orgSelection } from "../orgScope";

const { environments } = orgPlaceOf([
  { id: "e-cp", name: "prod", project: "control-plane", kind: "persistent" },
  { id: "e-web", name: "prod-web", project: "control-plane", kind: "persistent" },
  { id: "e-land", name: "prod", project: "landing", kind: "persistent" },
  { id: "e-land-stg", name: "staging", project: "landing", kind: "persistent" },
] satisfies Parameters<typeof orgPlaceOf>[0]);

describe("an org dashboard's scope", () => {
  it("names environments project/env, so the same env name in two projects is told apart", () => {
    expect(environments.map((e) => e.name)).toEqual(["control-plane/prod", "control-plane/prod-web", "landing/prod", "landing/staging"]);
  });

  it("with nothing picked reads every environment of the org, and names no project", () => {
    const pick = orgSelection(environments, undefined);
    expect(pick).toEqual({ names: [], ids: [], env: ENV_ALL });
    const scope = orgQueryScope("org-1", pick);
    expect(scope).toEqual({ orgId: "org-1", envIds: [], filters: [] });
    expect("projectId" in scope).toBe(false);
    expect(orgScopeLabel(pick)).toBe("all projects · all environments");
  });

  it("reads exactly the picked environments, across projects", () => {
    const pick = orgSelection(environments, ["landing/prod", "control-plane/prod-web"]);
    expect(pick.ids).toEqual(["e-web", "e-land"]);
    expect(orgQueryScope("org-1", pick).envIds).toEqual(["e-web", "e-land"]);
    expect(pick.env).toBe(ENV_ALL);
    expect(orgScopeLabel(pick)).toBe("control-plane/prod-web, landing/prod");
  });

  it("one pick is that environment (compare and alerts key on it)", () => {
    const pick = orgSelection(environments, ["landing/staging"]);
    expect(pick).toMatchObject({ ids: ["e-land-stg"], env: "landing/staging" });
  });

  it("drops a pick the org no longer has instead of scoping the page to nothing", () => {
    const pick = orgSelection(environments, ["landing/torn-down-preview"]);
    expect(pick.ids).toEqual([]);
    expect(orgScopeLabel(orgSelection(environments, environments.map((e) => e.name)))).toBe("4 environments");
  });
});
