// Copyright (c) 2025 Reliant Labs

/**
 * The terminal replay: its engine (replay.ts) step by step, then the player
 * on a small cast — Play prints each event at its time and speed, Pause holds,
 * Step finishes a command or shows the whole next one, Restart clears, and
 * each Copy puts exactly the command (or the proto) on the clipboard. Last,
 * the committed recording plays to its end.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

import { scaffoldFixtures, type ScaffoldCast } from "../fixtures";
import { delayToNext, EDIT_HOLD, formatSeconds, initialReplay, isComplete, outputOf, replayReducer, terminalText, type ReplayState } from "../replay";
import { TerminalReplay } from "../TerminalReplay";

const CAST: ScaffoldCast = {
  schema: "forge-demo.scaffold-cast/v1",
  producer: "frontends/web/scripts/demo/capture-forge-scaffold.sh",
  forge_version: "v1.0.0",
  captured_at: "2026-11-06T00:00:00.000Z",
  asciicast: { version: 2, width: 100, height: 30, idle_time_limit: 1, title: "t" },
  steps: [
    { kind: "command", cwd: "~", command: "forge project new shop", exit_code: 0, real_seconds: 30, duration: 2, events: [[0.5, "one\n"], [1.5, "two\n"]] },
    { kind: "edit", cwd: "~/shop", path: "proto/a.proto", replaces: "// TODO", text: "message A {}\n", lines: 1 },
    { kind: "command", cwd: "~/shop", command: "forge scaffold", exit_code: 0, real_seconds: 5, duration: 1, events: [[0.2, "done\n"]] },
  ],
};

/** Every timer the replay sets: one per event, one per step's tail, two per edit. */
const eventsIn = (cast: ScaffoldCast) => cast.steps.reduce((n, s) => n + (s.kind === "command" ? s.events.length + 1 : 2), 0);

const reduce = (state: ReplayState, ...actions: Parameters<typeof replayReducer>[2][]) =>
  actions.reduce((s, a) => replayReducer(CAST, s, a), state);

describe("the replay engine", () => {
  it("plays each event, then the next step, and stops at the end", () => {
    let s = reduce(initialReplay(1), { type: "play" });
    expect(s).toMatchObject({ step: 0, shown: 0, playing: true });
    expect(delayToNext(CAST, s)).toBe(0.5);
    s = reduce(s, { type: "tick" });
    expect(outputOf(CAST.steps[0], s.shown)).toBe("one\n");
    expect(delayToNext(CAST, s)).toBe(1);
    s = reduce(s, { type: "tick" });
    // The rest of the step's duration after its last event.
    expect(delayToNext(CAST, s)).toBe(0.5);
    s = reduce(s, { type: "tick" });
    expect(s).toMatchObject({ step: 1, shown: 0 });
    s = reduce(s, { type: "tick" });
    expect(delayToNext(CAST, s)).toBe(EDIT_HOLD);
    s = reduce(s, { type: "tick" }, { type: "tick" }, { type: "tick" });
    expect(isComplete(CAST, s)).toBe(true);
    expect(delayToNext(CAST, s)).toBeNull();
    expect(reduce(s, { type: "tick" }).playing).toBe(false);
  });

  it("waits less at a higher speed", () => {
    const s = reduce(initialReplay(4), { type: "play" });
    expect(delayToNext(CAST, s)).toBe(0.125);
  });

  it("steps: finishes the command on screen, then shows the whole next step, paused", () => {
    let s = reduce(initialReplay(), { type: "play" }, { type: "tick" }, { type: "step" });
    expect(s).toMatchObject({ step: 0, shown: 2, playing: false });
    s = reduce(s, { type: "step" });
    expect(s).toMatchObject({ step: 1, shown: 1 });
    s = reduce(s, { type: "step" });
    expect(s).toMatchObject({ step: 2, shown: 1 });
    expect(isComplete(CAST, s)).toBe(true);
    // Play from the end starts over.
    expect(reduce(s, { type: "play" })).toMatchObject({ step: 0, shown: 0, playing: true });
  });

  it("restarts at the top, keeping the speed", () => {
    expect(reduce(initialReplay(4), { type: "step" }, { type: "restart" })).toEqual(initialReplay(4));
  });

  it("shows a carriage return as a terminal does, and real times as minutes", () => {
    expect(terminalText("Progress: 1\rProgress: 2\r\ndone\n")).toBe("Progress: 2\ndone\n");
    expect(formatSeconds(38.9)).toBe("39s");
    expect(formatSeconds(140)).toBe("2m 20s");
    expect(formatSeconds(120)).toBe("2m");
  });
});

describe("<TerminalReplay>", () => {
  let writeText: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    vi.useFakeTimers();
    writeText = vi.fn(async () => undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  const screenText = () => screen.getByTestId("replay-screen").textContent ?? "";
  const advance = (ms: number) => act(() => void vi.advanceTimersByTime(ms));

  it("plays at its speed, and pauses", () => {
    render(<TerminalReplay cast={CAST} />);
    expect(screenText()).toContain("~ $ forge project new shop");
    expect(screenText()).not.toContain("one");
    fireEvent.click(screen.getByTestId("replay-speed-1"));
    fireEvent.click(screen.getByTestId("replay-toggle"));
    expect(screen.getByTestId("replay-toggle")).toHaveTextContent("Pause");
    advance(500);
    expect(screenText()).toContain("one");
    expect(screenText()).not.toContain("two");
    fireEvent.click(screen.getByTestId("replay-toggle"));
    advance(5_000);
    expect(screenText()).not.toContain("two");
    expect(screen.getByTestId("replay-toggle")).toHaveTextContent("Resume");
  });

  it("steps through, command by command, and shows how long each really took", () => {
    render(<TerminalReplay cast={CAST} />);
    fireEvent.click(screen.getByTestId("replay-step"));
    expect(screenText()).toContain("one\ntwo");
    expect(screen.getByTestId("replay-step-0")).toHaveTextContent("30s");
    fireEvent.click(screen.getByTestId("replay-step"));
    expect(screen.getByTestId("replay-step-1")).toHaveTextContent("You write, in proto/a.proto");
    expect(screenText()).toContain("message A {}");
    fireEvent.click(screen.getByTestId("replay-step"));
    expect(screenText()).toContain("~/shop $ forge scaffold");
    expect(screenText()).toContain("done");
    expect(screen.getByTestId("replay-step")).toBeDisabled();
    expect(screen.getByTestId("replay-toggle")).toHaveTextContent("Play again");
    fireEvent.click(screen.getByTestId("replay-restart"));
    expect(screenText()).not.toContain("one");
  });

  it("copies exactly the command, or the proto you write", async () => {
    render(<TerminalReplay cast={CAST} />);
    await act(async () => {
      fireEvent.click(within(screen.getByTestId("replay-step-0")).getByRole("button", { name: "Copy forge project new shop" }));
    });
    expect(writeText).toHaveBeenLastCalledWith("forge project new shop");
    expect(within(screen.getByTestId("replay-step-0")).getByRole("button", { name: "Copied forge project new shop" })).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("replay-step"));
    fireEvent.click(screen.getByTestId("replay-step"));
    await act(async () => {
      fireEvent.click(within(screen.getByTestId("replay-step-1")).getByRole("button", { name: /Copy the proto/ }));
    });
    expect(writeText).toHaveBeenLastCalledWith("message A {}\n");
  });

  it("plays the committed recording to its end", () => {
    const { cast } = scaffoldFixtures();
    render(<TerminalReplay cast={cast} autoPlay />);
    const total = cast.steps.reduce((n, s) => n + (s.kind === "command" ? s.duration : EDIT_HOLD + 0.3), 0);
    // One timer fires per advance (the next is set after the render), so step until it ends; at the
    // default 2× that takes no more than half the played length.
    let elapsed = 0;
    while (screen.getByTestId("replay-toggle").textContent !== "Play again" && elapsed < total * 1000) {
      advance(100);
      elapsed += 100;
    }
    expect(screen.getByTestId("replay-toggle")).toHaveTextContent("Play again");
    expect(elapsed).toBeLessThanOrEqual((total / 2) * 1000 + 100 * eventsIn(cast));
    expect(screenText()).toMatch(/All \d+ gating linters passed/);
    expect(screen.getAllByTestId("replay-command").map((el) => el.textContent)).toEqual(
      cast.steps.flatMap((s) => (s.kind === "command" ? [s.command] : []))
    );
  });
});
