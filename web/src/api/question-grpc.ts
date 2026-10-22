import { grpcClient } from './grpc-client';
import type { QuestionInfo as QuestionInfoPb } from '../gen/reliant/v1/question_pb';

export interface QuestionInfo {
  question_id: string;
  chat_id: string;
  workflow_id: string;
  /**
   * The thread that asked. Each spawned agent is a thread that can park on
   * its own question, so this — not the chat — says who is waiting. The
   * chat's main thread id is the chat id.
   */
  thread_id: string;
  step_id: string;
  status: string;
  created_at: string;
  metadata?: string;
  /**
   * Asked without waiting (ask_user async: true): the thread carried on, and
   * an answer reaches it later as a message from the user. Never the chat's
   * or a thread's pending gate — it renders as a card in the timeline.
   */
  async?: boolean;
  /** The ask_user tool call that raised it, when one did. */
  tool_call_id?: string;
  /** The answer (ResolveQuestion response_data), once resolved. */
  response_data?: string;
}

function fromProto(q: QuestionInfoPb): QuestionInfo {
  return {
    question_id: q.questionId,
    chat_id: q.chatId,
    workflow_id: q.workflowId,
    thread_id: q.threadId,
    step_id: q.stepId,
    status: q.status,
    created_at: q.createdAt,
    metadata: q.metadata ?? undefined,
    async: q.async,
    tool_call_id: q.toolCallId || undefined,
    response_data: q.responseData ?? undefined,
  };
}

export const questionGrpc = {
  async resolveQuestion(questionId: string, action: string, responseData?: string) {
    const client = grpcClient.question();
    const response = await client.resolveQuestion({
      questionId,
      action,
      responseData,
    });
    return response;
  },

  /** Every pending question in a chat, oldest first, across all its threads. */
  async listPendingQuestions(chatId: string): Promise<QuestionInfo[]> {
    const client = grpcClient.question();
    const response = await client.listPendingQuestions({ chatId });
    return response.questions.map(fromProto);
  },

  /**
   * Every async question in a chat (asked without waiting), pending and
   * resolved, oldest first: what the timeline's question cards render from.
   */
  async listAsyncQuestions(chatId: string): Promise<QuestionInfo[]> {
    const client = grpcClient.question();
    const response = await client.listAsyncQuestions({ chatId });
    return response.questions.map(fromProto);
  },
};
