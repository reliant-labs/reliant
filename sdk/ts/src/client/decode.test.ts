import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { UserUpdateDataSchema } from "../gen/reliant/v1/streaming_pb.js";
import { decodeUserUpdate } from "./decode.js";

describe("decodeUserUpdate", () => {
  it("carries the org the stream names for an update", () => {
    const update = decodeUserUpdate(create(UserUpdateDataSchema, { id: "u1", chatId: "c1", orgId: "acme", dataJson: "{}" }));
    expect(update.org_id).toBe("acme");
    expect(update.chat_id).toBe("c1");
  });

  it("names no org for an update about none, or from a server that predates the field", () => {
    const update = decodeUserUpdate(create(UserUpdateDataSchema, { id: "u2", dataJson: "{}" }));
    expect(update).not.toHaveProperty("org_id");
  });
});
