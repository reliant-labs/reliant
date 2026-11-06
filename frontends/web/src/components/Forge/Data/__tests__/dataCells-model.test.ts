// Copyright (c) 2025 Reliant Labs

import { afterEach, describe, expect, it } from "vitest";

import { clearAccess, isDenied, needsConfirmation, readAccess, validReason, writeAccess } from "../dataAccess-model";
import { CELL_PREVIEW_CHARS, cellCut, cellDisplay, cutNote, isJsonType, isNumericType, shortTypeLabel } from "../dataCells-model";

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
    // Described types, shortened for a header.
    expect(shortTypeLabel("timestamp with time zone")).toBe("timestamptz");
    expect(shortTypeLabel("timestamp(3) with time zone")).toBe("timestamptz(3)");
    expect(shortTypeLabel("timestamp with time zone[]")).toBe("timestamptz[]");
    expect(shortTypeLabel("character varying(40)")).toBe("varchar(40)");
    expect(shortTypeLabel("bigint")).toBe("bigint");
    // Result types decide how a value reads.
    expect(isNumericType("numeric")).toBe(true);
    expect(isNumericType("int8")).toBe(true);
    expect(isNumericType("text")).toBe(false);
    expect(isJsonType("jsonb")).toBe(true);
  });
});

describe("a cell the server cut at 4 KiB", () => {
  const cutText = `${"x".repeat(4096)}…[truncated: 4096 of 9000 bytes shown]`;

  it("is known by forge's marker when truncated_cells is empty, and the marker is not shown as the value", () => {
    const display = cellDisplay(cutText, "text", false);
    expect(display.serverCut).toEqual({ shownBytes: 4096, totalBytes: 9000 });
    expect(display.full).toBe("x".repeat(4096));
    expect(display.cut).toBe(true);
    expect(cutNote(display.serverCut!)).toBe("Cut by the server: 4,096 of 9,000 bytes shown. The rest is not readable here.");
  });

  it("is known by truncated_cells alone, with no marker to read", () => {
    const display = cellDisplay("short", "text", true);
    expect(display.serverCut).toEqual({ shownBytes: null, totalBytes: null });
    expect(display.cut).toBe(true);
    expect(cutNote(display.serverCut!)).toBe("Cut by the server at 4 KiB: the rest of this value is not readable here.");
  });

  it("reads a cut json value as the text that arrived, not as JSON", () => {
    const display = cellDisplay(`{"a": "${"y".repeat(10)}…[truncated: 4096 of 5000 bytes shown]`, "jsonb");
    expect(display.kind).toBe("text");
    expect(display.full).toBe(`{"a": "${"y".repeat(10)}`);
  });

  it("is an ordinary cell otherwise, whatever its text says in the middle", () => {
    expect(cellDisplay("…[truncated: 1 of 2 bytes shown] and more", "text").serverCut).toBeNull();
    expect(cellCut(null, true)).toEqual({ value: null, cut: { shownBytes: null, totalBytes: null } });
    expect(cellDisplay(null, "text", true).serverCut).toBeNull();
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
