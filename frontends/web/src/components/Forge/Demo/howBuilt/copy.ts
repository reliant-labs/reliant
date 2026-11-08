// Copyright (c) 2025 Reliant Labs

/**
 * The words around the recorded run: what each file category is, and what
 * each step of the entity chain shows. Every claim here is checked against
 * the fixtures by __tests__/HowThisAppWasBuilt.test.tsx (a path named here is
 * in the manifest; a fact about an excerpt is in its code).
 */

import type { CategoryId, ChainExcerpt } from "./fixtures";

/** Display order and labels: the contract first, the project's own files last. */
export const CATEGORY_COPY: { id: CategoryId; label: string; detail: string }[] = [
  { id: "contract", label: "Contract", detail: "The protos, and the Go and TypeScript stubs buf compiles from them" },
  { id: "migrations", label: "Schema", detail: "SQL migrations, the read-only role's grants, seed data" },
  { id: "handlers", label: "Go handlers", detail: "Connect handlers, CRUD wiring and rpc stubs" },
  { id: "store", label: "ORM and store", detail: "Row types and stores projected from the applied schema" },
  { id: "wiring", label: "Server, middleware and auth", detail: "The binary, the composition graph, auth and middleware" },
  { id: "mocks", label: "Contract mocks", detail: "Go mocks of each service, and the frontend's mock data" },
  { id: "tests", label: "Tests", detail: "CRUD tests on real Postgres, handler, hook and middleware tests" },
  { id: "frontend", label: "Frontend", detail: "Connect hooks, CRUD pages and the UI kit (Next.js)" },
  { id: "observability", label: "Observability", detail: "Dashboards, alerts, collector config, browser telemetry" },
  { id: "deploy", label: "Deploy", detail: "KCL for dev, staging and prod; Dockerfiles; local compose" },
  { id: "ci", label: "CI", detail: "GitHub workflows, git hooks, lint config, the Taskfile" },
  { id: "project", label: "Project files", detail: "README, AGENTS.md, runbook, editor and agent config" },
];

export const CHAIN_COPY: Record<ChainExcerpt["id"], { title: string; body: string }> = {
  proto: {
    title: "The message you write",
    body: "You write the message and its forge:entity marker. forge adds the fields it manages (id, created_at, updated_at) and, in the service above it, the five CRUD rpcs.",
  },
  migration: {
    title: "The migration",
    body: "forge writes the table's first migration from the message, once. After that the schema is yours: you change it with a new migration, not by editing the proto.",
  },
  orm: {
    title: "The ORM row",
    body: "forge applies the migrations to a real Postgres and reads the table back, so this type matches a schema that exists. It is rewritten on every forge generate.",
  },
  handler: {
    title: "The handler",
    body: "The Connect handler is yours and works from the start: it hands the request to forge's CRUD code. The custom rpc, CancelOrder, gets a stub that answers Unimplemented until you write it.",
  },
  hook: {
    title: "The React hooks",
    body: "One React Query hook per rpc, typed from the same proto as the server. They are rewritten when the proto changes.",
  },
  test: {
    title: "The test",
    body: "A lifecycle test that runs create, get, list, update and delete against a real Postgres with your migrations applied. forge writes it once; you extend it as the entity grows.",
  },
};

/** Files the explainers name; the test checks each is in the manifest. */
export const NAMED_PATHS = {
  serve: "cmd/shop/cmd/serve.go",
  browserTelemetry: "frontends/web/src/lib/otel_gen.ts",
  agents: "AGENTS.md",
  cancelStub: "internal/handlers/orders/rpc_cancel_order.go",
} as const;
