import { beforeEach, describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";

import { useGitInitialization } from "../useGitInitialization";
import type { Project } from "../../store/projectStore";

// The prompt is driven by an effect whose dependencies change on a 5s daemon
// poll, so "show the modal whenever is_git_repo is false" re-fires constantly.
// These tests pin the dismissal contract that stops it.

const STORAGE_KEY = "reliant.gitInit.dismissedProjectIds";

function project(overrides: Partial<Project> = {}): Project {
  return {
    id: "project-1",
    name: "My Project",
    is_git_repo: false,
    ...overrides,
  } as Project;
}

describe("useGitInitialization", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("does not prompt for a project that is already a git repo", () => {
    const { result } = renderHook(() => useGitInitialization());

    let proceeded = false;
    act(() => {
      proceeded = result.current.checkGitInitialization(project({ is_git_repo: true }));
    });

    expect(proceeded).toBe(true);
    expect(result.current.showGitInitModal).toBe(false);
  });

  it("prompts for a non-git project", () => {
    const { result } = renderHook(() => useGitInitialization());

    let proceeded = true;
    act(() => {
      proceeded = result.current.checkGitInitialization(project());
    });

    expect(proceeded).toBe(false);
    expect(result.current.showGitInitModal).toBe(true);
    expect(result.current.gitInitProjectInfo).toEqual({ id: "project-1", name: "My Project" });
  });

  it("stops prompting after the user dismisses", () => {
    const { result } = renderHook(() => useGitInitialization());

    act(() => {
      result.current.checkGitInitialization(project());
    });
    act(() => {
      result.current.handleDismissGitInit();
    });

    expect(result.current.showGitInitModal).toBe(false);

    // Simulates the effect re-running on the next daemon poll.
    let proceeded = false;
    act(() => {
      proceeded = result.current.checkGitInitialization(project());
    });

    expect(proceeded).toBe(true);
    expect(result.current.showGitInitModal).toBe(false);
  });

  it("remembers the dismissal across remounts", () => {
    const first = renderHook(() => useGitInitialization());
    act(() => {
      first.result.current.checkGitInitialization(project());
    });
    act(() => {
      first.result.current.handleDismissGitInit();
    });

    const second = renderHook(() => useGitInitialization());
    act(() => {
      second.result.current.checkGitInitialization(project());
    });

    expect(second.result.current.showGitInitModal).toBe(false);
  });

  it("keeps dismissals scoped per project", () => {
    const { result } = renderHook(() => useGitInitialization());

    act(() => {
      result.current.checkGitInitialization(project());
    });
    act(() => {
      result.current.handleDismissGitInit();
    });

    // A different project must still be prompted.
    act(() => {
      result.current.checkGitInitialization(project({ id: "project-2", name: "Other" }));
    });

    expect(result.current.showGitInitModal).toBe(true);
    expect(result.current.gitInitProjectInfo?.id).toBe("project-2");
  });

  it("does not record a dismissal when closing after a successful init", () => {
    const { result } = renderHook(() => useGitInitialization());

    act(() => {
      result.current.checkGitInitialization(project());
    });
    act(() => {
      result.current.handleCloseGitInitModal();
    });

    expect(result.current.showGitInitModal).toBe(false);
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it("survives unreadable localStorage", () => {
    localStorage.setItem(STORAGE_KEY, "not json");
    const { result } = renderHook(() => useGitInitialization());

    let proceeded = true;
    act(() => {
      proceeded = result.current.checkGitInitialization(project());
    });

    expect(proceeded).toBe(false);
  });
});
