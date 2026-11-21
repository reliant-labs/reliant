/**
 * Vertex AI is an API-key provider whose credential is a service-account key
 * file: it is offered personally (Settings › Providers) and to an org, and the
 * server's refusal is shown as the server said it.
 */

import { describe, expect, it } from "vitest";

import { ORG_KEY_PROVIDERS } from "../../../../types/orgProviderAccount";
import {
  VISIBLE_PROVIDERS,
  accountNoun,
  addAnotherLabel,
  parseErrorMessage,
  providerConfig,
  usesServiceAccountKey,
} from "../providerCatalog";

describe("the Vertex AI catalog entry", () => {
  it("is an API-key provider with no sign-in", () => {
    expect(providerConfig("vertexai")).toMatchObject({
      name: "Vertex AI",
      connect: "api_key",
      usesOAuth: false,
      credential: "service_account_json",
    });
    expect(accountNoun("vertexai")).toBe("key");
    expect(addAnotherLabel("vertexai")).toBe("Add another Vertex AI key");
  });

  it("is offered personally and to an org", () => {
    expect(VISIBLE_PROVIDERS).toContain("vertexai");
    expect(ORG_KEY_PROVIDERS.map((p) => p.id)).toContain("vertexai");
  });

  it("is the only provider that takes a key file", () => {
    expect(usesServiceAccountKey("vertexai")).toBe(true);
    for (const id of VISIBLE_PROVIDERS.filter((p) => p !== "vertexai")) {
      expect(usesServiceAccountKey(id)).toBe(false);
    }
    expect(usesServiceAccountKey("acme")).toBe(false);
  });

  it("shows the server's sentence rather than a generic 'key not accepted'", () => {
    const server = "[invalid_argument] vertex ai key: invalid credential type \"authorized_user\"";
    expect(parseErrorMessage(server, "vertexai")).toBe('vertex ai key: invalid credential type "authorized_user"');
    // Other providers keep their copy.
    expect(parseErrorMessage("invalid key", "openai")).toBe("That key was not accepted. Check it and try again.");
  });
});
