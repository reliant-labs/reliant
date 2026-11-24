/**
 * The arrival notice (lib/orgArrivalNotice.ts) as the person sees it: one
 * toast per arrival, shown once the root has mounted, never twice for one
 * notice, and a later notice replacing the last in place.
 */
import { act, render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const info = vi.fn();
vi.mock("@/lib/toast-manager", () => ({ toast: { info: (...args: unknown[]) => info(...args) } }));

import { announceOrgArrival, rememberOrgsForNotice, resetOrgArrivalNoticeForTests, useOrgArrivalNotice } from "@/lib/orgArrivalNotice";

import { OrgArrivalNotice } from "../OrgArrivalNotice";

beforeEach(() => {
  info.mockReset();
  resetOrgArrivalNoticeForTests();
  rememberOrgsForNotice(
    [
      { id: "org-a", slug: "acme", name: "Acme" },
      { id: "org-b", slug: "side", name: "Side" },
    ],
    "org-a",
  );
});

describe("OrgArrivalNotice", () => {
  it("shows a notice queued before it mounted (a page load's first commit), once", () => {
    announceOrgArrival("org-b", "org-a", "/o/side/inbox");
    const { rerender } = render(<OrgArrivalNotice />);
    rerender(<OrgArrivalNotice />);
    expect(info).toHaveBeenCalledTimes(1);
    expect(info).toHaveBeenCalledWith("Switched to Side to open this link.", { id: "org-arrival" });
    expect(useOrgArrivalNotice.getState().notice).toBeNull();
  });

  it("shows each later arrival under the same toast id, so one replaces the last", () => {
    render(<OrgArrivalNotice />);
    act(() => announceOrgArrival("org-b", "org-a", "/o/side/inbox"));
    act(() => announceOrgArrival("org-a", "org-b", "/o/acme/inbox"));
    expect(info.mock.calls).toEqual([
      ["Switched to Side to open this link.", { id: "org-arrival" }],
      ["Switched to Acme to open this link.", { id: "org-arrival" }],
    ]);
  });

  it("shows nothing for a commit that did not move the tab's org", () => {
    render(<OrgArrivalNotice />);
    act(() => announceOrgArrival("org-a", "org-a", "/o/acme/inbox"));
    expect(info).not.toHaveBeenCalled();
  });
});
