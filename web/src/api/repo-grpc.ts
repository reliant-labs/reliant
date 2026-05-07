// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";
import { create } from "@bufbuild/protobuf";
import type { Repo as ProtoRepo } from "../gen/reliant/v1/repo_pb";
import { ListReposRequestSchema } from "../gen/reliant/v1/repo_pb";

export interface Repo {
  id: string;
  project_id: string;
  name: string;
  relative_path: string;
  remote_url: string;
  created_at: string;
  updated_at: string;
}

function protoToFrontend(proto: ProtoRepo): Repo {
  return {
    id: proto.id,
    project_id: proto.projectId,
    name: proto.name,
    relative_path: proto.relativePath,
    remote_url: proto.remoteUrl,
    created_at: proto.createdAt,
    updated_at: proto.updatedAt,
  };
}

export const repoGrpc = {
  async list(projectId: string): Promise<{ repos: Repo[] }> {
    const client = grpcClient.repo();
    const request = create(ListReposRequestSchema, { projectId });
    const response = await client.listRepos(request);
    return { repos: response.repos.map(protoToFrontend) };
  },
};
