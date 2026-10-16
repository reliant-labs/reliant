import { MessageRole } from "../../gen/reliant/v1/chat_pb";
import type { Message } from "../../api/client";

type MessageRow = { type: "message"; message: Message; mobileTurnMessages?: Message[] };
/** Derived presentation only: stable first-message identity, original branch/copy targets. */
export function consolidateMobileAssistantTurns<T extends { type: string }>(rows: T[], canJoin: (left: T, right: T) => boolean): T[] {
  const output: T[] = [];
  let run: T[] = [];
  const flush = () => {
    if (!run.length) return;
    const originals = run.map(row => (row as unknown as MessageRow).message);
    const first = originals[0];
    const last = originals[originals.length - 1];
    const merged = {
      ...first,
      updatedAt: last.updatedAt,
      streamingState: last.streamingState,
      contentBlocks: originals.flatMap(message => message.contentBlocks || []).map((block, index) => ({ ...block, index })),
      attachments: originals.flatMap(message => message.attachments || []),
    };
    output.push({ ...run[0], message: merged, mobileTurnMessages: originals });
    run = [];
  };
  for (const row of rows) {
    const message = row.type === "message" ? (row as unknown as MessageRow).message : undefined;
    if (!message || message.role !== MessageRole.ASSISTANT || !canJoin(row, row)) {
      flush(); output.push(row); continue;
    }
    if (run.length && !canJoin(run[run.length - 1], row)) flush();
    run.push(row);
  }
  flush();
  return output;
}
