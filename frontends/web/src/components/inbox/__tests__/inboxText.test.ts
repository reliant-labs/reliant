import { describe, expect, it } from "vitest";
import { sentenceCase } from "../InboxItem";

describe("sentenceCase", () => {
  it("capitalises a server sentence", () => {
    expect(sentenceCase("daemon x is offline")).toBe("Daemon x is offline");
  });

  it("leaves a leading identifier as it is spelled", () => {
    expect(sentenceCase("default_repo is now required")).toBe("default_repo is now required");
    expect(sentenceCase("github.com/acme is unreachable")).toBe("github.com/acme is unreachable");
    expect(sentenceCase("projectId is missing")).toBe("projectId is missing");
  });
});
