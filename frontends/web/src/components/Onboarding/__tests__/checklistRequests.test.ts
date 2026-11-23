/**
 * The Setup Guide's requests of the chat home (checklistRequests.ts). The
 * guide shows on every page, and "Start chatting" and "Create a workspace"
 * did nothing off the chat home: the first focused a selector nothing
 * renders, the second fired an event nobody was listening for.
 */
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  CHAT_INPUT_SELECTOR,
  CREATE_WORKTREE_REQUEST_TTL_MS,
  OPEN_CREATE_WORKTREE_EVENT,
  clearCreateWorktreeRequest,
  createWorktreeRequested,
  requestChatInputFocus,
  requestCreateWorktree,
} from "../checklistRequests";

afterEach(() => {
  clearCreateWorktreeRequest();
  vi.useRealTimers();
  document.body.innerHTML = "";
});

describe("requestCreateWorktree", () => {
  it("is answered at once by a mounted chat home", () => {
    const opened = vi.fn();
    const handler = () => {
      clearCreateWorktreeRequest();
      opened();
    };
    window.addEventListener(OPEN_CREATE_WORKTREE_EVENT, handler);
    try {
      expect(requestCreateWorktree()).toBe(true);
    } finally {
      window.removeEventListener(OPEN_CREATE_WORKTREE_EVENT, handler);
    }
    expect(opened).toHaveBeenCalledTimes(1);
    expect(createWorktreeRequested()).toBe(false);
  });

  it("waits for the chat home when none is listening, across remounts, until the modal closes", () => {
    expect(requestCreateWorktree()).toBe(false);
    // Arriving can mount the chat home twice: both find the request.
    expect(createWorktreeRequested()).toBe(true);
    expect(createWorktreeRequested()).toBe(true);
    clearCreateWorktreeRequest();
    expect(createWorktreeRequested()).toBe(false);
  });

  it("expires, so a later visit to the chat home does not open the modal unasked", () => {
    vi.useFakeTimers();
    expect(requestCreateWorktree()).toBe(false);
    vi.advanceTimersByTime(CREATE_WORKTREE_REQUEST_TTL_MS + 1);
    expect(createWorktreeRequested()).toBe(false);
  });
});

describe("requestChatInputFocus", () => {
  it("asks the composer for focus when one is on screen", () => {
    document.body.innerHTML = `<div data-onboarding="chat-input"></div>`;
    expect(document.querySelector(CHAT_INPUT_SELECTOR)).not.toBeNull();
    const focus = vi.fn();
    window.addEventListener("focus-chat-input", focus);
    try {
      expect(requestChatInputFocus()).toBe(true);
    } finally {
      window.removeEventListener("focus-chat-input", focus);
    }
    expect(focus).toHaveBeenCalledTimes(1);
  });

  it("says so when there is no composer, so the guide can open the chat home", () => {
    expect(requestChatInputFocus()).toBe(false);
  });
});
