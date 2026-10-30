import { create } from "@bufbuild/protobuf";
import { expect, it } from "vitest";
import { PlanSchema, PlanTier } from "@/gen/controlplane/controlplane/v1/shared_pb";
import { deriveMachineOptions } from "../machineOptions";
it("reads native own-size included hours, not deprecated integer Small hours", () => {
  const plan = create(PlanSchema, { id: "plan-small", productId: "prod_compute", tier: PlanTier.PRO, priceCents: 2000n, structuredLimits: { allowedDaemonSizes: ["small"], machineIncludedHours: 160n, machineSize: "small", includedSmallDaemonHours: 999999n } });
  expect(deriveMachineOptions([plan])[0]?.includedHours).toBe("160");
});
