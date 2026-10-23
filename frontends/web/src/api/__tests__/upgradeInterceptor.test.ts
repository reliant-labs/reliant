/**
 * The backend-signalled modals open on a REASON (lib/errorReasons.ts), read
 * from x-forge-error-reason or, until control-plane switches, x-reliant-reason
 * — and never on "any reason at all".
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";

const openModal = vi.fn();
vi.mock("../../store/modalStore", () => ({
  useModalStore: {
    getState: () => ({ openModal }),
    subscribe: () => () => {},
  },
}));

import { __resetUpgradeModalGuardForTests, upgradeInterceptor } from "../upgradeInterceptor";

async function fail(error: unknown): Promise<unknown> {
  const call = upgradeInterceptor(async () => {
    throw error;
  });
  try {
    await call({} as never);
  } catch (thrown) {
    return thrown;
  }
  throw new Error("interceptor swallowed the error");
}

function withReason(header: string, reason: string, code: Code): ConnectError {
  return new ConnectError("refused", code, new Headers({ [header]: reason }));
}

describe("upgradeInterceptor", () => {
  beforeEach(() => {
    openModal.mockClear();
    __resetUpgradeModalGuardForTests();
  });

  it.each(["x-forge-error-reason", "x-reliant-reason"])(
    "opens the upgrade modal for an entitlement reason under %s, with no upgrade URL",
    async (header) => {
      const err = withReason(header, "compute_budget_exhausted", Code.ResourceExhausted);
      expect(await fail(err)).toBe(err);
      await vi.waitFor(() =>
        expect(openModal).toHaveBeenCalledWith("upgrade-required", {
          reason: "compute_budget_exhausted",
          message: "refused",
        }),
      );
    },
  );

  it.each(["x-forge-error-reason", "x-reliant-reason"])(
    "opens the billing-email modal for billing_email_missing under %s",
    async (header) => {
      await fail(withReason(header, "billing_email_missing", Code.InvalidArgument));
      await vi.waitFor(() =>
        expect(openModal).toHaveBeenCalledWith("billing-email-required", { message: "refused" }),
      );
    },
  );

  it("leaves a ResourceExhausted with a non-entitlement reason (a coupon) alone", async () => {
    await fail(withReason("x-forge-error-reason", "coupon_exhausted", Code.ResourceExhausted));
    await fail(new ConnectError("rate limited", Code.ResourceExhausted));
    await new Promise((r) => setTimeout(r, 0));
    expect(openModal).not.toHaveBeenCalled();
  });
});
