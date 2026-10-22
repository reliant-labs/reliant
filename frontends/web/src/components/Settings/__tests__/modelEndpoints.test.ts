import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ModelEndpointModelSchema, ModelEndpointRoute, ModelEndpointSchema } from "../../../gen/reliant/v1/model_endpoint_pb";
import {
  draftToInput,
  emptyDraft,
  emptyModelDraft,
  endpointToDraft,
  formatLatency,
  mergeProbeIntoDraft,
  validateDraft,
  validateExtraBodyJson,
  validateModelDraft,
} from "../modelEndpoints";

describe("validateExtraBodyJson", () => {
  it("accepts empty and plain objects", () => {
    expect(validateExtraBodyJson("")).toBeNull();
    expect(validateExtraBodyJson("   ")).toBeNull();
    expect(validateExtraBodyJson("{}")).toBeNull();
    expect(validateExtraBodyJson('{"min_p": 0.05, "chat_template_kwargs": {"enable_thinking": false}}')).toBeNull();
    expect(validateExtraBodyJson('{"nested": {"model": "fine"}}')).toBeNull();
  });
  it.each([
    ["{", /valid JSON/],
    ["[1]", /JSON object/],
    ['"x"', /JSON object/],
    ["null", /JSON object/],
    ["5", /JSON object/],
  ])("rejects %s", (raw, message) => {
    expect(validateExtraBodyJson(raw)).toMatch(message);
  });
  it.each(["model", "messages", "tools", "stream"])("rejects the reserved key %s", (key) => {
    expect(validateExtraBodyJson(`{"${key}": 1}`)).toContain(key);
  });
});

describe("validateModelDraft", () => {
  const base = emptyModelDraft("m");
  it("passes defaults", () => expect(validateModelDraft(base)).toEqual({}));
  it("checks ranges", () => {
    expect(validateModelDraft({ ...base, temperature: "3" }).temperature).toBeTruthy();
    expect(validateModelDraft({ ...base, temperature: "0" }).temperature).toBeUndefined();
    expect(validateModelDraft({ ...base, topP: "0" }).topP).toBeTruthy();
    expect(validateModelDraft({ ...base, topP: "1" }).topP).toBeUndefined();
    expect(validateModelDraft({ ...base, contextWindow: "12.5" }).contextWindow).toBeTruthy();
    expect(validateModelDraft({ ...base, contextWindow: "abc" }).contextWindow).toBeTruthy();
    expect(validateModelDraft({ ...base, contextWindow: "1000", maxOutputTokens: "2000" }).maxOutputTokens).toMatch(/exceed/);
    expect(validateModelDraft({ ...base, contextWindow: "4000", maxOutputTokens: "2000" })).toEqual({});
    expect(validateModelDraft({ ...base, extraBodyJson: "{" }).extraBodyJson).toBeTruthy();
  });
});

describe("validateDraft", () => {
  const ok = { ...emptyDraft(), name: "Lab", baseUrl: "https://llm.example.com/v1" };
  it("accepts a minimal direct endpoint", () => expect(validateDraft(ok)).toEqual({ ok: true }));
  it.each([
    [{ name: "" }, "name"],
    [{ baseUrl: "" }, "baseUrl"],
    [{ baseUrl: "not a url" }, "baseUrl"],
    [{ baseUrl: "ftp://x.example.com" }, "baseUrl"],
    [{ baseUrl: "https://user:pw@x.example.com/v1" }, "baseUrl"],
    [{ route: "via_daemon" as const }, "daemonId"],
  ])("rejects %j", (over, field) => {
    const result = validateDraft({ ...ok, ...over });
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.field).toBe(field);
  });
  it("requires a machine only for the via-machine route", () => {
    expect(validateDraft({ ...ok, route: "via_daemon", daemonId: "d-1" })).toEqual({ ok: true });
  });
  it("surfaces a bad model setting", () => {
    const result = validateDraft({ ...ok, models: [{ ...emptyModelDraft("m"), temperature: "9" }] });
    expect(result).toMatchObject({ ok: false, field: "models" });
  });
});

describe("draftToInput", () => {
  it("maps routes and omits the daemon for direct", () => {
    const direct = draftToInput({ ...emptyDraft(), name: " Lab ", baseUrl: " https://x.example.com/v1 ", daemonId: "stale" }, { includeKey: false });
    expect(direct.name).toBe("Lab");
    expect(direct.baseUrl).toBe("https://x.example.com/v1");
    expect(direct.route).toBe(ModelEndpointRoute.DIRECT);
    expect(direct.daemonId).toBe("");

    const via = draftToInput({ ...emptyDraft(), name: "H", baseUrl: "http://10.0.0.5:8000/v1", route: "via_daemon", daemonId: "d-1" }, { includeKey: false });
    expect(via.route).toBe(ModelEndpointRoute.VIA_DAEMON);
    expect(via.daemonId).toBe("d-1");
  });

  it("never sends an API key while the sealed store is unavailable", () => {
    const d = { ...emptyDraft(), name: "k", baseUrl: "https://x.example.com/v1", apiKey: "sk-secret" };
    expect(draftToInput(d, { includeKey: false }).apiKey).toBeUndefined();
    expect(draftToInput(d, { includeKey: true }).apiKey).toBe("sk-secret");
    expect(draftToInput({ ...d, apiKey: "" }, { includeKey: true }).apiKey).toBeUndefined();
  });

  it("sends only customised models, with tri-state overrides as optionals", () => {
    const d = {
      ...emptyDraft(),
      name: "L",
      baseUrl: "https://x.example.com/v1",
      models: [
        emptyModelDraft("untouched"),
        { ...emptyModelDraft("tuned"), contextWindow: "65536", maxOutputTokens: "4096", tools: "off" as const, vision: "on" as const, temperature: "0.2", topP: "0.9", extraBodyJson: ' {"min_p":0.05} ' },
        { ...emptyModelDraft("gone"), hidden: true },
      ],
    };
    const { models } = draftToInput(d, { includeKey: false });
    expect(models.map((m) => m.name)).toEqual(["tuned", "gone"]);
    const tuned = models[0];
    expect(tuned.contextWindow).toBe(65536n);
    expect(tuned.maxOutputTokens).toBe(4096n);
    expect(tuned.supportsTools).toBe(false);
    expect(tuned.supportsVision).toBe(true);
    expect(tuned.supportsThinking).toBeUndefined();
    expect(tuned.temperature).toBe(0.2);
    expect(tuned.topP).toBe(0.9);
    expect(tuned.extraBodyJson).toBe('{"min_p":0.05}');
    expect(models[1].hidden).toBe(true);
  });

  it("a temperature of 0 is a real value, not 'unset'", () => {
    const d = { ...emptyDraft(), name: "L", baseUrl: "https://x.example.com/v1", models: [{ ...emptyModelDraft("m"), temperature: "0" }] };
    expect(draftToInput(d, { includeKey: false }).models[0].temperature).toBe(0);
  });
});

describe("endpointToDraft / mergeProbeIntoDraft", () => {
  it("round-trips a stored endpoint", () => {
    const ep = create(ModelEndpointSchema, {
      id: "e1", name: "Lab", baseUrl: "https://x.example.com/v1", route: ModelEndpointRoute.VIA_DAEMON, daemonId: "d-1",
      models: [create(ModelEndpointModelSchema, { name: "m", hidden: true, contextWindow: 8192n, supportsTools: false, temperature: 0.5 })],
    });
    const draft = endpointToDraft(ep);
    expect(draft).toMatchObject({ name: "Lab", route: "via_daemon", daemonId: "d-1", apiKey: "" });
    expect(draft.models[0]).toMatchObject({ name: "m", hidden: true, contextWindow: "8192", tools: "off", vision: "auto", temperature: "0.5" });
  });
  it("adds newly discovered models without losing edits", () => {
    const edited = { ...emptyModelDraft("b"), temperature: "0.1" };
    const merged = mergeProbeIntoDraft([edited], ["b", "a"]);
    expect(merged.map((m) => m.name)).toEqual(["a", "b"]);
    expect(merged[1].temperature).toBe("0.1");
  });
});

describe("formatLatency", () => {
  it("formats", () => {
    expect(formatLatency(42n)).toBe("42 ms");
    expect(formatLatency(1500)).toBe("1.5 s");
  });
});
