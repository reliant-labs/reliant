import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PullRequestAction } from "./CommitComposer";
import type { PullRequestLookup } from "@/hooks/worktree-git-queries";

const base: PullRequestLookup & { onCreate: () => void; disabled: boolean } = {
  status: "loading", existing: null, error: null, refreshing: false,
  retry: vi.fn(), onCreate: vi.fn(), disabled: false,
};
afterEach(() => { cleanup(); vi.clearAllMocks(); });
describe("PR action", () => {
  it("offers Create only after confirmed absence", () => {
    const { rerender } = render(<PullRequestAction {...base} />);
    expect(screen.queryByRole("button", { name: "Create PR" })).toBeNull();
    expect(screen.getByRole("status").textContent).toBe("Checking PR…");
    rerender(<PullRequestAction {...base} status="absent" />);
    fireEvent.click(screen.getByRole("button", { name: "Create PR" }));
    expect(base.onCreate).toHaveBeenCalledOnce();
  });
  it("offers retry, never Create, after an error", () => {
    render(<PullRequestAction {...base} status="error" error="GitHub unavailable" />);
    expect(screen.queryByRole("button", { name: "Create PR" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Retry PR lookup" }));
    expect(base.retry).toHaveBeenCalledOnce();
  });
  it("preserves a known link with stale status and retry", () => {
    render(<PullRequestAction {...base} status="found" error="GitHub unavailable"
      existing={{ exists: true, url: "https://github.com/o/r/pull/12", number: 12, title: "Fix", state: "MERGED" }} />);
    expect(screen.getByRole("link").getAttribute("href")).toBe("https://github.com/o/r/pull/12");
    expect(screen.getByText("merged")).toBeTruthy();
    expect(screen.getByText("PR status unavailable")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry PR lookup" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Create PR" })).toBeNull();
  });
  it("gives missing-gh installation guidance without offering Create", () => {
    render(<PullRequestAction {...base} status="error" error="GitHub CLI (gh) is not installed" />);
    expect(screen.getByRole("button", { name: "Retry PR lookup" }).getAttribute("title")).toContain("cli.github.com");
    expect(screen.queryByRole("button", { name: "Create PR" })).toBeNull();
  });
});
