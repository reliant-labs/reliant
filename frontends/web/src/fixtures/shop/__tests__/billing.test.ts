// Copyright (c) 2025 Reliant Labs

import { createClient } from "@connectrpc/connect";
import { afterEach, assert, expect, it } from "vitest";

import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";

import { createFixtureTransport } from "../../transport";
import { shopAppPack } from "../app";
import { SHOP_READ_ONLY_MESSAGE } from "../index";
import { demoNow, resetDemoNowForTests } from "../shopStory";

afterEach(() => resetDemoNowForTests());

it.each([1, 18, 30])("reconciles shop's daily costs and month-to-date summaries on day %i", async (day) => {
  const transport = createFixtureTransport({
    pack: shopAppPack({ now: Date.UTC(2026, 10, day, 15, 30) }),
    now: demoNow,
    readOnly: true,
    readOnlyMessage: SHOP_READ_ONLY_MESSAGE,
  });
  const cost = await createClient(BillingService, transport).getProjectCost({ projectId: "shop" });
  const dailyTotal = cost.daily.reduce((total, entry) => {
    const usdNanos = entry.list?.usdNanos;
    assert.isDefined(usdNanos);
    return total + usdNanos;
  }, 0n);

  expect(cost.daily).not.toHaveLength(0);
  expect(cost.monthToDateList!.usdNanos).toBe(dailyTotal);
  // No allowances or caps alter the list price in this fixture.
  expect(cost.allowances).toEqual([]);
  expect(cost.caps).toEqual([]);
  expect(cost.monthToDate!.usdNanos).toBe(dailyTotal);
});
