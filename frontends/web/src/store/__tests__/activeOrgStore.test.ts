import { beforeEach, describe, expect, it } from "vitest";

import { resolveActiveOrg, type MyOrgs } from "@/services/controlPlane/orgs";
import {
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
  sessionStorage.clear();
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
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

});

describe("the choice is per tab", () => {
  /** A fresh page in a new tab: its own sessionStorage, the browser's localStorage. */
  function openNewTab() {
    sessionStorage.clear();
    useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
  }

  it("a new tab starts in the org last chosen in any tab", () => {
    bindActiveOrgUser("alice");
    chooseActiveOrg("org-b");
    openNewTab();
    bindActiveOrgUser("alice");
    expect(getActiveOrgId()).toBe("org-b");
  });

  it("a choice in another tab does not move this one, even on reload", () => {
    bindActiveOrgUser("alice");
    chooseActiveOrg("org-a");
    const thisTab = sessionStorage.getItem("reliant.activeOrg.alice");
    // Another tab picks org-b: it writes the shared last choice and its own session.
    openNewTab();
    bindActiveOrgUser("alice");
    chooseActiveOrg("org-b");
    // Back in the first tab, which still has its own session, and reloads.
    sessionStorage.setItem("reliant.activeOrg.alice", thisTab!);
    useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
    bindActiveOrgUser("alice");
    expect(getActiveOrgId()).toBe("org-a");
    // And a tab opened now starts in the latest choice.
    openNewTab();
    bindActiveOrgUser("alice");
    expect(getActiveOrgId()).toBe("org-b");
  });

  it("a tab that has its org keeps it when the shared choice changes later", () => {
    bindActiveOrgUser("alice");
    chooseActiveOrg("org-a");
    localStorage.setItem("reliant.activeOrg.alice", "org-b");
    useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
    bindActiveOrgUser("alice");
    expect(getActiveOrgId()).toBe("org-a");
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
    expect(sessionStorage.getItem("reliant.activeOrg.alice")).toBeNull();
    expect(localStorage.getItem("reliant.activeOrg.alice")).toBeNull();
  });

  it("does not forget another tab's newer choice for new tabs", () => {
    bindActiveOrgUser("alice");
    chooseActiveOrg("org-gone");
    localStorage.setItem("reliant.activeOrg.alice", "org-b"); // another tab chose since
    setEffectiveOrg("org-a");
    forgetChosenOrg();
    expect(localStorage.getItem("reliant.activeOrg.alice")).toBe("org-b");
  });

  it("is resolved once the membership list answers, even with no org", () => {
    bindActiveOrgUser("alice");
    expect(useActiveOrgStore.getState().resolved).toBe(false);
    setEffectiveOrg(null);
    expect(useActiveOrgStore.getState().resolved).toBe(true);
    // Another person signs in: theirs is not known yet.
    bindActiveOrgUser("bob");
    expect(useActiveOrgStore.getState().resolved).toBe(false);
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
