import { beforeEach, describe, expect, it } from "vitest";

import { resolveActiveOrg, type MyOrgs } from "@/services/controlPlane/orgs";
import {
  adoptPersistedActiveOrg,
  bindActiveOrgUser,
  chooseActiveOrg,
  forgetChosenOrg,
  getActiveOrgId,
  setEffectiveOrg,
  useActiveOrgStore,
} from "../activeOrgStore";

const org = (id: string, isDefault = false) => ({ id, name: id, slug: id, role: "member", isOwner: false, isDefault });

beforeEach(() => {
  localStorage.clear();
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null });
});

describe("the choice is per user", () => {
  it("is remembered for the person who made it and not for anyone else", () => {
    bindActiveOrgUser("alice");
    chooseActiveOrg("org-b");
    expect(getActiveOrgId()).toBe("org-b");

    bindActiveOrgUser("bob");
    expect(getActiveOrgId()).toBeNull();

    bindActiveOrgUser("alice");
    expect(getActiveOrgId()).toBe("org-b");
  });

  it("is dropped on sign-out, so the next person starts clean", () => {
    bindActiveOrgUser("alice");
    chooseActiveOrg("org-b");
    bindActiveOrgUser(null);
    expect(getActiveOrgId()).toBeNull();
    chooseActiveOrg("org-c"); // no one is signed in: nothing to remember it for
    expect(localStorage.getItem("reliant.activeOrg.null")).toBeNull();
  });

  it("is adopted from another tab", () => {
    bindActiveOrgUser("alice");
    chooseActiveOrg("org-a");
    localStorage.setItem("reliant.activeOrg.alice", "org-b");
    adoptPersistedActiveOrg();
    expect(getActiveOrgId()).toBe("org-b");
  });
});

describe("reconciliation", () => {
  it("forgets a remembered org the person has left, and keeps the server default's effect", () => {
    bindActiveOrgUser("alice");
    chooseActiveOrg("org-b");
    setEffectiveOrg("org-a"); // the live list no longer contains org-b
    forgetChosenOrg();
    expect(useActiveOrgStore.getState().chosenId).toBeNull();
    expect(getActiveOrgId()).toBe("org-a");
    expect(localStorage.getItem("reliant.activeOrg.alice")).toBeNull();
  });
});

describe("resolveActiveOrg", () => {
  const mine = (defaultOrgId: string, ...orgs: ReturnType<typeof org>[]): MyOrgs => ({ orgs, defaultOrgId });

  it("honours the choice while the person is still in that org", () => {
    expect(resolveActiveOrg(mine("a", org("a", true), org("b")), "b")).toBe("b");
  });

  it("falls back to the server's default when the chosen org is gone", () => {
    expect(resolveActiveOrg(mine("a", org("a", true), org("b")), "gone")).toBe("a");
  });

  it("uses the server's default when nothing was chosen", () => {
    expect(resolveActiveOrg(mine("b", org("a"), org("b", true)), null)).toBe("b");
  });

  it("never picks the first org in the list as a guess", () => {
    // No choice and the server names no default: there is no answer, not "a".
    expect(resolveActiveOrg(mine("", org("a"), org("b")), null)).toBeNull();
  });

  it("is null for a person with no orgs yet", () => {
    expect(resolveActiveOrg(mine(""), "x")).toBeNull();
  });
});
