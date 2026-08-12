import { describe, expect, it } from "vitest";
import { isSpawnOrigin } from "../threadUtils";
import type { ThreadOrigin } from "../../../../types/streaming";

// Mirrors the augmentation rule in InterleavedTimeline: the stream may only
// FILL a missing origin, never override one that came from the workflow tree.
function applyStreamOrigin(
  existing: { origin?: ThreadOrigin; isSpawn: boolean },
  streamed: ThreadOrigin | undefined,
) {
  if (streamed && !existing.origin) {
    existing.origin = streamed;
    existing.isSpawn = isSpawnOrigin(streamed);
  }
  return existing;
}

describe("thread origin precedence", () => {
  // The regression that made this look chat-specific. Thread announcements are
  // persisted in chat_updates and replayed on reconnect, so a thread announced
  // before the spawn-origin overwrite was fixed still has a stale "node" event
  // on disk permanently. Letting the stream win re-poisoned already-correct
  // threads on every reload — new chats looked fixed, old ones stayed broken.
  it("does not let a stale streamed origin override the workflow tree", () => {
    const fromTree = { origin: "spawn" as ThreadOrigin, isSpawn: true };
    const result = applyStreamOrigin(fromTree, "node");

    expect(result.origin).toBe("spawn");
    expect(result.isSpawn).toBe(true);
  });

  // The stream's legitimate job: a thread the execution tree has not delivered
  // yet still needs to be classified.
  it("fills an origin the workflow tree did not provide", () => {
    const missing = { origin: undefined, isSpawn: false };
    const result = applyStreamOrigin(missing, "spawn");

    expect(result.origin).toBe("spawn");
    expect(result.isSpawn).toBe(true);
  });

  it("leaves an unclassified thread alone when the stream has nothing either", () => {
    const missing = { origin: undefined, isSpawn: false };
    expect(applyStreamOrigin(missing, undefined).origin).toBeUndefined();
  });
});
