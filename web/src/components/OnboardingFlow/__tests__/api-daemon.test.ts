import { describe, expect, it } from "vitest";
import type { Daemon } from "../api";
import { getDaemonId, getFirstDaemonId, getActiveDaemonName, hasActiveDaemon } from "../api";

// ── getDaemonId ──────────────────────────────────────────────

describe("getDaemonId", () => {
  it("returns daemonId when present (camelCase JSON)", () => {
    const d: Daemon = { daemonId: "abc-123", status: 1 };
    expect(getDaemonId(d)).toBe("abc-123");
  });

  it("returns daemon_id when daemonId is absent (snake_case JSON)", () => {
    const d: Daemon = { daemon_id: "xyz-789", status: 1 };
    expect(getDaemonId(d)).toBe("xyz-789");
  });

  it("returns name when both daemonId and daemon_id are absent (control-plane AIP resource name)", () => {
    const d: Daemon = { name: "daemons/onboarding-workspace", status: 0 };
    expect(getDaemonId(d)).toBe("daemons/onboarding-workspace");
  });

  it("prefers daemonId over daemon_id over name", () => {
    const d: Daemon = {
      daemonId: "camel",
      daemon_id: "snake",
      name: "resource-name",
      status: 1,
    };
    expect(getDaemonId(d)).toBe("camel");
  });

  it("falls back through the chain: daemon_id > name", () => {
    const d: Daemon = {
      daemon_id: "snake",
      name: "resource-name",
      status: 1,
    };
    expect(getDaemonId(d)).toBe("snake");
  });

  it("returns empty string when no identifier fields are set", () => {
    const d: Daemon = { status: 0 };
    expect(getDaemonId(d)).toBe("");
  });

  it("skips empty string daemonId and falls through", () => {
    const d: Daemon = { daemonId: "", daemon_id: "", name: "fallback", status: 1 };
    expect(getDaemonId(d)).toBe("fallback");
  });
});

// ── getFirstDaemonId ─────────────────────────────────────────

describe("getFirstDaemonId", () => {
  it("returns empty string for empty list", () => {
    expect(getFirstDaemonId([])).toBe("");
  });

  it("extracts ID from the first daemon", () => {
    const daemons: Daemon[] = [
      { name: "daemons/first", status: 0 },
      { name: "daemons/second", status: 1 },
    ];
    expect(getFirstDaemonId(daemons)).toBe("daemons/first");
  });

  it("works with control-plane response shape (name only)", () => {
    const daemons: Daemon[] = [
      { name: "daemons/onboarding-workspace", hostname: "ws-abc", status: 0 },
    ];
    expect(getFirstDaemonId(daemons)).toBe("daemons/onboarding-workspace");
  });

  it("works with OSS response shape (daemonId)", () => {
    const daemons: Daemon[] = [
      { daemonId: "local-daemon-1", hostname: "localhost", status: 1 },
    ];
    expect(getFirstDaemonId(daemons)).toBe("local-daemon-1");
  });
});

// ── hasActiveDaemon ──────────────────────────────────────────

describe("hasActiveDaemon", () => {
  it("returns false for empty list", () => {
    expect(hasActiveDaemon([])).toBe(false);
  });

  it("returns true for OSS DAEMON_STATUS_ACTIVE (1)", () => {
    expect(hasActiveDaemon([{ status: 1 }])).toBe(true);
  });

  it("returns true for control-plane ACTIVE (3)", () => {
    expect(hasActiveDaemon([{ status: 3 }])).toBe(true);
  });

  it("returns false for UNSPECIFIED (0)", () => {
    expect(hasActiveDaemon([{ status: 0 }])).toBe(false);
  });

  it("returns false for IDLE (2)", () => {
    expect(hasActiveDaemon([{ status: 2 }])).toBe(false);
  });

  it("returns true if any daemon is active", () => {
    expect(hasActiveDaemon([{ status: 0 }, { status: 3 }])).toBe(true);
  });
});

// ── getActiveDaemonName ──────────────────────────────────────

describe("getActiveDaemonName", () => {
  it("returns empty string for empty list", () => {
    expect(getActiveDaemonName([])).toBe("");
  });

  it("returns empty string when no daemon is active", () => {
    expect(getActiveDaemonName([{ status: 0, hostname: "ws-abc" }])).toBe("");
  });

  it("prefers hostname over daemon ID", () => {
    expect(
      getActiveDaemonName([
        { daemonId: "id-1", hostname: "my-host", status: 1 },
      ]),
    ).toBe("my-host");
  });

  it("falls back to daemon ID when hostname is missing", () => {
    expect(
      getActiveDaemonName([{ daemonId: "id-1", status: 3 }]),
    ).toBe("id-1");
  });

  it("falls back to name when hostname and daemonId are missing", () => {
    expect(
      getActiveDaemonName([{ name: "daemons/cloud-ws", status: 3 }]),
    ).toBe("daemons/cloud-ws");
  });
});
