import { describe, expect, it } from "vitest";

import { serviceAccountKeyError, vertexErrorMessage } from "../serviceAccountKey";

// Not a real key: the check only needs the fields present.
const KEY = {
  type: "service_account",
  project_id: "acme-prod",
  private_key_id: "0123456789abcdef",
  private_key: "fake-private-key-material",
  client_email: "reliant@acme-prod.iam.gserviceaccount.com",
  client_id: "1234567890",
  token_uri: "https://oauth2.googleapis.com/token",
};

const paste = (value: unknown) => JSON.stringify(value, null, 2);

describe("serviceAccountKeyError", () => {
  it("accepts a whole service-account key file, multi-line and padded", () => {
    expect(serviceAccountKeyError(paste(KEY))).toBeNull();
    expect(serviceAccountKeyError(`\n  ${paste(KEY)}\n\n`)).toBeNull();
  });

  it("asks for the file when nothing was pasted", () => {
    expect(serviceAccountKeyError("   ")).toMatch(/paste the contents/i);
  });

  it("names half a file as not JSON", () => {
    expect(serviceAccountKeyError(paste(KEY).slice(0, 60))).toMatch(/not valid JSON/);
  });

  it("refuses JSON that is not an object", () => {
    expect(serviceAccountKeyError("[]")).toMatch(/not a key file/);
    expect(serviceAccountKeyError('"AIza-something"')).toMatch(/not a key file/);
  });

  it("names the other Google credential files the server refuses", () => {
    expect(serviceAccountKeyError(paste({ type: "authorized_user", client_id: "x" }))).toMatch(/gcloud user login/);
    expect(serviceAccountKeyError(paste({ type: "external_account" }))).toMatch(/workload identity/);
    expect(serviceAccountKeyError(paste({ ...KEY, type: undefined }))).toMatch(/"type" is "service_account"/);
  });

  it("lists every missing field", () => {
    const { project_id: _p, private_key: _k, ...rest } = KEY;
    expect(serviceAccountKeyError(paste(rest))).toBe(
      "The key file has no project_id, private_key. Download a new JSON key and paste the whole file.",
    );
    expect(serviceAccountKeyError(paste({ ...KEY, client_email: " " }))).toMatch(/no client_email/);
  });

  it("never quotes the key back", () => {
    const cases = [
      paste({ ...KEY, type: "authorized_user" }),
      paste({ ...KEY, project_id: "" }),
      paste(KEY).slice(0, 120),
    ];
    for (const text of cases) {
      const message = serviceAccountKeyError(text) ?? "";
      expect(message).not.toContain(KEY.private_key);
      expect(message).not.toContain(KEY.client_email);
    }
  });
});

describe("vertexErrorMessage", () => {
  it("shows the server's sentence without the Connect code", () => {
    expect(vertexErrorMessage("[invalid_argument] vertex ai key: only service_account keys are accepted")).toBe(
      "vertex ai key: only service_account keys are accepted",
    );
  });

  it("falls back to the role hint when the server said nothing", () => {
    expect(vertexErrorMessage("[invalid_argument] ")).toMatch(/Vertex AI User/);
  });
});
