// Copyright (c) 2026 Reliant Labs

/**
 * Which checkout backs an app's console (consoleHost.ts consoleCheckout): the
 * one the URL's `?project=` names, else the only project declaring the app;
 * several and none named is a question for the person, never the first row.
 */

import { describe, expect, it } from "vitest";

import type { Project } from "@/store/projectStore";

import { consoleCheckout } from "../consoleHost";

function project(id: string, forgeName?: string): Project {
  return {
    id,
    name: id,
    path: `/src/${id}`,
    is_git_repo: true,
    is_forge: forgeName !== undefined,
    forge_project_name: forgeName,
    last_active: "",
    created_at: "",
    updated_at: "",
  } as Project;
}

const SHOP_A = project("shop-a", "shop");
const SHOP_B = project("shop-b", "shop");
const BLOG = project("blog", "blog");
const PLAIN = project("plain");

describe("consoleCheckout", () => {
  it("is the only project declaring the app", () => {
    expect(consoleCheckout("blog", [SHOP_A, BLOG, PLAIN])).toEqual({ kind: "one", project: BLOG });
  });

  it("asks when several declare it and the URL names none, rather than taking the first", () => {
    expect(consoleCheckout("shop", [SHOP_A, BLOG, SHOP_B])).toEqual({ kind: "several", candidates: [SHOP_A, SHOP_B] });
  });

  it("is the checkout `?project=` names, among several", () => {
    expect(consoleCheckout("shop", [SHOP_A, SHOP_B], "shop-b")).toEqual({ kind: "one", project: SHOP_B });
  });

  it("ignores a `?project=` that names no checkout of this app (stale, or another app's)", () => {
    expect(consoleCheckout("shop", [SHOP_A, SHOP_B, BLOG], "blog")).toEqual({ kind: "several", candidates: [SHOP_A, SHOP_B] });
    expect(consoleCheckout("blog", [SHOP_A, BLOG], "gone")).toEqual({ kind: "one", project: BLOG });
  });

  it("is none when no project declares it (the console still reads the control plane by name)", () => {
    expect(consoleCheckout("elsewhere", [SHOP_A, PLAIN], "shop-a")).toEqual({ kind: "none" });
  });

  it("matches the declared name trimmed", () => {
    const padded = project("padded", " shop ");
    expect(consoleCheckout("shop", [padded])).toEqual({ kind: "one", project: padded });
  });
});
