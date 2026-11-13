import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { UnaryRequest, UnaryResponse } from "@connectrpc/connect";
import { holdLandingAttribution, readSignupAttribution } from "@/lib/landingAttribution";
import { resetSignupAttributionState, SIGNUP_ATTRIBUTION_HEADER, signupAttributionInterceptor } from "../signupAttribution";

function request(): UnaryRequest {
  return { stream: false, header: new Headers() } as unknown as UnaryRequest;
}

// A fixed clock: the hold's age is measured from it, never from the wall clock.
const NOW = Date.UTC(2026, 10, 13, 12, 0, 0);

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
  localStorage.clear();
  resetSignupAttributionState();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("signupAttributionInterceptor", () => {
  it("sends the held handoff until a request succeeds, then drops it", async () => {
    holdLandingAttribution("?utm_source=hn&ref=news.ycombinator.com", NOW);
    const seen: (string | null)[] = [];
    const next = async (req: UnaryRequest) => {
      seen.push(req.header.get(SIGNUP_ATTRIBUTION_HEADER));
      return {} as UnaryResponse;
    };
    await signupAttributionInterceptor(next as never)(request());
    await signupAttributionInterceptor(next as never)(request());
    expect(seen).toEqual(["utm_source=hn&ref=news.ycombinator.com", null]);
    expect(readSignupAttribution()).toBeUndefined();
  });

  it("keeps it when the server fails the request, for the next one", async () => {
    holdLandingAttribution("?utm_source=hn", NOW);
    const failing = async () => {
      throw new Error("unavailable");
    };
    await expect(signupAttributionInterceptor(failing as never)(request())).rejects.toThrow("unavailable");
    expect(readSignupAttribution()).toBe("utm_source=hn");
  });

  it("never costs a request: a refused preflight is retried without the header, and it stops for the page load", async () => {
    holdLandingAttribution("?utm_source=hn", NOW);
    const seen: (string | null)[] = [];
    const corsRefusing = async (req: UnaryRequest) => {
      seen.push(req.header.get(SIGNUP_ATTRIBUTION_HEADER));
      if (req.header.has(SIGNUP_ATTRIBUTION_HEADER)) throw new TypeError("Failed to fetch");
      return {} as UnaryResponse;
    };
    await signupAttributionInterceptor(corsRefusing as never)(request());
    await signupAttributionInterceptor(corsRefusing as never)(request());
    expect(seen).toEqual(["utm_source=hn", null, null]);
    expect(readSignupAttribution()).toBe("utm_source=hn"); // kept for the next page load
  });

  it("leaves a reused request header-free after two failed attempts", async () => {
    holdLandingAttribution("?utm_source=hn", NOW);
    const req = request();
    const seen: (string | null)[] = [];
    const offline = async (r: UnaryRequest) => {
      seen.push(r.header.get(SIGNUP_ATTRIBUTION_HEADER));
      throw new TypeError("Failed to fetch");
    };
    // The header-bearing attempt and its header-free retry both fail.
    await expect(signupAttributionInterceptor(offline as never)(req)).rejects.toThrow("Failed to fetch");
    expect(req.header.has(SIGNUP_ATTRIBUTION_HEADER)).toBe(false);
    // A later call reusing the same request object, even with the header put
    // back on it by an earlier attempt, goes out without it.
    req.header.set(SIGNUP_ATTRIBUTION_HEADER, "utm_source=hn");
    const ok = async (r: UnaryRequest) => {
      seen.push(r.header.get(SIGNUP_ATTRIBUTION_HEADER));
      return {} as UnaryResponse;
    };
    await signupAttributionInterceptor(ok as never)(req);
    expect(seen).toEqual(["utm_source=hn", null, null]);
    expect(req.header.has(SIGNUP_ATTRIBUTION_HEADER)).toBe(false);
  });
});
