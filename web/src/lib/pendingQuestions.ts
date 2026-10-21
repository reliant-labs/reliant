/**
 * Which pending question belongs to which thread.
 *
 * A chat is not "waiting on one question": every spawned agent is a thread
 * that can park on its own ask_user, so a chat can be waiting on the user in
 * several places at once. The composer answers the question of the thread the
 * user is looking at, and everything else must stay discoverable — showing
 * "the chat's question" hid an older spawn's question behind a sibling's
 * newer one, and the reply the user typed for it went to a mailbox the parked
 * thread never drains.
 */

import type { QuestionInfo } from "../api/question-grpc";

/**
 * Whether a thread id names the chat's main thread. The main thread's id is
 * the chat id (and the chat's root workflow id, which is the same value for
 * every chat today); an empty id is a row that predates thread scoping.
 */
function isMainThread(threadId: string | null | undefined, chatId: string, mainThreadId?: string | null): boolean {
  return !threadId || threadId === chatId || (!!mainThreadId && threadId === mainThreadId);
}

/** The thread a question was asked on, with the main thread canonicalized to the chat id. */
export function questionThreadId(question: QuestionInfo, chatId: string, mainThreadId?: string | null): string {
  return isMainThread(question.thread_id, chatId, mainThreadId) ? chatId : question.thread_id;
}

/** The thread in view: a selected side thread, else the main thread (as the chat id). */
export function viewedThreadId(chatId: string, selectedThreadId?: string | null, mainThreadId?: string | null): string {
  return isMainThread(selectedThreadId, chatId, mainThreadId) ? chatId : selectedThreadId!;
}

/**
 * The question the thread in view is parked on, or null. The newest wins when
 * a thread has several (the list is oldest first, and streamed questions are
 * appended), because a thread blocks on one question at a time and its newest
 * is the one it is waiting on.
 */
export function questionForThread(
  questions: readonly QuestionInfo[] | undefined,
  chatId: string | undefined,
  selectedThreadId?: string | null,
  mainThreadId?: string | null,
): QuestionInfo | null {
  if (!chatId || !questions?.length) return null;
  const threadId = viewedThreadId(chatId, selectedThreadId, mainThreadId);
  for (let i = questions.length - 1; i >= 0; i--) {
    if (questionThreadId(questions[i], chatId, mainThreadId) === threadId) return questions[i];
  }
  return null;
}
