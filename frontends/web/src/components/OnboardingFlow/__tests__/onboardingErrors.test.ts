/**
 * Classifying a failed key validation into an opaque code and a sentence.
 *
 * The cases are the provider bodies keyvalidate.Validate passes through as
 * "Validation failed: <driver error>". The first is verbatim (minus the URL's
 * remedy hint) from a 2026-11-12 desktop sign-up that stopped on this step: a
 * valid OpenRouter key on an account with no credits, which the user was shown
 * as raw JSON.
 */
import { describe, expect, it } from "vitest";
import { describeKeyError } from "../onboardingErrors";

const OPENROUTER_402 =
  'Validation failed: POST "https://openrouter.ai/api/v1/responses": 402 Payment Required ' +
  '{"message":"Insufficient credits. This account never purchased credits. Make sure your key is on the correct account or org, and if so, purchase more at https://openrouter.ai/settings/credits","code":402}';

describe("describeKeyError", () => {
  it("an unfunded OpenRouter key is no-credit, not the raw body", () => {
    const failure = describeKeyError(OPENROUTER_402, "openrouter");
    expect(failure.code).toBe("key_no_credits");
    expect(failure.message).not.toContain("{");
    expect(failure.message).toMatch(/no credit/i);
  });

  it("Anthropic's low-balance 400 is no-credit even though it says invalid_request_error", () => {
    const failure = describeKeyError(
      'Validation failed: 400 Bad Request {"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}',
      "anthropic",
    );
    expect(failure.code).toBe("key_no_credits");
  });

  it("OpenAI's insufficient_quota 429 is no-credit, not a rate limit", () => {
    const failure = describeKeyError(
      'Validation failed: POST "https://api.openai.com/v1/responses": 429 Too Many Requests {"error":{"code":"insufficient_quota"}}',
      "openai",
    );
    expect(failure.code).toBe("key_no_credits");
  });

  it("the server's own verdict is an invalid key", () => {
    expect(describeKeyError("Invalid API key", "anthropic").code).toBe("key_invalid");
  });

  it("a plain 429 is a rate limit", () => {
    expect(
      describeKeyError("Validation failed: 429 Too Many Requests: rate limit reached", "openai").code,
    ).toBe("key_rate_limited");
  });

  it("OpenRouter's data-policy refusal keeps its own code", () => {
    expect(
      describeKeyError("No endpoints found matching your data policy", "openrouter").code,
    ).toBe("key_data_policy");
  });

  it("anything else keeps the provider's text on screen under a generic code", () => {
    const failure = describeKeyError("Validation failed: dial tcp: i/o timeout", "openai");
    expect(failure).toEqual({
      code: "key_validation_failed",
      message: "Validation failed: dial tcp: i/o timeout",
    });
  });
});
