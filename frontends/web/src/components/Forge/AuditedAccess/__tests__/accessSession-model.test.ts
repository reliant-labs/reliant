// Copyright (c) 2025 Reliant Labs

/**
 * Confirmations are isolated per surface, environment and authenticated reader
 * epoch. Legacy environment-only reasons are discarded, never migrated.
 */

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { getReaderSession, setReaderPrincipal } from "../readerSession";
beforeEach(() => setReaderPrincipal("reader-A", true));

import { accessStorageKey, clearAccess, isDenied, needsConfirmation, readAccess, REASON_MAX, validReason, writeAccess } from "../accessSession-model";

afterEach(() => window.sessionStorage.clear());

describe("the kept confirmation", () => {
  it("is kept per surface and environment, trimmed and bounded", () => {
    expect(writeAccess("storage", "env-prod", "  checking an avatar  ")).toEqual({ confirmEnvId: "env-prod", reason: "checking an avatar" });
    expect(readAccess("storage", "env-prod")).toEqual({ confirmEnvId: "env-prod", reason: "checking an avatar" });
    // A reason for reading files is not one for reading rows, nor for another environment.
    expect(readAccess("data", "env-prod")).toBeNull();
    expect(readAccess("storage", "env-staging")).toBeNull();
    expect(writeAccess("data", "env-prod", "x".repeat(REASON_MAX + 20)).reason).toHaveLength(REASON_MAX);
  });

  it("discards legacy authority and isolates both namespaces by reader epoch", () => {
    window.sessionStorage.setItem("forge.data.access.env-prod", JSON.stringify({ reason: "legacy" }));
    expect(readAccess("data", "env-prod")).toBeNull();
    const old = getReaderSession();
    writeAccess("data", "env-prod", "A rows");
    writeAccess("storage", "env-prod", "A files");
    expect(accessStorageKey("data", "env-prod")).toContain("reader-A");
    setReaderPrincipal("reader-B");
    expect(readAccess("data", "env-prod")).toBeNull();
    expect(readAccess("storage", "env-prod")).toBeNull();
    expect(() => writeAccess("data", "env-prod", "late A", old)).toThrow();
    setReaderPrincipal(null);
    expect(() => writeAccess("storage", "env-prod", "signed out")).toThrow();
  });

  it("is cleared, and reads a damaged value as none", () => {
    writeAccess("storage", "env-prod", "why");
    clearAccess("storage", "env-prod");
    expect(readAccess("storage", "env-prod")).toBeNull();
    window.sessionStorage.setItem(accessStorageKey("storage", "env-prod"), "{not json");
    expect(readAccess("storage", "env-prod")).toBeNull();
    window.sessionStorage.setItem(accessStorageKey("storage", "env-prod"), JSON.stringify({ reason: "   " }));
    expect(readAccess("storage", "env-prod")).toBeNull();
  });
});

describe("policies and reasons", () => {
  it("asks only an audited environment, and refuses deny and an unknown policy", () => {
    expect(needsConfirmation("audited")).toBe(true);
    expect(needsConfirmation("open")).toBe(false);
    expect(isDenied("deny")).toBe(true);
    expect(isDenied("unknown")).toBe(true);
    expect(isDenied("audited")).toBe(false);
  });

  it("takes a reason trimmed, non-empty and within the bound", () => {
    expect(validReason("  why  ")).toBe("why");
    expect(validReason("   ")).toBeNull();
    expect(validReason("x".repeat(REASON_MAX + 1))).toBeNull();
  });
});
