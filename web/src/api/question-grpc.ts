import { grpcClient } from './grpc-client';

export interface QuestionInfo {
  question_id: string;
  chat_id: string;
  workflow_id: string;
  step_id: string;
  status: string;
  created_at: string;
  metadata?: string;
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

  async getPendingQuestion(chatId: string): Promise<QuestionInfo | null> {
    const client = grpcClient.question();
    const response = await client.getPendingQuestion({ chatId });
    const q = response.question;
    if (!q) return null;
    return {
      question_id: q.questionId,
      chat_id: q.chatId,
      workflow_id: q.workflowId,
      step_id: q.stepId,
      status: q.status,
      created_at: q.createdAt,
      metadata: q.metadata ?? undefined,
    };
  },
};
