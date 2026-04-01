import { grpcClient } from "./grpc-client";
import { create } from "@bufbuild/protobuf";
import type { Plan as ProtoPlan } from "../gen/reliant/v1/plan_pb";
import {
  CreatePlanRequestSchema,
  GetPlanRequestSchema,
  GetPlanByChatIdRequestSchema,
  UpdatePlanRequestSchema,
  DeletePlanRequestSchema,
  ListPlanTasksRequestSchema,
} from "../gen/reliant/v1/plan_pb";
import { PlanStatus } from "../gen/reliant/v1/common_pb";
import { PlanComplexity } from "../gen/reliant/v1/plan_pb";

export { PlanStatus, PlanComplexity };

// Type definition matching frontend expectations
export interface Plan {
  id: string;
  chat_id: string;
  title: string;
  description?: string;
  status: PlanStatus;
  complexity?: PlanComplexity;
  created_at: string;
  updated_at: string;
}

// Convert proto Plan to frontend Plan
function protoToFrontend(proto: ProtoPlan): Plan {
  return {
    id: proto.id,
    chat_id: proto.chatId,
    title: proto.title,
    description: proto.description || undefined,
    status: proto.status,
    complexity: proto.complexity || undefined,
    created_at: proto.createdAt,
    updated_at: proto.updatedAt,
  };
}

export const planGrpc = {
  async create(chatId: string, title: string, description?: string, status?: PlanStatus, complexity?: PlanComplexity): Promise<Plan> {
    const client = grpcClient.plan();
    const request = create(CreatePlanRequestSchema, {
      chatId,
      title,
      description: description || "",
      status: status || PlanStatus.PENDING,
      complexity: complexity,
    });
    const response = await client.createPlan(request);
    if (!response.plan) throw new Error("No plan in response");
    return protoToFrontend(response.plan);
  },

  async get(planId: string): Promise<Plan> {
    const client = grpcClient.plan();
    const request = create(GetPlanRequestSchema, { planId });
    const response = await client.getPlan(request);
    if (!response.plan) throw new Error("No plan in response");
    return protoToFrontend(response.plan);
  },

  async getByChatId(chatId: string): Promise<Plan | null> {
    const client = grpcClient.plan();
    const request = create(GetPlanByChatIdRequestSchema, { chatId });
    const response = await client.getPlanByChatId(request);
    if (!response.plan) return null;
    return protoToFrontend(response.plan);
  },

  async update(planId: string, updates: { title?: string; description?: string; status?: PlanStatus; complexity?: PlanComplexity }): Promise<Plan> {
    const client = grpcClient.plan();
    const request = create(UpdatePlanRequestSchema, {
      planId,
      title: updates.title,
      description: updates.description,
      status: updates.status,
      complexity: updates.complexity,
    });
    const response = await client.updatePlan(request);
    if (!response.plan) throw new Error("No plan in response");
    return protoToFrontend(response.plan);
  },

  async delete(planId: string): Promise<void> {
    const client = grpcClient.plan();
    const request = create(DeletePlanRequestSchema, { planId });
    await client.deletePlan(request);
  },

  async listTasks(planId: string) {
    const client = grpcClient.plan();
    const request = create(ListPlanTasksRequestSchema, { planId });
    const response = await client.listPlanTasks(request);
    return {
      tasks: response.tasks,
      total: response.tasks.length,
    };
  },
};
