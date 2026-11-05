// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// stripServerFraming and userMessage used to exist TWICE: once here and once
// in the scaffold's src/lib/format-utils.ts, each carrying a comment
// promising to keep the other in sync. The scaffold copy was written once and
// never overwritten, so any correction to the framing regex could only ever
// reach projects generated after it. These tests pin the single surviving
// implementation.
//
// The framing it strips is the transport's, not the server's: connect-es
// prefixes `ConnectError.message` with "[code] ". Everything after that
// prefix is the message the backend deliberately wrote, and it is shown
// verbatim — this function used to also delete a trailing category tag the
// backend appended to every message, which is a defect that was fixed on the
// backend instead (forge/pkg/svcerr).
import { Code, ConnectError } from "@connectrpc/connect";
import { codeToString } from "@connectrpc/connect/protocol-connect";
import { describe, expect, it, vi } from "vitest";

import {
  codeName,
  ConnectClientError,
  FORGE_ERROR_REASON_HEADER,
  normalizeError,
  stripServerFraming,
  userMessage,
} from "./errors.js";
import { buildRuntimeInterceptors } from "./interceptors.js";

/**
 * A ConnectClientError from a SECOND COPY of this package: same shape, same
 * `name`, different class object. npm produces exactly this whenever two
 * dependencies want different versions of @reliantlabs/forge-web-runtime (it
 * nests the loser), and a bundler produces it whenever one package is pulled
 * in as both ESM and CJS. Declared here rather than imported so the class
 * identity is genuinely different, which is the whole point. Its shape is a
 * ConnectError's (it is one) plus the normalized fields.
 */
class ForeignConnectClientError extends Error {
  readonly code: number;
  readonly codeName: string;
  readonly metadata: Headers = new Headers();
  readonly rawMessage: string;
  details: unknown[] = [];
  readonly status: number;
  readonly retryable: boolean;
  readonly reason: string | null;
  readonly traceId: string | null = null;

  constructor(args: {
    message: string;
    code: number;
    codeName: string;
    status: number;
    retryable: boolean;
    reason?: string | null;
  }) {
    super(args.message);
    this.name = "ConnectError";
    this.rawMessage = args.message;
    this.code = args.code;
    this.codeName = args.codeName;
    this.status = args.status;
    this.retryable = args.retryable;
    this.reason = args.reason ?? null;
    this.cause = undefined;
  }
}

/** The same idea for ConnectError — a second copy of @connectrpc/connect. */
class ForeignConnectError extends Error {
  readonly code: number;
  readonly metadata: Headers;
  readonly rawMessage: string;
  details: unknown[] = [];

  constructor(message: string, code: number, metadata?: HeadersInit) {
    super(`[code_${code}] ${message}`);
    this.name = "ConnectError";
    this.rawMessage = message;
    this.code = code;
    this.metadata = new Headers(metadata ?? {});
    this.cause = undefined;
  }
}

describe("stripServerFraming", () => {
  it("drops the leading gRPC code prefix", () => {
    expect(stripServerFraming("[not_found] no such task")).toBe("no such task");
  });

  it("leaves the server's own message intact — every word of it is display copy", () => {
    expect(
      stripServerFraming("[invalid_argument] expires_at must follow issued_at"),
    ).toBe("expires_at must follow issued_at");
  });

  it("is idempotent, so double-stripping a normalized message is safe", () => {
    const once = stripServerFraming("[invalid_argument] name is required");
    expect(stripServerFraming(once)).toBe(once);
    expect(once).toBe("name is required");
  });

  it("leaves an unframed message alone", () => {
    expect(stripServerFraming("plain message")).toBe("plain message");
  });
});

describe("userMessage", () => {
  const FALLBACK = "Something went wrong. Please try again.";

  it("prefers a ConnectError's rawMessage", () => {
    const err = new ConnectError("no such task", Code.NotFound);
    expect(userMessage(err)).toBe("no such task");
  });

  it("strips framing from a plain Error", () => {
    expect(userMessage(new Error("[internal] boom"))).toBe("boom");
  });

  it("falls back when the error carries no presentable text", () => {
    expect(userMessage(new Error(""))).toBe(FALLBACK);
    expect(userMessage(null)).toBe(FALLBACK);
    expect(userMessage(undefined)).toBe(FALLBACK);
  });

  it("stringifies a non-Error throw", () => {
    expect(userMessage("[internal] bare string")).toBe("bare string");
  });

  it("takes a caller-supplied fallback", () => {
    expect(userMessage(null, "Could not save.")).toBe("Could not save.");
  });

  it("agrees with normalizeError on the message it produces", () => {
    const err = new ConnectError("[not_found] gone", Code.NotFound);
    expect(userMessage(err)).toBe(normalizeError(err).message);
  });
});

describe("normalizeError across a package boundary", () => {
  // The observed failure: EVERY error in the app arrived as
  // code:"unknown", connectCode:undefined, status:500, reason:null.
  //
  // normalizeError's pass-through guard used `err instanceof
  // ConnectClientError`, which compares CLASS IDENTITY, and class identity is
  // per module instance rather than per package. With two copies of this
  // package in one bundle the guard is false for a genuinely-normalized
  // error, and — because a normalized error was not then a ConnectError — it
  // missed the ConnectError branch too and landed in the generic Error
  // branch, which has no code and no reason to give. Silent, total, and on
  // every error. (Today the ConnectError branch would catch it, but would
  // re-wrap it rather than pass it through.)
  it("passes through an already-normalized error from a second copy of this package", () => {
    const foreign = new ForeignConnectClientError({
      message: "no such patient",
      code: Code.NotFound,
      codeName: "not_found",
      status: 404,
      retryable: false,
      reason: "patient_not_found",
    });

    const out = normalizeError(foreign);

    expect(out.codeName).toBe("not_found");
    expect(out.code).toBe(Code.NotFound);
    expect(out.status).toBe(404);
    expect(out.reason).toBe("patient_not_found");
    expect(out.retryable).toBe(false);
    // Pass-through, not a re-wrap: the interceptor chain must never double-wrap.
    expect(out).toBe(foreign);
  });

  it("recognises a second copy's ConnectClientError via instanceof", () => {
    const foreign = new ForeignConnectClientError({
      message: "boom",
      code: Code.Internal,
      codeName: "internal",
      status: 500,
      retryable: false,
    });
    expect(foreign instanceof ConnectClientError).toBe(true);
    // ...and connect-es's own shape check accepts it as a ConnectError.
    expect(foreign instanceof ConnectError).toBe(true);
  });

  it("does not mistake an unrelated Error for a normalized one", () => {
    expect(new Error("plain") instanceof ConnectClientError).toBe(false);
    expect(
      new ConnectError("x", Code.Internal) instanceof ConnectClientError,
    ).toBe(false);
    // Right name, no payload — not a ConnectClientError.
    const impostor = new Error("x");
    impostor.name = "ConnectClientError";
    expect(impostor instanceof ConnectClientError).toBe(false);
  });

  // A pin on a DEPENDENCY guarantee we now rely on deliberately:
  // @connectrpc/connect defines `static [Symbol.hasInstance]` on ConnectError
  // that matches on shape, so `err instanceof ConnectError` already survives
  // two copies of @connectrpc/connect. That is why normalizeError does not
  // carry a second, competing duck-type of its own for that branch. If a
  // future connect release drops the shim, this test fails first and tells us
  // to add one.
  it("still extracts a ConnectError raised by a second copy of @connectrpc/connect", () => {
    const metadata = { [FORGE_ERROR_REASON_HEADER]: "patient_not_found" };
    const raw = "no such patient";

    const foreign = normalizeError(
      new ForeignConnectError(raw, Code.NotFound, metadata),
    );
    const native = normalizeError(
      new ConnectError(raw, Code.NotFound, metadata),
    );

    // Field-for-field identical to the same error from our own copy — the
    // property under test, without blessing a particular `code` spelling.
    expect(foreign.codeName).toBe(native.codeName);
    expect(foreign.code).toBe(Code.NotFound);
    expect(foreign.status).toBe(404);
    expect(foreign.reason).toBe("patient_not_found");
    expect(foreign.message).toBe("no such patient");
  });
});

// ConnectClientError extends ConnectError. An app written against connect-es
// — `err instanceof ConnectError`, `err.code === Code.X`, `ConnectError.from`,
// `findDetails`, `metadata.get` — must read a normalized error exactly as it
// read the raw one, or putting the runtime chain in front of it silently turns
// every error into Code.Unknown. Both instanceof paths are pinned here.
describe("ConnectClientError is a ConnectError", () => {
  const raw = () =>
    new ConnectError("no such patient", Code.NotFound, {
      [FORGE_ERROR_REASON_HEADER]: "patient_not_found",
      "x-other": "kept",
    });

  it("satisfies instanceof ConnectError AND instanceof ConnectClientError", () => {
    const out = normalizeError(raw());
    expect(out instanceof ConnectError).toBe(true);
    expect(out instanceof ConnectClientError).toBe(true);
    expect(out).toBeInstanceOf(Error);
  });

  it("keeps the numeric code, metadata and rawMessage of the error it wraps", () => {
    const out = normalizeError(raw());
    expect(out.code).toBe(Code.NotFound);
    expect(out.codeName).toBe("not_found");
    expect(out.rawMessage).toBe("no such patient");
    expect(out.message).toBe("no such patient");
    expect(out.metadata.get("x-other")).toBe("kept");
    expect(out.reason).toBe("patient_not_found");
    expect(out.status).toBe(404);
    expect(out.retryable).toBe(false);
  });

  it("keeps the incoming details, so findDetails answers as it did", () => {
    const err = raw();
    const detail = {
      type: "google.rpc.ErrorInfo",
      value: new Uint8Array([1, 2]),
    };
    err.details.push(detail);
    const out = normalizeError(err);
    expect(out.details).toEqual([detail]);
  });

  it("is returned as-is by ConnectError.from (no re-wrap to Unknown)", () => {
    const out = normalizeError(raw());
    expect(ConnectError.from(out)).toBe(out);
    expect(ConnectError.from(out).code).toBe(Code.NotFound);
  });

  it("is still a ConnectError for failures that never reached a server", () => {
    const network = normalizeError(new TypeError("Failed to fetch"));
    expect(network instanceof ConnectError).toBe(true);
    expect(network.code).toBe(Code.Unavailable);
    expect(network.retryable).toBe(true);

    const generic = normalizeError(new Error("boom"));
    expect(generic instanceof ConnectError).toBe(true);
    expect(generic.code).toBe(Code.Unknown);
    expect(generic.retryable).toBe(false);

    const aborted = new Error("The user aborted a request.");
    aborted.name = "AbortError";
    expect(normalizeError(aborted).code).toBe(Code.Canceled);

    const thrownString = normalizeError("[internal] bare");
    expect(thrownString instanceof ConnectError).toBe(true);
    expect(thrownString.message).toBe("bare");
  });

  it("does not make a plain ConnectError a ConnectClientError", () => {
    expect(raw() instanceof ConnectClientError).toBe(false);
  });

  // The adoption path end to end: a consumer that catches what the runtime
  // chain throws and branches the connect-es way.
  it("is what the runtime interceptor chain throws, with .code intact", async () => {
    const onError = vi.fn();
    const [errorNormalize] = buildRuntimeInterceptors({ onError });
    if (!errorNormalize) throw new Error("interceptor chain changed shape");
    const call = errorNormalize(async () => {
      throw raw();
    });
    const caught = await call({
      header: new Headers(),
      stream: false,
    } as never).catch((e: unknown) => e);

    expect(caught instanceof ConnectError).toBe(true);
    expect(caught instanceof ConnectClientError).toBe(true);
    expect((caught as ConnectError).code).toBe(Code.NotFound);
    expect(ConnectError.from(caught).code).toBe(Code.NotFound);
    expect((caught as ConnectClientError).reason).toBe("patient_not_found");
    expect(onError).toHaveBeenCalledWith(caught);
  });

  it("passes a normalized error through unchanged", () => {
    const once = normalizeError(raw());
    expect(normalizeError(once)).toBe(once);
  });
});

describe("codeName", () => {
  it("spells every Code the way the Connect protocol does", () => {
    for (const value of Object.values(Code)) {
      if (typeof value !== "number") continue;
      expect(codeName(value)).toBe(codeToString(value));
    }
    expect(codeName(Code.NotFound)).toBe("not_found");
    expect(codeName(Code.DeadlineExceeded)).toBe("deadline_exceeded");
  });
});
