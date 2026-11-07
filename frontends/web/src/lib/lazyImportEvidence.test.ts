// Copyright (c) 2025 Reliant Labs
import { describe, expect, it } from "vitest";
import { failedImportResources, importFailureDetails, redactImportUrls } from "./lazyImportEvidence";
import { isChunkLoadError, StaleBuildError } from "./lazyRoute";
const resource = (name: string, responseStatus?: number) => ({ name, responseStatus }) as unknown as PerformanceEntry;
describe("lazy import evidence", () => {
  it("retains Safari's original rejection without asserting a deploy caused it", () => {
    const cause = new TypeError("Importing a module script failed.");
    expect(isChunkLoadError(cause)).toBe(true);
    const error = new StaleBuildError(cause);
    expect(error.cause).toBe(cause);
    expect(error.message).not.toMatch(/was updated|newer version|latest version/);
    expect(importFailureDetails(error)).toContain("TypeError: Importing a module script failed.");
  });
  it("records failed chunk HTTP status/path but no auth, query or fragment", () => {
    const entries = [resource("https://user:password@preview.test/assets/route.js?token=secret#private", 502), resource("https://preview.test/assets/site.css?session=hidden",401),resource("https://preview.test/api/user?token=private",500),resource("https://preview.test/assets/ok.js",200)];
    expect(failedImportResources(entries)).toEqual([{path:"/assets/route.js",status:502},{path:"/assets/site.css",status:401}]);
    const details = importFailureDetails(new Error("Failed to fetch dynamically imported module: https://u:p@preview.test/assets/route.js?secret=hidden#fragment"),entries);
    expect(details).toContain("Resource HTTP 502: /assets/route.js");
    expect(details).not.toMatch(/password|token|secret|hidden|fragment|u:p/);
  });
  it("redacts secrets embedded in path segments but keeps asset names", () => {
    const entries = [resource("https://storage.test/sig/AKIA1234567890ABCDEFsecretvalue/assets/route-BxY12abc.js", 403), resource("https://preview.test/t/abc%2Fdef/app.css", 500)];
    expect(failedImportResources(entries)).toEqual([{path:"/sig/[redacted]/assets/route-BxY12abc.js",status:403},{path:"/t/[redacted]/app.css",status:500}]);
    const details = importFailureDetails(new Error("Failed to fetch dynamically imported module: https://preview.test/s/eyJhbGciOiJIUzI1NiJ9xyz123/assets/route.js"), entries);
    expect(details).toContain("https://preview.test/s/[redacted]/assets/route.js");
    expect(details).not.toMatch(/AKIA|eyJhbGci|abc%2F/);
  });
  it("does not fabricate HTTP status in older Safari and redacts copied page URLs", () => {
    expect(failedImportResources([resource("https://preview.test/assets/route.js")])).toEqual([]);
    expect(redactImportUrls("URL: https://u:p@preview.test/logs?token=secret#part")).toBe("URL: https://preview.test/logs");
  });
  it("does not label real module evaluation errors as a chunk load failure", () => {
    expect(isChunkLoadError(new SyntaxError("The requested module does not provide an export named Foo"))).toBe(false);
    expect(isChunkLoadError(new TypeError("Cannot read properties of undefined"))).toBe(false);
  });
});
