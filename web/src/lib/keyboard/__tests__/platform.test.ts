import { afterEach, describe, expect, it, vi } from "vitest";
import { detectIsMac, formatBinding } from "../platform";

/** Replace navigator fields for one assertion, then restore. */
function withNavigator(
  overrides: Partial<{
    platform: string;
    userAgent: string;
    userAgentData: { platform?: string };
  }>,
  assert: () => void,
) {
  const nav = window.navigator as Navigator & {
    userAgentData?: { platform?: string };
  };
  const saved = {
    platform: nav.platform,
    userAgent: nav.userAgent,
    userAgentData: nav.userAgentData,
  };

  for (const [key, value] of Object.entries(overrides)) {
    Object.defineProperty(nav, key, { value, configurable: true });
  }

  try {
    assert();
  } finally {
    for (const [key, value] of Object.entries(saved)) {
      Object.defineProperty(nav, key, { value, configurable: true });
    }
  }
}

describe("detectIsMac", () => {
  afterEach(() => vi.restoreAllMocks());

  it("detects a Mac from the legacy platform field", () => {
    withNavigator({ platform: "MacIntel" }, () => {
      expect(detectIsMac()).toBe(true);
    });
  });

  it("detects a Mac from userAgentData when platform is empty", () => {
    // navigator.platform is deprecated and returns "" in some environments;
    // falling through to a second signal is what keeps Cmd working there.
    withNavigator(
      { platform: "", userAgentData: { platform: "macOS" } },
      () => {
        expect(detectIsMac()).toBe(true);
      },
    );
  });

  it("falls back to the user-agent string", () => {
    withNavigator(
      {
        platform: "",
        userAgentData: undefined,
        userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
      },
      () => {
        expect(detectIsMac()).toBe(true);
      },
    );
  });

  it("returns false on Windows", () => {
    withNavigator(
      {
        platform: "Win32",
        userAgentData: undefined,
        userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
      },
      () => {
        expect(detectIsMac()).toBe(false);
      },
    );
  });

  it("returns false on Linux", () => {
    withNavigator(
      {
        platform: "Linux x86_64",
        userAgentData: undefined,
        userAgent: "Mozilla/5.0 (X11; Linux x86_64)",
      },
      () => {
        expect(detectIsMac()).toBe(false);
      },
    );
  });

  it("returns false rather than throwing when every signal is empty", () => {
    withNavigator(
      { platform: "", userAgentData: undefined, userAgent: "" },
      () => {
        expect(detectIsMac()).toBe(false);
      },
    );
  });
});

describe("formatBinding", () => {
  it("uses symbols on macOS", () => {
    expect(formatBinding("meta+shift+P", true)).toBe("⌘⇧P");
  });

  it("uses words off macOS", () => {
    expect(formatBinding("ctrl+shift+P", false)).toBe("Ctrl+Shift+P");
  });

  it("renders arrows as glyphs", () => {
    expect(formatBinding("meta+Down", true)).toBe("⌘↓");
  });

  it("joins a sequence with 'then' so it reads as two presses", () => {
    expect(formatBinding("meta+K T", true)).toBe("⌘K then T");
  });

  it("returns an empty string for an empty binding", () => {
    expect(formatBinding("", true)).toBe("");
  });
});
