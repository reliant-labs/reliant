import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { DaemonInfoSchema, DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { chatMachineNotice, formatWaitElapsed, resolveChatMachine } from "../chatMachineNotice";

function machine(status: DaemonStatus, extra: Partial<DaemonInfo> = {}): DaemonInfo {
  return create(DaemonInfoSchema, { daemonId: "d-1", hostname: "box", status, ...extra });
}

const NOW = 1_000_000_000;

describe("chatMachineNotice", () => {
  it("says the machine is waking and the message will send, with the control plane's progress", () => {
    const notice = chatMachineNotice({
      daemon: machine(DaemonStatus.PENDING, {
        lastStatusMessage: "Starting a machine for your workspace. A first start usually takes a few minutes.",
      }),
      messageUnanswered: true,
      messageSentAt: NOW - 42_000,
      now: NOW,
    });
    expect(notice).toMatchObject({
      kind: "waking",
      title: "Waking your machine — your message will send when it connects",
      detail: "Starting a machine for your workspace. A first start usually takes a few minutes.",
      since: NOW - 42_000,
      offerStart: false,
      offerRetry: false,
    });
  });

  it("times the wait from the wake when one was recorded", () => {
    const notice = chatMachineNotice({
      daemon: machine(DaemonStatus.PENDING),
      messageUnanswered: true,
      messageSentAt: NOW - 600_000,
      wakeStartedAt: NOW - 30_000,
      now: NOW,
    });
    expect(notice?.since).toBe(NOW - 30_000);
  });

  it("speaks for the run, not a message, when the run is already past it", () => {
    expect(chatMachineNotice({ daemon: machine(DaemonStatus.PENDING), messageUnanswered: false })?.title).toBe(
      "Waking your machine — the run continues when it connects",
    );
    expect(
      chatMachineNotice({ daemon: machine(DaemonStatus.DISCONNECTED), messageUnanswered: false })?.title,
    ).toBe("Reconnecting to your machine — the run continues when it connects");
  });

  it("says a failed machine failed, with its reason and Try again", () => {
    const notice = chatMachineNotice({
      daemon: machine(DaemonStatus.FAILED, { lastStatusMessage: "image pull failed: manifest unknown" }),
      messageUnanswered: true,
    });
    expect(notice).toMatchObject({
      kind: "failed",
      title: "Your machine failed to start",
      detail: "image pull failed: manifest unknown",
      offerRetry: true,
    });
    expect(notice?.since).toBeUndefined();
  });

  it("says an asleep machine is starting while something wakes it, and offers Start it otherwise", () => {
    const asleep = machine(DaemonStatus.SUSPENDED);
    expect(chatMachineNotice({ daemon: asleep, messageUnanswered: true, sending: true })).toMatchObject({
      title: "Your machine is asleep — starting it",
      offerStart: false,
    });
    expect(
      chatMachineNotice({ daemon: asleep, messageUnanswered: true, wakeStartedAt: NOW - 10_000, now: NOW }),
    ).toMatchObject({ title: "Your machine is asleep — starting it", offerStart: false });

    // A wake recorded long ago that left it asleep did not wake it.
    expect(
      chatMachineNotice({ daemon: asleep, messageUnanswered: true, wakeStartedAt: NOW - 120_000, now: NOW }),
    ).toMatchObject({
      title: "Your machine is asleep",
      detail: "Start your machine and your message will send when it connects.",
      offerStart: true,
    });
  });

  it("says nothing about a machine that is up, or unknown", () => {
    expect(chatMachineNotice({ daemon: machine(DaemonStatus.ACTIVE), messageUnanswered: true })).toBeNull();
    expect(chatMachineNotice({ daemon: machine(DaemonStatus.IDLE), messageUnanswered: true })).toBeNull();
    expect(chatMachineNotice({ daemon: undefined, messageUnanswered: true })).toBeNull();
  });
});

describe("resolveChatMachine", () => {
  const up = create(DaemonInfoSchema, { daemonId: "up", status: DaemonStatus.ACTIVE });
  const starting = create(DaemonInfoSchema, { daemonId: "starting", status: DaemonStatus.PENDING });

  it("is the pinned machine for a pinned chat", () => {
    expect(resolveChatMachine([up, starting], "starting")?.daemonId).toBe("starting");
    expect(resolveChatMachine([up], "gone")).toBeUndefined();
  });

  it("is the default machine for an unpinned chat", () => {
    expect(resolveChatMachine([starting], undefined)?.daemonId).toBe("starting");
    expect(resolveChatMachine([starting, up], undefined)?.daemonId).toBe("up");
  });

  it("is unknown until the registry answers", () => {
    expect(resolveChatMachine(undefined, "starting")).toBeUndefined();
  });
});

describe("formatWaitElapsed", () => {
  it("reads as seconds, then minutes, then hours", () => {
    expect(formatWaitElapsed(42_000)).toBe("42s");
    expect(formatWaitElapsed(185_000)).toBe("3m 05s");
    expect(formatWaitElapsed(3_725_000)).toBe("1h 02m");
    expect(formatWaitElapsed(-5)).toBe("0s");
  });
});
