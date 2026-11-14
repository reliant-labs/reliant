/**
 * An adopted automation's inputs are sent as the type the adopter chose: an
 * input is not always a string, and "3" for a number would fail the
 * workflow's input check. Blank inputs are left out; a value that does not
 * parse as its type is an error and is not sent.
 */
import { describe, expect, it } from "vitest";
import { toJson } from "@bufbuild/protobuf";
import { ValueSchema } from "@bufbuild/protobuf/wkt";

import { encodeParams } from "../AdoptAutomationDialog";

const json = (v: ReturnType<typeof encodeParams>["values"]) =>
  Object.fromEntries(Object.entries(v).map(([k, x]) => [k, toJson(ValueSchema, x)]));

describe("encodeParams", () => {
  it("sends each input as the type it was given", () => {
    const { values, errors } = encodeParams({
      channel: { kind: "text", text: "#ops" },
      count: { kind: "number", text: "3" },
      dry_run: { kind: "boolean", text: "false" },
      repos: { kind: "json", text: '["acme/app", "acme/api"]' },
      limits: { kind: "json", text: '{"max": 5}' },
      ids: { kind: "text", text: "007" },
    });
    expect(errors).toEqual({});
    expect(json(values)).toEqual({
      channel: "#ops",
      count: 3,
      dry_run: false,
      repos: ["acme/app", "acme/api"],
      limits: { max: 5 },
      ids: "007",
    });
  });

  it("leaves blank inputs out and refuses values that are not their type", () => {
    const { values, errors } = encodeParams({
      blank: { kind: "number", text: "  " },
      count: { kind: "number", text: "three" },
      flag: { kind: "boolean", text: "maybe" },
      repos: { kind: "json", text: "[acme/app" },
    });
    expect(values).toEqual({});
    expect(Object.keys(errors).sort()).toEqual(["count", "flag", "repos"]);
  });
});
