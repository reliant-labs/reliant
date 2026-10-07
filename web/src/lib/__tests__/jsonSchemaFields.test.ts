import { describe, expect, it } from "vitest";

import { actionParamFields } from "../jsonSchemaFields";
import type { JsonSchema } from "../jsonSchema";

// Slack's Post message, as GetCatalogEntry serves it: a Struct's keys come
// back sorted, so the declared order travels separately as param_order.
const postMessage: JsonSchema = {
  type: "object",
  required: ["channel"],
  properties: {
    blocks: { type: "array", items: { type: "object" }, title: "Blocks", description: "Block Kit layout blocks.", examples: [[{ type: "section" }]] },
    channel: { type: "string", title: "Channel ID", description: "The conversation's ID, not its #name.", examples: ["C0123ABCDEF"] },
    connection: { type: "string" },
    reply_broadcast: { type: "boolean", title: "Also send to channel", description: "Also show the reply in the channel." },
    text: { type: "string", title: "Message text", description: "The message in Slack mrkdwn.", examples: ["Deploy finished"] },
    thread_ts: { type: "string", title: "Thread timestamp", description: "The parent message's ts.", examples: ["1712345678.123456"] },
  },
};
const declared = ["channel", "text", "blocks", "thread_ts", "reply_broadcast", "connection"];

describe("actionParamFields", () => {
  it("lists required params first, then the manifest's declared order, never alphabetically", () => {
    expect(actionParamFields(postMessage, declared).map((f) => f.name)).toEqual(["channel", "text", "blocks", "thread_ts", "reply_broadcast"]);
    // Without a declared order, required still leads and the rest keep the schema's order.
    expect(actionParamFields(postMessage).map((f) => f.name)[0]).toBe("channel");
  });

  it("labels with the title and carries the description, example and type for the form", () => {
    const fields = Object.fromEntries(actionParamFields(postMessage, declared).map((f) => [f.name, f.schema]));
    expect(fields.channel).toMatchObject({
      label: "Channel ID *",
      description: "The conversation's ID, not its #name.",
      example: "C0123ABCDEF",
    });
    expect(fields.channel.helpText).toBeUndefined();
    expect(fields.blocks).toMatchObject({ example: '[{"type":"section"}]', typeHint: "JSON list" });
    expect(fields.text.typeHint).toBeUndefined();
  });

  it("writes a list example the way the comma-separated input takes it", () => {
    const [field] = actionParamFields({
      type: "object",
      properties: { labels: { type: "array", items: { type: "string" }, examples: [["bug", "needs-triage"]] } },
    });
    expect(field.schema).toMatchObject({ example: "bug, needs-triage", typeHint: "list, comma-separated" });
  });

  it("offers no empty option beside a default: unset is the default, shown selected", () => {
    const [withDefault, withoutDefault] = actionParamFields({
      type: "object",
      properties: {
        response_format: { type: "string", enum: ["auto", "json", "text"], default: "auto" },
        state: { type: "string", enum: ["open", "closed"] },
      },
    });
    expect(withDefault.schema).toMatchObject({ allowEmptyOption: false, defaultValue: "auto", helpText: 'Default: "auto"' });
    expect(withoutDefault.schema).toMatchObject({ allowEmptyOption: true, emptyOptionLabel: "None" });
  });

  it("names a formatted string's kind", () => {
    const [field] = actionParamFields({ type: "object", properties: { status_callback: { type: "string", format: "uri" } } });
    expect(field.schema.typeHint).toBe("URL");
  });
});
