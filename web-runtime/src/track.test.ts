// The contract of track(), setTelemetryUser() and disableTelemetry() with no
// SDK behind them: bounds, redaction, the queue before init, and the no-op
// cases. Their wiring to the SDK is pinned in telemetry.test.ts.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  disableTelemetry,
  isTelemetryDisabled,
  MAX_EVENT_NAME_CHARS,
  MAX_PROP_KEYS,
  MAX_PROP_VALUE_CHARS,
  MAX_QUEUED_EVENTS,
  MAX_USER_ID_CHARS,
  redactSecrets,
  resetTrackState,
  sanitizeProps,
  setTelemetrySink,
  setTelemetryUser,
  track,
  type TelemetrySink,
} from "./track.js";

function sink() {
  const track = vi.fn<TelemetrySink["track"]>();
  const setUser = vi.fn<TelemetrySink["setUser"]>();
  const disable = vi.fn<TelemetrySink["disable"]>();
  return { track, setUser, disable };
}

beforeEach(() => {
  resetTrackState();
  vi.stubGlobal("window", {});
});

afterEach(() => {
  resetTrackState();
  vi.unstubAllGlobals();
});

describe("sanitizeProps", () => {
  it("keeps strings, finite numbers and booleans, and drops everything else", () => {
    expect(
      sanitizeProps({
        s: "x",
        n: 3,
        b: false,
        nan: Number.NaN,
        inf: Number.POSITIVE_INFINITY,
        o: { a: 1 },
        a: [1],
        u: undefined,
        nul: null,
        fn: () => 1,
      }),
    ).toEqual({ s: "x", n: 3, b: false });
  });

  it("accepts only [a-z0-9_.]{1,64} keys", () => {
    const longKey = "k".repeat(65);
    expect(
      sanitizeProps({
        ok_key: 1,
        "dotted.key": 2,
        Upper: 3,
        "has space": 4,
        "dash-key": 5,
        "": 6,
        [longKey]: 7,
        [`${"k".repeat(64)}`]: 8,
      }),
    ).toEqual({ ok_key: 1, "dotted.key": 2, ["k".repeat(64)]: 8 });
  });

  it("keeps at most MAX_PROP_KEYS keys, in insertion order", () => {
    const props: Record<string, number> = {};
    for (let i = 0; i < MAX_PROP_KEYS + 10; i++) props[`k${i}`] = i;

    const out = sanitizeProps(props);

    expect(Object.keys(out)).toHaveLength(MAX_PROP_KEYS);
    expect(Object.keys(out)[0]).toBe("k0");
    expect(out).not.toHaveProperty(`k${MAX_PROP_KEYS}`);
  });

  it("does not spend the key budget on dropped entries", () => {
    const props: Record<string, unknown> = {};
    for (let i = 0; i < 30; i++) props[`Bad${i}`] = 1;
    props["good"] = 1;

    expect(sanitizeProps(props)).toEqual({ good: 1 });
  });

  it("truncates string values to MAX_PROP_VALUE_CHARS", () => {
    const out = sanitizeProps({ v: "word ".repeat(MAX_PROP_VALUE_CHARS) });

    expect((out.v as string).length).toBe(MAX_PROP_VALUE_CHARS);
  });

  it("returns {} for a non-object", () => {
    expect(sanitizeProps(undefined)).toEqual({});
    expect(sanitizeProps(null as never)).toEqual({});
    expect(sanitizeProps("str" as never)).toEqual({});
  });
});

describe("redactSecrets", () => {
  const cases: Array<[string, string]> = [
    [
      "postgres://user:hunter2@db:5432/app",
      "postgres://[redacted]@db:5432/app",
    ],
    ["Authorization: Bearer abc.def.ghi", "Authorization: [redacted]"],
    ["password=hunter2&next=/home", "password=[redacted]&next=/home"],
    ["bearer abcDEF123456", "bearer [redacted]"],
    [
      "jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.sig",
      "jwt [redacted]",
    ],
    ["key sk-ant-api03-abcdefghij", "key [redacted]"],
    ["gh ghp_abcdefghij1234", "gh [redacted]"],
    ["blob " + "A".repeat(48), "blob [redacted]"],
  ];

  it.each(cases)("redacts %s", (input, want) => {
    expect(redactSecrets(input)).toBe(want);
  });

  it("leaves ordinary values alone", () => {
    for (const v of [
      "onboarding",
      "3fa85f64-5717-4562-b3fc-2c963f66afa6",
      "/settings/billing",
      "Create project",
    ]) {
      expect(redactSecrets(v)).toBe(v);
    }
  });

  it("is applied to prop values", () => {
    expect(sanitizeProps({ note: "token=abc123" })).toEqual({
      note: "token=[redacted]",
    });
  });
});

describe("track", () => {
  it("forwards a sanitized event to the installed sink", () => {
    const s = sink();
    setTelemetrySink(s);

    track("  onboarding_step ", { step: 2, Bad: 1 });

    expect(s.track).toHaveBeenCalledWith("onboarding_step", { step: 2 });
  });

  it("bounds the event name and ignores blank or non-string names", () => {
    const s = sink();
    setTelemetrySink(s);

    track("n".repeat(MAX_EVENT_NAME_CHARS + 40));
    track("   ");
    track(undefined as never);
    track(42 as never);

    expect(s.track).toHaveBeenCalledTimes(1);
    expect(s.track.mock.calls[0]![0]).toHaveLength(MAX_EVENT_NAME_CHARS);
  });

  it("holds calls until a sink is installed, then replays them in order", () => {
    track("one");
    track("two", { a: 1 });
    const s = sink();

    setTelemetrySink(s);

    expect(s.track.mock.calls).toEqual([
      ["one", {}],
      ["two", { a: 1 }],
    ]);
    // Replayed once: a second install has nothing left to replay.
    const again = sink();
    setTelemetrySink(again);
    expect(again.track).not.toHaveBeenCalled();
  });

  it("holds at most MAX_QUEUED_EVENTS and drops the rest", () => {
    for (let i = 0; i < MAX_QUEUED_EVENTS + 25; i++) track(`e${i}`);
    const s = sink();

    setTelemetrySink(s);

    expect(s.track).toHaveBeenCalledTimes(MAX_QUEUED_EVENTS);
    expect(s.track.mock.calls[0]![0]).toBe("e0");
    expect(s.track.mock.calls.at(-1)![0]).toBe(`e${MAX_QUEUED_EVENTS - 1}`);
  });

  it("drops what was held when the sink is cleared, so a failed start leaves no queue", () => {
    track("lost");
    setTelemetrySink(null);
    const s = sink();

    setTelemetrySink(s);

    expect(s.track).not.toHaveBeenCalled();
  });

  it("is a no-op on the server", () => {
    vi.unstubAllGlobals(); // vitest runs in node: no window
    const s = sink();
    setTelemetrySink(s);

    track("ssr");
    setTelemetryUser("u");

    expect(s.track).not.toHaveBeenCalled();
    expect(s.setUser).not.toHaveBeenCalled();
  });

  it("never throws when the sink does", () => {
    const s = sink();
    s.track.mockImplementation(() => {
      throw new Error("boom");
    });
    setTelemetrySink(s);

    expect(() => track("x")).not.toThrow();
  });
});

describe("setTelemetryUser", () => {
  it("passes the id through, bounded and trimmed", () => {
    const s = sink();
    setTelemetrySink(s);

    setTelemetryUser(" u-1 ");
    setTelemetryUser("x".repeat(MAX_USER_ID_CHARS + 50));

    expect(s.setUser.mock.calls[0]![0]).toBe("u-1");
    expect(s.setUser.mock.calls[1]![0]).toHaveLength(MAX_USER_ID_CHARS);
  });

  it("clears with an empty id for null, undefined or a non-string", () => {
    const s = sink();
    setTelemetrySink(s);

    setTelemetryUser(null);
    setTelemetryUser(undefined);
    setTelemetryUser(7 as never);

    expect(s.setUser.mock.calls).toEqual([[""], [""], [""]]);
  });

  it("holds only the last user while the SDK loads, in front of later events", () => {
    setTelemetryUser("first");
    track("e");
    setTelemetryUser("second");
    const s = sink();

    setTelemetrySink(s);

    expect(s.setUser.mock.calls).toEqual([["second"]]);
    expect(s.track).toHaveBeenCalledTimes(1);
  });
});

describe("disableTelemetry", () => {
  it("stops the sink, drops held events and silences later calls", () => {
    track("held");
    const s = sink();
    setTelemetrySink(s);
    s.track.mockClear();

    disableTelemetry();
    track("after");
    setTelemetryUser("u");

    expect(isTelemetryDisabled()).toBe(true);
    expect(s.disable).toHaveBeenCalledTimes(1);
    expect(s.track).not.toHaveBeenCalled();
    expect(s.setUser).not.toHaveBeenCalled();
  });

  it("discards events held before a sink arrives, and replays none to a late sink", () => {
    track("held");
    disableTelemetry();
    const s = sink();

    setTelemetrySink(s);
    track("later");

    expect(s.track).not.toHaveBeenCalled();
  });

  it("is safe with no sink and when called twice", () => {
    expect(() => {
      disableTelemetry();
      disableTelemetry();
    }).not.toThrow();
  });

  it("never throws when the sink's disable does", () => {
    const s = sink();
    s.disable.mockImplementation(() => {
      throw new Error("boom");
    });
    setTelemetrySink(s);

    expect(() => disableTelemetry()).not.toThrow();
  });
});
