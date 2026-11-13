/**
 * The preview's sentences for control-plane's half of a move: said only when
 * control-plane answered, and a hosted deployment named only when the source
 * org actually hosts one.
 */
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { ProjectOrgMoveEffectsSchema, ProjectOrgMoveMachineSchema } from "@/gen/reliant/v1/project_pb";

import { effectLines } from "../projectOrgMove";

const base = { chats: 2, runs: 5, forgeProjectName: "shop" };

describe("effectLines — control-plane's facts", () => {
  it("says what happens to a hosted deployment it could not check", () => {
    const lines = effectLines(create(ProjectOrgMoveEffectsSchema, base), "Acme", "Globex");
    expect(lines).toContain(
      "It deploys as shop: a hosted deployment of that name stays in Acme, and the next deploy goes to Globex.",
    );
    expect(lines.join(" ")).not.toMatch(/secret-access|no plan or balance/);
  });

  it("names the hosted deployment that stays, the secret access revoked and an unfunded target", () => {
    const e = create(ProjectOrgMoveEffectsSchema, {
      ...base,
      controlPlaneEffectsKnown: true,
      secretAccessToRevoke: 3,
      hostedProjectsStaying: ["shop"],
      targetFunded: false,
      machines: [
        create(ProjectOrgMoveMachineSchema, { daemonId: "d-1", inTargetOrg: true }),
        create(ProjectOrgMoveMachineSchema, { daemonId: "d-2", inTargetOrg: false }),
      ],
    });
    const lines = effectLines(e, "Acme", "Globex");
    expect(lines).toContain("It deploys as shop: its hosted deployment stays in Acme, and the next deploy goes to Globex.");
    expect(lines).toContain("3 secret-access entries of Acme for this project are revoked.");
    expect(lines).toContain("Globex has no plan or balance yet, so the project's next run there won't start until it does.");
    expect(lines.join(" ")).toMatch(/1 machine outside Globex keeps billing/);
  });

  it("says the source hosts nothing of that name when control-plane found none", () => {
    const e = create(ProjectOrgMoveEffectsSchema, { ...base, controlPlaneEffectsKnown: true, secretAccessToRevoke: 0 });
    expect(effectLines(e, "Acme", "Globex")).toContain(
      "It deploys as shop, which Acme does not host; the next deploy goes to Globex.",
    );
  });
});
