// Copyright (c) 2025 Reliant Labs

/**
 * The committed fixtures are the capture script's output, and say so: each
 * carries the script's schema, its producer path and the forge version it
 * ran; the parsers refuse anything else; the manifest's totals add up; and
 * the cast is the run the script performs, every command exiting 0.
 */

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

import {
  CAPTURE_PRODUCER,
  CAST_SCHEMA,
  MANIFEST_SCHEMA,
  parseCast,
  parseManifest,
  scaffoldFixtures,
  toAsciicast,
} from "../fixtures";

const WEB = resolve(__dirname, "../../../../../..");
const read = (path: string) => readFileSync(resolve(WEB, path), "utf8");
const castRaw = () => read("src/components/Forge/Demo/howBuilt/data/forge-scaffold-cast.json");
const manifestRaw = () => read("src/components/Forge/Demo/howBuilt/data/forge-scaffold-manifest.json");

describe("the scaffold fixtures", () => {
  it("name the capture script as their producer, with the script's own schemas", () => {
    const script = read("scripts/demo/capture-forge-scaffold.mjs");
    // The recorder writes these exact constants; the parser expects the same.
    expect(script).toContain(`export const CAST_SCHEMA = "${CAST_SCHEMA}";`);
    expect(script).toContain(`export const MANIFEST_SCHEMA = "${MANIFEST_SCHEMA}";`);
    expect(script).toContain(`const PRODUCER = "${CAPTURE_PRODUCER}";`);
    expect(read(CAPTURE_PRODUCER.replace(/^frontends\/web\//, ""))).toContain("capture-forge-scaffold.mjs");

    const { cast, manifest } = scaffoldFixtures();
    for (const fixture of [cast, manifest]) {
      expect(fixture.producer).toBe(CAPTURE_PRODUCER);
      expect(fixture.forge_version).toMatch(/^v\d+\.\d+\.\d+/);
    }
    expect(cast.schema).toBe(CAST_SCHEMA);
    expect(manifest.schema).toBe(MANIFEST_SCHEMA);
    // One run wrote both.
    expect(cast.forge_version).toBe(manifest.forge_version);
    expect(cast.captured_at).toBe(manifest.captured_at);
  });

  it("refuse a file the script did not write", () => {
    const cast = JSON.parse(castRaw());
    expect(() => parseCast(JSON.stringify({ ...cast, schema: "asciicast/v2" }))).toThrow(/schema/);
    expect(() => parseCast(JSON.stringify({ ...cast, producer: "by hand" }))).toThrow(/producer/);
    expect(() => parseCast(JSON.stringify({ ...cast, forge_version: "latest" }))).toThrow(/forge_version/);
    const manifest = JSON.parse(manifestRaw());
    expect(() => parseManifest(JSON.stringify({ ...manifest, schema: "x" }))).toThrow(/schema/);
    expect(() => parseManifest(JSON.stringify({ ...manifest, totals: { ...manifest.totals, forge_lines: 99_999 } }))).toThrow(/forge_lines/);
    expect(() => parseManifest(JSON.stringify({ ...manifest, totals: { ...manifest.totals, files: 1 } }))).toThrow(/files/);
  });

  it("are the run the script performs: project new, the two protos you write, scaffold, lint, build", () => {
    const { cast } = scaffoldFixtures();
    expect(cast.steps.map((s) => (s.kind === "command" ? s.command : `edit ${s.path}`))).toEqual([
      "forge project new shop --service orders --service inventory --frontend web",
      "edit proto/services/orders/v1/orders.proto",
      "edit proto/services/inventory/v1/inventory.proto",
      "forge scaffold",
      "forge lint --quiet",
      "go build ./...",
    ]);
    for (const step of cast.steps) {
      if (step.kind !== "command") continue;
      expect(step.exit_code, step.command).toBe(0);
      // Played no longer than it ran; waits are shortened, never stretched.
      expect(step.duration).toBeLessThanOrEqual(step.real_seconds);
    }
    // The work dir and home of the machine that recorded it read as `~`.
    expect(castRaw()).not.toContain("forge-demo-capture");
    expect(castRaw()).not.toMatch(/\/(home|Users)\//);
  });

  it("count what you wrote as exactly the proto the edit steps add", () => {
    const { cast, manifest } = scaffoldFixtures();
    const edits = cast.steps.flatMap((s) => (s.kind === "edit" ? [s] : []));
    expect(manifest.you_wrote.map((f) => f.path).sort()).toEqual(edits.map((e) => e.path).sort());
    expect(manifest.totals.you_wrote_files).toBe(edits.length);
    // The diff's added lines: an edit's text closes the service with the `}` forge's TODO line sat above,
    // which git counts as forge's line, kept; so a file's count is the text's, or one less.
    for (const edit of edits) {
      const counted = manifest.you_wrote.find((f) => f.path === edit.path)!.lines;
      expect(counted, edit.path).toBeGreaterThanOrEqual(edit.lines - 1);
      expect(counted, edit.path).toBeLessThanOrEqual(edit.lines);
    }
    expect(manifest.totals.you_wrote_lines).toBe(manifest.you_wrote.reduce((n, f) => n + f.lines, 0));
  });

  it("write a standard asciicast v2 file from the cast", () => {
    const { cast } = scaffoldFixtures();
    const [header, ...events] = toAsciicast(cast).trimEnd().split("\n").map((line) => JSON.parse(line));
    expect(header).toMatchObject({ version: 2, width: cast.asciicast.width, height: cast.asciicast.height });
    expect(typeof header.timestamp).toBe("number");
    let last = 0;
    for (const [at, kind, data] of events) {
      expect(kind).toBe("o");
      expect(typeof data).toBe("string");
      expect(at).toBeGreaterThanOrEqual(last);
      last = at;
    }
    expect(events[0][2]).toBe("~ $ forge project new shop --service orders --service inventory --frontend web\r\n");
  });
});
