// Copyright (c) 2025 Reliant Labs
import { beforeEach, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { ForgeConsoleContext, type ForgeConsole } from "../../Console/consoleContext";
import { IdentityRowLink } from "../IdentityRowLink";
const s = vi.hoisted(() => ({ data: { hasInstance: true }, error: null as unknown, status: vi.fn() }));
vi.mock("@/hooks/identity-queries", () => ({ useIdentityOrg: () => ({ orgId: "org" }), useIdentityStatus: (...args: unknown[]) => { s.status(...args); return s; } }));
beforeEach(() => { s.data = { hasInstance: true }; s.error = null; vi.clearAllMocks(); });
const href = vi.fn((_page: unknown, search: unknown) => `/identity?${new URLSearchParams(search as Record<string, string>)}`);
function show() {
 return render(<ForgeConsoleContext.Provider value={{ href } as unknown as ForgeConsole}><IdentityRowLink env={{ id: "prod-id", name: "prod" }} target={{ user: "a&b/'\"" }} /></ForgeConsoleContext.Provider>);
}
it("uses actual scoped environment and exact subject, not an owner or a remembered environment", () => {
 show();
 expect(s.status).toHaveBeenCalledWith("org", "prod-id");
 expect(href).toHaveBeenCalledWith({ kind: "environment", env: "prod" }, { tab: "identity", user: "a&b/'\"" });
 expect(screen.getByRole("link", { name: "Open in Identity" })).toBeInTheDocument();
});
it("never emits a link when identity is not declared", () => {
 s.data = { hasInstance: false }; show();
 expect(screen.queryByRole("link")).not.toBeInTheDocument();
});
it("never emits a link after an IAM refusal even if cached status exists", () => {
 s.error = new Error("denied"); show();
 expect(screen.queryByRole("link")).not.toBeInTheDocument();
});
