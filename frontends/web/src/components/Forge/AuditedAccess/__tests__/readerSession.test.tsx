// Copyright (c) 2025 Reliant Labs

/**
 * The authenticated path accepts a captured reader only while it IS the current
 * session; fixture readers have their own validity check, and an authenticated
 * session can never be passed off as one.
 */

import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, render } from "@testing-library/react";
import { createFixtureReader, FixtureReaderProvider, getReaderSession, requireCurrentReader, setReaderPrincipal } from "../readerSession";

beforeEach(() => setReaderPrincipal("production-a", true));
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it.each([
  ["rotation", "production-b"],
  ["sign-out", null],
] as const)("a real reader offered as a fixture reader still fails requireCurrentReader after %s mid-await", async (_, next) => {
  vi.spyOn(console, "error").mockImplementation(() => {});
  const real = getReaderSession();
  // The provider refuses a reader createFixtureReader did not mint; whether it
  // throws or not, the real reader must not gain fixture validity from it.
  try { render(<FixtureReaderProvider reader={real}><span /></FixtureReaderProvider>); } catch { /* refused */ }
  requireCurrentReader(real); // before the request: still the current session
  let release!: () => void;
  const inFlight = new Promise<void>((resolve) => { release = resolve; });
  const request = (async () => { await inFlight; requireCurrentReader(real); })();
  setReaderPrincipal(next);
  release();
  await expect(request).rejects.toThrow("Reader session changed");
});

it("keeps a mounted fixture reader valid across auth rotation, and drops it on unmount", () => {
  const fixture = createFixtureReader();
  const view = render(<FixtureReaderProvider reader={fixture}><span /></FixtureReaderProvider>);
  setReaderPrincipal("production-b");
  setReaderPrincipal(null);
  expect(() => requireCurrentReader(fixture)).not.toThrow();
  view.unmount();
  expect(() => requireCurrentReader(fixture)).toThrow("Reader session changed");
});
