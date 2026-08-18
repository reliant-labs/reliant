import { describe, it, expect, afterEach } from "vitest";
import {
  classifyFrame,
  RingBuffer,
  evaluateAutoDumpTrigger,
  isAutoDumpAllowed,
  pruneToWindow,
  shouldAutoEnable,
  chunkString,
  createRecorderForTest,
  SCROLL_JITTER_TAG,
  AUTO_DUMP_LARGE_CORRECTION_PX,
  AUTO_DUMP_BURST_COUNT,
  AUTO_DUMP_BURST_WINDOW_MS,
  AUTO_DUMP_MIN_INTERVAL_MS,
} from "../scrollDebug";

describe("scrollDebug classifyFrame", () => {
  it("attributes a frame near a wheel/touch/key input as 'user'", () => {
    const lastInputAt = 1000;
    const lastSelfScrollAt = null;
    expect(classifyFrame(10, 1050, lastInputAt, lastSelfScrollAt)).toBe("user");
  });

  it("attributes a frame near a scrollToIndex/followOutput call as 'ours'", () => {
    const lastInputAt = null;
    const lastSelfScrollAt = 1000;
    expect(classifyFrame(10, 1300, lastInputAt, lastSelfScrollAt)).toBe("ours");
  });

  it("attributes a frame with movement but no explaining input or self-scroll as 'correction'", () => {
    // Both signals have SOME history (so we're not just in the "no data yet"
    // state), but neither is within its attribution window of this frame.
    const lastInputAt = 0;
    const lastSelfScrollAt = 0;
    expect(classifyFrame(15, 5000, lastInputAt, lastSelfScrollAt)).toBe("correction");
  });

  it("prefers 'user' over 'ours' when both windows are live", () => {
    expect(classifyFrame(10, 1100, 1000, 1000)).toBe("user");
  });

  it("returns 'none' for a frame with zero delta", () => {
    expect(classifyFrame(0, 1000, 1000, 1000)).toBe("none");
  });

  it("returns 'unknown' before any input or self-scroll baseline has been observed", () => {
    expect(classifyFrame(10, 1000, null, null)).toBe("unknown");
  });
});

describe("scrollDebug RingBuffer", () => {
  it("returns items in insertion order under capacity", () => {
    const ring = new RingBuffer<number>(5);
    ring.push(1);
    ring.push(2);
    ring.push(3);
    expect(ring.toArray()).toEqual([1, 2, 3]);
    expect(ring.length).toBe(3);
  });

  it("bounds memory by overwriting the oldest entry once full", () => {
    const ring = new RingBuffer<number>(3);
    for (let i = 0; i < 10; i++) {
      ring.push(i);
    }
    // Only the last 3 pushes should survive, and length never exceeds capacity.
    expect(ring.toArray()).toEqual([7, 8, 9]);
    expect(ring.length).toBe(3);
  });

  it("clear() empties the buffer", () => {
    const ring = new RingBuffer<number>(3);
    ring.push(1);
    ring.push(2);
    ring.clear();
    expect(ring.toArray()).toEqual([]);
    expect(ring.length).toBe(0);
  });
});

// The auto-dump heuristic is the whole reason the instrument can report
// without the user having devtools open, so both directions matter: a real
// jitter MUST fire, and ordinary settled scrolling must NOT — a recorder that
// dumps on every correction frame is just a slower way to fill the log.
describe("scrollDebug auto-dump trigger", () => {
  it("fires on a single correction frame at or above the large-jump threshold", () => {
    expect(evaluateAutoDumpTrigger(AUTO_DUMP_LARGE_CORRECTION_PX, [])).toMatch(
      /single-frame correction/,
    );
  });

  it("fires on a large NEGATIVE correction — jitter is direction-agnostic", () => {
    expect(evaluateAutoDumpTrigger(-AUTO_DUMP_LARGE_CORRECTION_PX, [])).toMatch(
      /single-frame correction/,
    );
  });

  it("does not fire on a small isolated correction", () => {
    // A sub-threshold correction with no burst behind it is the sub-pixel
    // settling that happens constantly and is not visible as jitter.
    expect(evaluateAutoDumpTrigger(1, [100])).toBeNull();
  });

  it("fires when enough small corrections cluster inside the burst window", () => {
    // Three 1px corrections is invisible as any single frame but IS the
    // visible shimmer — this is the pattern the user reports as "jitter".
    const clustered = Array.from({ length: AUTO_DUMP_BURST_COUNT }, (_, i) => 100 + i * 10);
    expect(evaluateAutoDumpTrigger(1, clustered)).toMatch(/correction frames within/);
  });

  it("does not fire when the cluster is one frame short of the burst count", () => {
    const nearMiss = Array.from({ length: AUTO_DUMP_BURST_COUNT - 1 }, (_, i) => 100 + i * 10);
    expect(evaluateAutoDumpTrigger(1, nearMiss)).toBeNull();
  });

  it("drops corrections that fall outside the burst window", () => {
    // Old timestamps must not accumulate into a false burst: three corrections
    // spread over ten seconds are not jitter.
    const stale = [0, 10, 20];
    const pruned = pruneToWindow(stale, 5000, AUTO_DUMP_BURST_WINDOW_MS);
    expect(pruned).toEqual([]);
    expect(evaluateAutoDumpTrigger(1, pruned)).toBeNull();
  });

  it("keeps timestamps inside the window while dropping those outside", () => {
    const t = 1000;
    const mixed = [t - AUTO_DUMP_BURST_WINDOW_MS - 1, t - 10, t - 5];
    expect(pruneToWindow(mixed, t, AUTO_DUMP_BURST_WINDOW_MS)).toEqual([t - 10, t - 5]);
  });
});

describe("scrollDebug auto-dump rate limit", () => {
  it("allows the first dump, when nothing has been written yet", () => {
    expect(isAutoDumpAllowed(null, 0)).toBe(true);
  });

  it("blocks a second dump inside the interval", () => {
    // A sustained correction stream fires the trigger on nearly every frame;
    // without this the recorder would write ~60 dumps a second.
    expect(isAutoDumpAllowed(1000, 1000 + AUTO_DUMP_MIN_INTERVAL_MS - 1)).toBe(false);
  });

  it("allows another dump once the interval has elapsed", () => {
    expect(isAutoDumpAllowed(1000, 1000 + AUTO_DUMP_MIN_INTERVAL_MS)).toBe(true);
  });
});

describe("scrollDebug auto-arm", () => {
  it("is on by default in a dev build, so the first jitter is already recorded", () => {
    expect(shouldAutoEnable(null, true)).toBe(true);
  });

  it("is off by default in a production build", () => {
    expect(shouldAutoEnable(null, false)).toBe(false);
  });

  it("lets '0' force it off even in dev", () => {
    expect(shouldAutoEnable("0", true)).toBe(false);
  });

  it("lets '1' force it on outside dev", () => {
    expect(shouldAutoEnable("1", false)).toBe(true);
  });
});

// End-to-end over the REAL recorder. The pure functions above prove the
// heuristic; these prove the recorder actually wires them to the emit path —
// a correct predicate that is never consulted would pass every test above and
// still record nothing.
describe("scrollDebug recorder emit path", () => {
  /** A stand-in scroller whose geometry the test drives directly. */
  function fakeScroller(): HTMLElement & { scrollTop: number } {
    return {
      scrollTop: 0,
      scrollHeight: 10_000,
      clientHeight: 800,
      getBoundingClientRect: () => ({ top: 0, bottom: 800 }),
    } as unknown as HTMLElement & { scrollTop: number };
  }

  /**
   * Drive the recorder to the point where a correction is classifiable: both
   * attribution baselines must exist and be stale, or classifyFrame returns
   * 'unknown'/'user'/'ours' instead of 'correction'.
   */
  function armCorrectionBaseline(
    recorder: ReturnType<typeof createRecorderForTest>,
    setTime: (t: number) => void,
  ) {
    setTime(0);
    recorder.mark("input:wheel", { deltaY: 1 });
    recorder.mark("scrollToIndex", { reason: "test" });
    // Move well past both attribution windows so later movement is unexplained.
    setTime(5000);
  }

  function setup() {
    let clockValue = 0;
    const setTime = (t: number) => {
      clockValue = t;
    };
    const logged: Array<{ level: string; args: unknown[] }> = [];
    (window as unknown as { electronAPI: unknown }).electronAPI = {
      log: (level: string, ...args: unknown[]) => logged.push({ level, args }),
    };
    const recorder = createRecorderForTest(() => clockValue);
    const scroller = fakeScroller();
    recorder.registerScroller(scroller);
    return { recorder, scroller, logged, setTime };
  }

  afterEach(() => {
    delete (window as unknown as { electronAPI?: unknown }).electronAPI;
  });

  it("emits a tagged record through electronAPI.log when a large correction fires", () => {
    const { recorder, scroller, logged, setTime } = setup();
    recorder.enable();
    armCorrectionBaseline(recorder, setTime);

    // Baseline frame, then a jump well over the large-correction threshold.
    recorder.sampleFrameForTest();
    scroller.scrollTop += AUTO_DUMP_LARGE_CORRECTION_PX * 3;
    recorder.sampleFrameForTest();

    // The dump waits for its post-trigger tail before emitting.
    expect(logged).toHaveLength(0);
    for (let i = 0; i < 40; i++) {
      setTime(5000 + i);
      recorder.sampleFrameForTest();
    }

    expect(logged.length).toBeGreaterThan(0);
    expect(logged.every((l) => l.level === "warn")).toBe(true);
    // Every line must carry the tag, or the rg extraction misses it.
    for (const line of logged) {
      expect(String(line.args[0])).toContain(SCROLL_JITTER_TAG);
    }
    recorder.disable();
  });

  it("does not emit when scrolling is attributable to the user", () => {
    const { recorder, scroller, logged, setTime } = setup();
    recorder.enable();
    setTime(0);
    recorder.sampleFrameForTest();

    // A large movement, but a wheel event explains it — this is the user
    // scrolling, which must never be reported as jitter.
    for (let i = 1; i < 50; i++) {
      setTime(i * 10);
      recorder.mark("input:wheel", { deltaY: 50 });
      scroller.scrollTop += 50;
      recorder.sampleFrameForTest();
    }

    expect(logged).toHaveLength(0);
    recorder.disable();
  });

  it("holds the rate limit across a sustained correction stream", () => {
    const { recorder, scroller, logged, setTime } = setup();
    recorder.enable();
    armCorrectionBaseline(recorder, setTime);

    // Every frame is a large unexplained correction for ~2s of frames. Without
    // the rate limit this would emit a dump on nearly every one of them.
    for (let i = 0; i < 120; i++) {
      setTime(5000 + i * 16);
      scroller.scrollTop += AUTO_DUMP_LARGE_CORRECTION_PX * 2;
      recorder.sampleFrameForTest();
    }

    const headerLines = logged.filter((l) => !String(l.args[0]).includes("json "));
    // ~2s of sustained jitter is inside one AUTO_DUMP_MIN_INTERVAL_MS window,
    // so exactly one dump may be written.
    expect(headerLines).toHaveLength(1);
    recorder.disable();
  });

  it("degrades quietly when there is no electronAPI (plain browser)", () => {
    const { recorder, scroller, setTime } = setup();
    delete (window as unknown as { electronAPI?: unknown }).electronAPI;
    recorder.enable();
    armCorrectionBaseline(recorder, setTime);

    // Must not throw when neither the IPC channel nor a usable download path
    // is available — a failed diagnostic write is not a second bug.
    expect(() => {
      recorder.sampleFrameForTest();
      scroller.scrollTop += AUTO_DUMP_LARGE_CORRECTION_PX * 3;
      for (let i = 0; i < 40; i++) {
        setTime(5000 + i);
        recorder.sampleFrameForTest();
      }
    }).not.toThrow();
    recorder.disable();
  });
});

describe("scrollDebug rowRef without ResizeObserver", () => {
  // jsdom (the test environment) has no ResizeObserver. rowRef must degrade
  // to an inert-but-valid ref rather than throwing when constructing one.
  it("returns a no-op ref callback when ResizeObserver is unavailable", () => {
    const original = globalThis.ResizeObserver;
    // @ts-expect-error -- simulating an environment without ResizeObserver
    delete globalThis.ResizeObserver;
    try {
      const recorder = createRecorderForTest(() => 0);
      recorder.enable();

      const ref = recorder.rowRef("row-1");
      expect(ref).toBeTypeOf("function");

      const el = document.createElement("div");
      let cleanup: (() => void) | void;
      expect(() => {
        cleanup = ref!(el) as (() => void) | void;
      }).not.toThrow();
      // The cleanup contract must still hold — callers rely on a callable
      // return value, not undefined, even when instrumentation is inert.
      expect(cleanup).toBeTypeOf("function");
      expect(() => cleanup!()).not.toThrow();

      recorder.disable();
    } finally {
      globalThis.ResizeObserver = original;
    }
  });
});

describe("scrollDebug log chunking", () => {
  it("splits a payload into reassemblable chunks", () => {
    const text = "abcdefghij";
    const chunks = chunkString(text, 4);
    expect(chunks).toEqual(["abcd", "efgh", "ij"]);
    expect(chunks.join("")).toBe(text);
  });

  it("leaves a payload under the chunk size in one piece", () => {
    expect(chunkString("abc", 10)).toEqual(["abc"]);
  });

  it("emits one empty chunk rather than nothing for empty input", () => {
    expect(chunkString("", 10)).toEqual([""]);
  });
});
