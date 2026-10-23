// The contract of visitor identity and attribution: what is captured from the
// landing URL, how an outbound link is stamped, when `user_identified` fires,
// how app.client is chosen, and that none of it runs without consent.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const RID = "0b8f3c2e-6a1d-4f7e-9c55-2d1e8a7b4c90";

class FakeStorage {
  readonly data = new Map<string, string>();
  getItem(k: string) {
    return this.data.get(k) ?? null;
  }
  setItem(k: string, v: string) {
    this.data.set(k, v);
  }
}

type Attribution = typeof import("./attribution.js");
type Track = typeof import("./track.js");

let storage: FakeStorage;

function stubWindow(extra: Record<string, unknown> = {}) {
  storage = new FakeStorage();
  vi.stubGlobal("window", { localStorage: storage, ...extra });
}

// Each load is a fresh page: module state is gone, storage persists.
async function load(): Promise<{ a: Attribution; k: Track }> {
  vi.resetModules();
  return {
    a: await import("./attribution.js"),
    k: await import("./track.js"),
  };
}

beforeEach(() => stubWindow());
afterEach(() => vi.unstubAllGlobals());

describe("parseAttribution", () => {
  it("reads rid and the five utm parameters", async () => {
    const { a } = await load();
    expect(
      a.parseAttribution(
        `?rid=${RID}&utm_source=news&utm_medium=email&utm_campaign=launch&utm_term=t&utm_content=c&other=x`,
      ),
    ).toEqual({
      rid: RID,
      utm_source: "news",
      utm_medium: "email",
      utm_campaign: "launch",
      utm_term: "t",
      utm_content: "c",
    });
  });

  it("ignores a malformed rid, blank values, and bounds the rest", async () => {
    const { a } = await load();
    expect(a.parseAttribution("?rid=<script>&utm_source=%20")).toEqual({});
    const long = "x".repeat(500);
    expect(
      a.parseAttribution(`?utm_campaign=${long}`).utm_campaign,
    ).toHaveLength(a.MAX_ATTRIBUTION_VALUE_CHARS);
  });
});

describe("captureAttribution", () => {
  it("stores rid and utm from the URL and adopts rid as the visitor id", async () => {
    const { a } = await load();
    const id = a.captureAttribution({
      search: `?rid=${RID}&utm_source=news`,
    });
    expect(id).toBe(RID);
    expect(a.getAttribution()).toEqual({ rid: RID, utm_source: "news" });
    expect(JSON.parse(storage.data.get(a.ATTRIBUTION_STORAGE_KEY)!)).toEqual({
      rid: RID,
      utm_source: "news",
    });
  });

  it("keeps what was stored when a later visit carries nothing, and replaces it when one does", async () => {
    const first = await load();
    first.a.captureAttribution({ search: `?rid=${RID}&utm_source=news` });

    const second = await load();
    second.a.captureAttribution({ search: "" });
    expect(second.a.getAttribution()).toEqual({
      rid: RID,
      utm_source: "news",
    });

    const third = await load();
    third.a.captureAttribution({ search: "?utm_source=ads" });
    expect(third.a.getAttribution()).toEqual({ utm_source: "ads" });
  });

  it("mints a stable anonymous visitor id when there is no rid", async () => {
    const first = await load();
    const id = first.a.captureAttribution({ search: "" });
    expect(id).toMatch(/^[A-Za-z0-9_-]{8,64}$/);

    const second = await load();
    expect(second.a.captureAttribution({ search: "" })).toBe(id);
  });

  it("adopts the visitor id an app already keeps under visitorIdStorageKey", async () => {
    const { a } = await load();
    storage.setItem("reliant.visitor_id", "landing-visitor-1");
    expect(
      a.captureAttribution({
        search: "",
        visitorIdStorageKey: "reliant.visitor_id",
      }),
    ).toBe("landing-visitor-1");
  });

  it("stores nothing after disableTelemetry", async () => {
    const { a, k } = await load();
    k.disableTelemetry();
    expect(a.captureAttribution({ search: `?rid=${RID}` })).toBe("");
    expect(storage.data.size).toBe(0);
  });

  it("survives blocked storage", async () => {
    vi.stubGlobal("window", {
      get localStorage(): never {
        throw new Error("blocked");
      },
    });
    const { a } = await load();
    expect(() =>
      a.captureAttribution({ search: `?rid=${RID}&utm_source=s` }),
    ).not.toThrow();
    expect(a.getAttribution()).toEqual({ rid: RID, utm_source: "s" });
  });
});

describe("withAttribution", () => {
  it("appends rid and captured utm, keeping existing parameters", async () => {
    const { a } = await load();
    a.captureAttribution({ search: `?rid=${RID}&utm_source=news&utm_term=t` });

    const out = new URL(a.withAttribution("https://app.example.com/x?a=1"));
    expect(out.searchParams.get("a")).toBe("1");
    expect(out.searchParams.get("rid")).toBe(RID);
    expect(out.searchParams.get("utm_source")).toBe("news");
    expect(out.searchParams.get("utm_term")).toBe("t");
    expect(out.searchParams.has("utm_medium")).toBe(false);
  });

  it("does not overwrite a parameter the URL already carries", async () => {
    const { a } = await load();
    a.captureAttribution({ search: `?rid=${RID}&utm_source=news` });
    const out = new URL(
      a.withAttribution("https://app.example.com/?utm_source=pinned"),
    );
    expect(out.searchParams.get("utm_source")).toBe("pinned");
  });

  it("leaves relative, non-http and unparsable URLs alone", async () => {
    const { a } = await load();
    a.captureAttribution({ search: `?rid=${RID}` });
    for (const u of ["/docs", "mailto:a@b.c", "not a url", ""]) {
      expect(a.withAttribution(u)).toBe(u);
    }
  });

  it("is the identity before telemetry starts and after it is disabled", async () => {
    const { a, k } = await load();
    const url = "https://app.example.com/";
    expect(a.withAttribution(url)).toBe(url);

    a.captureAttribution({ search: `?rid=${RID}` });
    expect(a.withAttribution(url)).not.toBe(url);

    k.disableTelemetry();
    expect(a.withAttribution(url)).toBe(url);
  });
});

describe("takeIdentifyProps", () => {
  it("returns visitor_id and utm once per user, and again for a different user", async () => {
    const { a } = await load();
    a.captureAttribution({ search: `?rid=${RID}&utm_campaign=launch` });

    expect(a.takeIdentifyProps("user-1")).toEqual({
      visitor_id: RID,
      utm_campaign: "launch",
    });
    expect(a.takeIdentifyProps("user-1")).toBeNull();
    expect(a.takeIdentifyProps("user-2")).not.toBeNull();
  });

  it("does not repeat across page loads for the same user", async () => {
    const first = await load();
    first.a.captureAttribution({ search: `?rid=${RID}` });
    expect(first.a.takeIdentifyProps("user-1")).not.toBeNull();

    const second = await load();
    second.a.captureAttribution({ search: "" });
    expect(second.a.takeIdentifyProps("user-1")).toBeNull();
  });

  it("never identifies an empty user, or without attribution running", async () => {
    const { a } = await load();
    expect(a.takeIdentifyProps("user-1")).toBeNull();
    a.captureAttribution({ search: "" });
    expect(a.takeIdentifyProps("")).toBeNull();
  });
});

describe("detectAppClient", () => {
  it("honours an explicit value", async () => {
    const { a } = await load();
    expect(a.detectAppClient("landing")).toBe("landing");
  });

  it("is web by default and electron inside Electron", async () => {
    const { a } = await load();
    expect(a.detectAppClient()).toBe("web");

    stubWindow({ process: { versions: { electron: "31.0.0" } } });
    expect(a.detectAppClient()).toBe("electron");

    stubWindow();
    vi.stubGlobal("navigator", {
      userAgent: "Mozilla/5.0 Electron/31.0.0 Safari",
    });
    expect(a.detectAppClient()).toBe("electron");
  });
});
