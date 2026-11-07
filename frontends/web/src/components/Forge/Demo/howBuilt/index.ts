// Copyright (c) 2025 Reliant Labs

/**
 * "How this app was built", for embedding: the whole section, or its replay
 * and panel on their own, and the recorded run they read.
 *
 *   <HowThisAppWasBuilt />                          the section, on the built-in recording
 *   <TerminalReplay cast={cast} />                  just the terminal
 *   <ScaffoldPanel manifest={manifest} cast={cast} /> just "What forge generated"
 *
 * The data is two JSON files written by scripts/demo/capture-forge-scaffold.sh
 * (data/forge-scaffold-cast.json, data/forge-scaffold-manifest.json). Another
 * app can copy them and read them with parseCast / parseManifest, which check
 * the schema and the totals; toAsciicast turns the cast into a standard .cast.
 */

export { HowThisAppWasBuilt, type HowThisAppWasBuiltProps } from "./HowThisAppWasBuilt";
export { TerminalReplay, type TerminalReplayProps } from "./TerminalReplay";
export { ScaffoldPanel, summarize } from "./ScaffoldPanel";
export {
  CAPTURE_PRODUCER,
  CAST_SCHEMA,
  MANIFEST_SCHEMA,
  parseCast,
  parseManifest,
  scaffoldFixtures,
  toAsciicast,
  type ScaffoldCast,
  type ScaffoldManifest,
} from "./fixtures";
