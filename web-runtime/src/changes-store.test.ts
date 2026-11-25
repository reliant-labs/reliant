// The contract of the Changes store: loadChanges never rejects, evaluate
// decides through changes.ts, and only enrolled, preview and everyone results
// are recorded as flag_evaluated, once per person, change, variant and run.
// The twin of pkg/changes/record_test.go.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { previewHash } from "./changes.js";
import {
  CHANGES_SNAPSHOT_URL,
  changeBool,
  changeJson,
  changeNumber,
  changeString,
  loadChanges,
} from "./changes-store.js";
import { FLAG_EVALUATED_EVENT, resetExposureState } from "./exposure.js";
import {
  resetTrackState,
  setTelemetrySink,
  type TelemetrySink,
} from "./track.js";

/** One change per outcome the store must tell apart. */
const SNAPSHOT = JSON.stringify({
  schema: "forge.changes/v1",
  revision: 7,
  changes: {
    exp: {
      variants: { control: false, treatment: true },
      default_variant: "control",
      delivery: {
        mode: "experiment",
        unit: "user",
        run: "r_1",
        salt: "r_1",
        percent_bp: 10000,
        audience: { attribute: "plan", in: ["paid"] },
        split: [{ variant: "treatment", weight_bp: 10000 }],
        preview: [
          { unit_hash: previewHash("exp", "previewer"), variant: "control" },
        ],
      },
    },
    acct: {
      variants: { on: true },
      delivery: {
        mode: "rollout",
        unit: "account",
        run: "r_2",
        salt: "r_2",
        percent_bp: 10000,
        rollout_variant: "on",
      },
    },
    zero: {
      variants: { on: true },
      delivery: {
        mode: "rollout",
        unit: "user",
        run: "r_3",
        salt: "r_3",
        percent_bp: 0,
        rollout_variant: "on",
      },
    },
    all: {
      variants: { blue: "blue" },
      delivery: { mode: "everyone", variant: "blue" },
    },
    off: {
      variants: { on: true, no: false },
      default_variant: "no",
      delivery: { mode: "off" },
    },
    bad: { variants: { on: true }, delivery: { mode: "sideways" } },
    limits: {
      variants: { small: { max: 3 } },
      delivery: { mode: "everyone", variant: "small" },
    },
    ratio: {
      variants: { half: 0.5 },
      delivery: { mode: "everyone", variant: "half" },
    },
  },
});

let events: Array<[string, Record<string, unknown>]>;
const warn = vi.fn();

function respond(body: string, status = 200): typeof fetch {
  return vi.fn(
    async () => new Response(body, { status }),
  ) as unknown as typeof fetch;
}

beforeEach(() => {
  resetTrackState();
  resetExposureState();
  events = [];
  vi.stubGlobal("window", {});
  vi.stubGlobal("navigator", {});
  vi.spyOn(console, "warn").mockImplementation(warn);
  const sink: TelemetrySink = {
    track: (name, attributes) => events.push([name, attributes]),
    setUser: () => {},
    disable: () => {},
  };
  setTelemetrySink(sink);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  warn.mockReset();
});

describe("loadChanges", () => {
  it("fetches /_forge/changes.json, uncached and same-origin", async () => {
    const fetchImpl = respond(SNAPSHOT);
    const store = await loadChanges({ fetch: fetchImpl });
    expect(fetchImpl).toHaveBeenCalledWith(CHANGES_SNAPSHOT_URL, {
      cache: "no-cache",
      credentials: "same-origin",
    });
    expect(CHANGES_SNAPSHOT_URL).toBe("/_forge/changes.json");
    expect(store.revision).toBe(7);
    expect(changeString(store.evaluate("all"), "red")).toBe("blue");
  });

  it("is an empty store, never a rejection, when the snapshot is missing, broken or unreachable", async () => {
    const failing = vi.fn(async () => {
      throw new TypeError("network down");
    }) as unknown as typeof fetch;
    for (const fetchImpl of [
      respond("", 404),
      respond("oops", 500),
      respond("not json"),
      respond('{"schema":"forge.changes/v9","revision":1}'),
      failing,
    ]) {
      const store = await loadChanges({ fetch: fetchImpl, url: "/c.json" });
      expect(store.revision).toBe(0);
      const r = store.evaluate("all");
      expect(r.reason).toBe("unknown");
      expect(changeString(r, "red")).toBe("red");
    }
    // A 404 is "no changes yet"; the other four are worth a warning.
    expect(warn).toHaveBeenCalledTimes(4);
    expect(events).toEqual([]);
  });

  it("is an empty store when there is no fetch at all", async () => {
    vi.stubGlobal("fetch", undefined);
    const store = await loadChanges();
    expect(store.evaluate("all").reason).toBe("unknown");
  });
});

describe("ChangesStore.evaluate", () => {
  it("records an assignment once per person, change, variant and run", async () => {
    const store = await loadChanges({ fetch: respond(SNAPSHOT) });
    const ctx = { targetingKey: "u1", attributes: { plan: "paid" } };
    for (let i = 0; i < 3; i++) {
      expect(store.evaluate("exp", ctx).reason).toBe("experiment");
    }
    expect(events).toEqual([
      [
        FLAG_EVALUATED_EVENT,
        {
          flag: "exp",
          variant: "treatment",
          provider: "forge",
          run: "r_1",
          unit: "user",
          reason: "experiment",
          revision: "7",
        },
      ],
    ]);
  });

  it.each([
    [
      "experiment",
      "exp",
      // An account attribute on a user-unit change is not recorded.
      { targetingKey: "u1", attributes: { plan: "paid", account: "a9" } },
      true,
    ],
    ["rollout", "acct", { attributes: { account: "a1" } }, true],
    ["preview", "exp", { targetingKey: "previewer" }, true],
    ["everyone", "all", {}, true],
    [
      "ineligible",
      "exp",
      { targetingKey: "u3", attributes: { plan: "free" } },
      false,
    ],
    ["no_unit", "exp", {}, false],
    ["not_enrolled", "zero", { targetingKey: "u4" }, false],
    ["default", "off", { targetingKey: "u5" }, false],
    ["invalid", "bad", { targetingKey: "u6" }, false],
    ["unknown", "missing", { targetingKey: "u7" }, false],
  ])("%s is recorded: %s", async (reason, key, ctx, recorded) => {
    const store = await loadChanges({ fetch: respond(SNAPSHOT) });
    expect(store.evaluate(key, ctx).reason).toBe(reason);
    expect(events.length).toBe(recorded ? 1 : 0);
    if (recorded && key === "acct") {
      expect(events[0]![1].unit).toBe("account");
      // The account the change hashed, which no event column carries.
      expect(events[0]![1].account).toBe("a1");
    }
    if (recorded && key === "exp") {
      expect(events[0]![1]).not.toHaveProperty("account");
    }
    if (recorded && key === "all") {
      expect(events[0]![1]).not.toHaveProperty("run");
      expect(events[0]![1]).not.toHaveProperty("unit");
    }
  });
});

describe("typed accessors", () => {
  it("read a value of the right type, else the default", async () => {
    const store = await loadChanges({ fetch: respond(SNAPSHOT) });
    const ctx = { targetingKey: "u1", attributes: { plan: "paid" } };
    expect(changeBool(store.evaluate("exp", ctx), false)).toBe(true);
    expect(changeBool(store.evaluate("all"), false)).toBe(false);
    expect(changeString(store.evaluate("exp", ctx), "x")).toBe("x");
    expect(changeNumber(store.evaluate("ratio"), 1)).toBe(0.5);
    expect(changeNumber(store.evaluate("all"), 1)).toBe(1);
    expect(changeJson(store.evaluate("limits"), { max: 1 })).toEqual({
      max: 3,
    });
    expect(changeJson(store.evaluate("missing"), { max: 1 })).toEqual({
      max: 1,
    });
  });
});
