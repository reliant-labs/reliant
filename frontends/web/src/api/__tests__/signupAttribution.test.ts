import { beforeEach, describe, expect, it } from "vitest";
import type { UnaryRequest, UnaryResponse } from "@connectrpc/connect";
import { holdLandingAttribution, readSignupAttribution } from "@/lib/landingAttribution";
import { resetSignupAttributionState, SIGNUP_ATTRIBUTION_HEADER, signupAttributionInterceptor } from "../signupAttribution";

function request(): UnaryRequest {
  return { stream: false, header: new Headers() } as unknown as UnaryRequest;
}

beforeEach(() => {
  localStorage.clear();
  resetSignupAttributionState();
});

describe("signupAttributionInterceptor", () => {
  it("sends the held handoff until a request succeeds, then drops it", async () => {
    holdLandingAttribution("?utm_source=hn&ref=news.ycombinator.com", Date.now());
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
    holdLandingAttribution("?utm_source=hn", Date.now());
    const failing = async () => {
      throw new Error("unavailable");
    };
    await expect(signupAttributionInterceptor(failing as never)(request())).rejects.toThrow("unavailable");
    expect(readSignupAttribution()).toBe("utm_source=hn");
  });

  it("never costs a request: a refused preflight is retried without the header, and it stops for the page load", async () => {
    holdLandingAttribution("?utm_source=hn", Date.now());
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
});
