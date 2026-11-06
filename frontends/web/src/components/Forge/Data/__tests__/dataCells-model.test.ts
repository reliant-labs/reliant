// Copyright (c) 2025 Reliant Labs

import { afterEach, describe, expect, it } from "vitest";

import { clearAccess, isDenied, needsConfirmation, readAccess, validReason, writeAccess } from "../dataAccess-model";
import { CELL_PREVIEW_CHARS, cellDisplay, isNumericType, typeLabel } from "../dataCells-model";

describe("cellDisplay", () => {
  it("keeps NULL, the empty string and the string 'null' apart", () => {
    expect(cellDisplay(null)).toMatchObject({ kind: "null", short: "NULL" });
    expect(cellDisplay("")).toMatchObject({ kind: "text", short: "", cut: false });
    expect(cellDisplay("null")).toMatchObject({ kind: "text", short: "null" });
  });

  it("cuts a long value to one line and keeps the whole for the popover", () => {
    const long = "x".repeat(CELL_PREVIEW_CHARS + 30);
    const display = cellDisplay(long, "text");
    expect(display.short).toHaveLength(CELL_PREVIEW_CHARS + 1);
    expect(display.short.endsWith("…")).toBe(true);
    expect(display.full).toBe(long);
    expect(display.cut).toBe(true);
    // A multi-line value reads as one line, and offers the rest.
    expect(cellDisplay("a\nb", "text")).toMatchObject({ short: "a b", full: "a\nb", cut: true });
  });

  it("pretty-prints json, whether it arrives as a value or as text", () => {
    expect(cellDisplay({ plan: "team", flags: { beta: true } }, "jsonb")).toMatchObject({
      kind: "json",
      short: '{"plan":"team","flags":{"beta":true}}',
      full: '{\n  "plan": "team",\n  "flags": {\n    "beta": true\n  }\n}',
      cut: true,
    });
    expect(cellDisplay('{"a":1}', "json")).toMatchObject({ kind: "json", full: '{\n  "a": 1\n}' });
    // Text that merely looks like JSON in a text column is text.
    expect(cellDisplay('{"a":1}', "text")).toMatchObject({ kind: "text", full: '{"a":1}' });
  });

  it("shows numbers and booleans as themselves", () => {
    expect(cellDisplay(42)).toMatchObject({ kind: "number", short: "42" });
    expect(cellDisplay(false)).toMatchObject({ kind: "bool", short: "false" });
  });

  it("names types for a header", () => {
    expect(typeLabel("_text")).toBe("text[]");
    expect(typeLabel("int8")).toBe("int8");
    expect(isNumericType("numeric")).toBe(true);
    expect(isNumericType("text")).toBe(false);
  });
});

describe("the audited confirmation", () => {
  afterEach(() => window.sessionStorage.clear());

  it("is kept per environment for the tab session", () => {
    expect(readAccess("env-prod")).toBeNull();
    expect(writeAccess("env-prod", "  investigating #4412  ")).toEqual({ confirmEnvId: "env-prod", reason: "investigating #4412" });
    expect(readAccess("env-prod")).toEqual({ confirmEnvId: "env-prod", reason: "investigating #4412" });
    expect(readAccess("env-staging")).toBeNull();
    clearAccess("env-prod");
    expect(readAccess("env-prod")).toBeNull();
  });

  it("ignores what it did not write", () => {
    window.sessionStorage.setItem("forge.data.access.env-prod", "not json");
    expect(readAccess("env-prod")).toBeNull();
    window.sessionStorage.setItem("forge.data.access.env-prod", JSON.stringify({ reason: "   " }));
    expect(readAccess("env-prod")).toBeNull();
  });

  it("asks only under audited, and refuses under deny or an unrecorded policy", () => {
    expect(needsConfirmation("audited")).toBe(true);
    expect(needsConfirmation("open")).toBe(false);
    expect(isDenied("deny")).toBe(true);
    expect(isDenied("unknown")).toBe(true);
    expect(isDenied("audited")).toBe(false);
  });

  it("takes a reason the server will take", () => {
    expect(validReason("  why ")).toBe("why");
    expect(validReason("   ")).toBeNull();
    expect(validReason("x".repeat(501))).toBeNull();
  });
});
