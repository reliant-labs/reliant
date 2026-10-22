import { grpcClient } from "./grpc-client";
import { create } from "@bufbuild/protobuf";
import type { Task as ProtoTask } from "../gen/reliant/v1/task_pb";
import {
  CreateTaskRequestSchema,
  GetTaskRequestSchema,
  ListTasksRequestSchema,
  UpdateTaskRequestSchema,
  DeleteTaskRequestSchema,
} from "../gen/reliant/v1/task_pb";
import { TaskStatus } from "../gen/reliant/v1/task_pb";

export { TaskStatus };

// Type definition matching frontend expectations  
export interface Task {
  id: string;
  plan_id: string;
  parent_task_id?: string;
  title: string;
  description?: string;
  status: TaskStatus;
  position: number;
  created_at: string;
  updated_at: string;
}

// Convert proto Task to frontend Task
function protoToFrontend(proto: ProtoTask): Task {
  return {
    id: proto.id,
    plan_id: proto.planId,
    parent_task_id: proto.parentTaskId || undefined,
    title: proto.title,
    description: proto.description || undefined,
    status: proto.status,
    position: proto.position,
    created_at: proto.createdAt,
    updated_at: proto.updatedAt,
  };
}

export const taskGrpc = {
  async create(request: {
    plan_id: string;
    title: string;
    description?: string;
    status?: TaskStatus;
    position?: number;
    parent_task_id?: string;
  }): Promise<Task> {
    const client = grpcClient.task();
    const req = create(CreateTaskRequestSchema, {
      planId: request.plan_id,
      title: request.title,
      description: request.description || "",
      status: request.status || TaskStatus.PENDING,
      position: request.position || 0,
      parentTaskId: request.parent_task_id || "",
    });
    const response = await client.createTask(req);
    if (!response.task) throw new Error("No task in response");
    return protoToFrontend(response.task);
  },

  async get(taskId: string): Promise<Task> {
    const client = grpcClient.task();
    const request = create(GetTaskRequestSchema, { taskId });
    const response = await client.getTask(request);
    if (!response.task) throw new Error("No task in response");
    return protoToFrontend(response.task);
  },

  async list(planId: string): Promise<{ tasks: Task[]; total: number }> {
    const client = grpcClient.task();
    const request = create(ListTasksRequestSchema, { planId });
    const response = await client.listTasks(request);
    return {
      tasks: response.tasks.map(protoToFrontend),
      total: response.tasks.length,
    };
  },

  async update(taskId: string, updates: {
    title?: string;
    description?: string;
    status?: TaskStatus;
    position?: number;
  }): Promise<Task> {
    const client = grpcClient.task();
    const request = create(UpdateTaskRequestSchema, {
      taskId,
      title: updates.title,
      description: updates.description,
      status: updates.status,
      position: updates.position,
    });
    const response = await client.updateTask(request);
    if (!response.task) throw new Error("No task in response");
    return protoToFrontend(response.task);
  },

  async delete(taskId: string): Promise<void> {
    const client = grpcClient.task();
    const request = create(DeleteTaskRequestSchema, { taskId });
    await client.deleteTask(request);
  },
};
