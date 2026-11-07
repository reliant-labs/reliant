// Copyright (c) 2025 Reliant Labs
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { IdentitySelection } from "../IdentitySelection";

const state = vi.hoisted(() => ({ search: {} as Record<string, unknown>, user: {} as Record<string, unknown>, matches: {} as Record<string, unknown>, get: vi.fn(), list: vi.fn() }));
vi.mock("@tanstack/react-router", async (original) => ({ ...(await original<typeof import("@tanstack/react-router")>()), useSearch: () => state.search }));
vi.mock("@/hooks/identity-queries", () => ({ useIdentityUser: (...args: unknown[]) => { state.get(...args); return state.user; }, useIdentityUsers: (...args: unknown[]) => { state.list(...args); return state.matches; } }));
beforeEach(() => { state.search = {}; state.user = {}; state.matches = {}; vi.clearAllMocks(); });
describe("identity URL lookup", () => {
 it("gets an id in exactly the current org and environment", () => {
  state.search = { user: "subject-42" };
  state.user = { data: { userId: "subject-42", email: "ada@example.com" } };
  render(<IdentitySelection orgId="org-a" envId="env-prod" serving />);
  expect(state.get).toHaveBeenCalledWith("org-a", "env-prod", "subject-42", true);
  expect(screen.getByText("ada@example.com")).toBeInTheDocument();
 });
 it("does not display a mismatched returned subject", () => {
  state.search = { user: "expected" };
  state.user = { data: { userId: "other", email: "wrong@example.com" } };
  render(<IdentitySelection orgId="o" envId="e" serving />);
  expect(screen.queryByText("wrong@example.com")).not.toBeInTheDocument();
 });
 it("refuses overlong input before it enables a lookup", () => {
  state.search = { user: "x".repeat(201), q: ["bad"] };
  render(<IdentitySelection orgId="o" envId="e" serving />);
  expect(state.get).toHaveBeenCalledWith("o", "e", null, true);
  expect(state.list).toHaveBeenCalledWith("o", "e", { query: "" }, false);
 });
 it("shows refusal without falling back to email scanning", () => {
  state.search = { user: "id", q: "ada@example.com" };
  state.user = { error: new Error("denied") };
  render(<IdentitySelection orgId="o" envId="e" serving />);
  expect(screen.getByRole("alert")).toHaveTextContent("server refused");
  expect(state.list).toHaveBeenCalledWith("o", "e", { query: "" }, false);
 });
 it("does not mistake one substring match for an exact identity", () => {
  state.search = { q: "ada@example.com" };
  state.matches = { data: { pages: [{ users: [{ email: "otherada@example.com", userId: "other" }] }] }, hasNextPage: false };
  render(<IdentitySelection orgId="o" envId="e" serving />);
  expect(screen.getByText(/No unique exact identity/)).toBeInTheDocument();
  expect(screen.queryByText("other")).not.toBeInTheDocument();
 });
});
