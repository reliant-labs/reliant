// The cross-language guard of lazy tool bodies (dev-docs/design/chat-lazy-tool-bodies.md, §2).
//
// A stub transcript read sends a finished call's input as a skeleton: the same
// JSON object, every key kept, long strings cut. Every collapsed surface (a
// row's header, a fold's live line and counts, the deploy card, the
// request-machine card) labels the call from that skeleton, through the same
// formatters as the full input. The server writes a golden corpus of
// real-shaped inputs and their skeletons (TestSkeletonCorpus in
// internal/messageproto); this reads it and asserts that every label comes
// out the same from the skeleton as from the input. A formatter that starts
// reading a key the skeleton cuts fails here rather than drifting in the UI.

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { formatToolParams, type ToolInput } from "./toolFormatters";
import { toolActionVerb } from "./toolAction";
import { deployOfShellCall } from "../components/Apps/DeployedAppCard";
import { requestMachineReason } from "../components/Chat/tool-renderers/RequestMachineToolRenderer";
import { summarizeTurnWork } from "../components/Chat/turnWorkSummary";
import { ToolCallStatus } from "../gen/reliant/v1/chat_pb";

interface CorpusEntry {
  name: string;
  tool: string;
  input: string;
  skeleton: string;
  elided: boolean;
}

const corpusPath = resolve(__dirname, "../../../../internal/messageproto/testdata/skeleton_corpus.json");
const corpus = JSON.parse(readFileSync(corpusPath, "utf8")) as { entries: CorpusEntry[] };

function parse(raw: string): ToolInput {
  try {
    return JSON.parse(raw) as Record<string, unknown>;
  } catch {
    return raw;
  }
}

/** The bytes of the stored input, as the server reports it in body_size. */
const byteLength = (s: string) => new TextEncoder().encode(s).length;

// A header-less patch keeps no line in its skeleton, so its size label reads
// the stored input's byte length (inputSize) instead of the patch text's: the
// one label that differs in its number, never in its shape. Known gap,
// recorded in the design doc.
const SIZE_LABEL = /^\d+ chars$/;

// How much of a summary any surface shows: a row's header truncates to its
// width, a fold's line and a group's lead to 48 characters. A skeleton keeps a
// string's first 256 bytes, so past this the two may differ unseen (a raw,
// non-JSON input is the whole summary).
const SHOWN = 200;
const shown = (text: string | undefined) => text?.slice(0, SHOWN);

function labels(tool: string, input: ToolInput, inputSize?: number) {
  const formatted = formatToolParams(tool, input, { inputSize });
  const fold = summarizeTurnWork([
    { call: { id: "c", name: tool, input, inputSize, durableStatus: ToolCallStatus.COMPLETED }, result: {} },
  ]);
  return {
    summary: shown(formatted?.summary),
    description: shown(formatted?.structured?.description as string | undefined),
    filePaths: formatted?.structured?.filePaths,
    verbDone: toolActionVerb(tool, "done", input),
    verbLive: toolActionVerb(tool, "live", input),
    foldStep: fold.current,
    foldCounts: fold.counts,
    deploy: deployOfShellCall(input as Record<string, unknown> | string, { content: '{"exit_code":0}' }),
    requestMachine: requestMachineReason(input as Record<string, unknown> | string),
  };
}

describe("tool labels from a skeleton input (skeleton_corpus.json)", () => {
  it("has a corpus to check", () => {
    expect(corpus.entries.length).toBeGreaterThan(5);
  });

  for (const entry of corpus.entries) {
    it(`labels "${entry.name}" the same from its skeleton`, () => {
      const full = labels(entry.tool, parse(entry.input));
      const fromSkeleton = labels(
        entry.tool,
        parse(entry.skeleton),
        entry.elided ? byteLength(entry.input) : undefined,
      );
      if (entry.elided && SIZE_LABEL.test(full.summary ?? "")) {
        expect(fromSkeleton.summary).toMatch(SIZE_LABEL);
        expect({ ...fromSkeleton, summary: "", foldStep: "" }).toEqual({ ...full, summary: "", foldStep: "" });
        return;
      }
      expect(fromSkeleton).toEqual(full);
    });
  }

  it("covers the labels the design names: file paths, a deploy env and a request-machine reason", () => {
    const all = corpus.entries.map((entry) => labels(entry.tool, parse(entry.skeleton)));
    expect(all.some((l) => (l.filePaths?.length ?? 0) > 1)).toBe(true);
    expect(all.some((l) => l.deploy !== null)).toBe(true);
    expect(all.some((l) => l.requestMachine !== "")).toBe(true);
  });
});
