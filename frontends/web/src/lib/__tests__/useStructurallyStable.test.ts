// Copyright (c) 2025 Reliant Labs

import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { structurallyEqual, useStructurallyStable } from "../useStructurallyStable";

describe("structurallyEqual", () => {
  it("compares arrays and plain objects by their members", () => {
    expect(structurallyEqual([1, [2, { a: "x" }]], [1, [2, { a: "x" }]])).toBe(true);
    expect(structurallyEqual({ a: 1, b: [null, NaN] }, { b: [null, NaN], a: 1 })).toBe(true);
    expect(structurallyEqual([1, 2], [1, 2, 3])).toBe(false);
    expect(structurallyEqual({ a: 1 }, { a: 1, b: undefined })).toBe(false);
    expect(structurallyEqual({ a: [1] }, { a: [2] })).toBe(false);
    expect(structurallyEqual([], {})).toBe(false);
  });

  it("compares everything else by identity", () => {
    const f = () => 1;
    expect(structurallyEqual({ f }, { f })).toBe(true);
    expect(structurallyEqual({ f }, { f: () => 1 })).toBe(false);
    expect(structurallyEqual(new Date(0), new Date(0))).toBe(false);
  });
});

describe("useStructurallyStable", () => {
  it("keeps the first value while new ones hold the same data, and takes a changed one", () => {
    const first = [{ label: "p95", values: [1, 2, 3] }];
    const { result, rerender } = renderHook(({ value }) => useStructurallyStable(value), { initialProps: { value: first } });
    rerender({ value: [{ label: "p95", values: [1, 2, 3] }] });
    expect(result.current).toBe(first);
    const changed = [{ label: "p95", values: [1, 2, 4] }];
    rerender({ value: changed });
    expect(result.current).toBe(changed);
  });
});
