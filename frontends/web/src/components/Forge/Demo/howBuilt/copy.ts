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
  { id: "contract", label: "API definitions", detail: "Request and response definitions, with matching Go and TypeScript code" },
  { id: "migrations", label: "Database setup", detail: "Database changes, read-only access rules and sample data" },
  { id: "handlers", label: "Server code", detail: "Standard data operations and placeholders for custom API actions" },
  { id: "store", label: "Database code", detail: "Go types and data-access code generated from the database structure" },
  { id: "wiring", label: "Server setup and sign-in", detail: "Server startup, connected services and request checks" },
  { id: "mocks", label: "Test substitutes", detail: "Stand-ins for services and sample browser data" },
  { id: "tests", label: "Tests", detail: "Database operations tested with real Postgres, plus server and browser tests" },
  { id: "frontend", label: "Browser app", detail: "API helpers, starting pages for managing data and Next.js UI components" },
  { id: "observability", label: "Monitoring", detail: "Dashboards, alerts and settings for collecting browser and server health data" },
  { id: "deploy", label: "Deployment setup", detail: "Configuration for dev, staging and production; Dockerfiles and local services" },
  { id: "ci", label: "Automated checks", detail: "GitHub workflows and local build, test and code-check settings" },
  { id: "project", label: "Project files", detail: "README, AGENTS.md, runbook, editor and agent config" },
];

export const CHAIN_COPY: Record<ChainExcerpt["id"], { title: string; body: string }> = {
  proto: {
    title: "The API definition",
    body: "You define the order's fields and mark it for Forge to build on. Forge adds an ID, creation and update times, and API operations for creating, viewing, listing, updating and deleting orders.",
  },
  migration: {
    title: "The database setup",
    body: "Forge writes the table's first SQL migration: instructions for creating it. Later database changes go in new migration files, not edits to the API definition alone.",
  },
  orm: {
    title: "The Go database type",
    body: "Forge applies the migrations to a real Postgres database and reads the table back, so this Go type matches the database structure. It is rewritten on every forge generate.",
  },
  handler: {
    title: "The server code",
    body: "Creating, viewing, listing, updating and deleting orders are wired up for you. Custom API actions still need your app's rules: CancelOrder starts as a placeholder that returns Unimplemented until you or your agent implement it.",
  },
  hook: {
    title: "The browser's API helpers",
    body: "Forge generates React helpers for calling each API operation, using the same request and response definitions as the server. They are regenerated when those definitions change.",
  },
  test: {
    title: "The test",
    body: "This test creates, reads, lists, updates and deletes orders against a real Postgres database with your migrations applied. Forge writes it once; you extend it as your app grows. The recording shows the test code being generated, not the test being run.",
  },
};

/** Files the explainers name; the test checks each is in the manifest. */
export const NAMED_PATHS = {
  serve: "cmd/shop/cmd/serve.go",
  browserTelemetry: "frontends/web/src/lib/otel_gen.ts",
  agents: "AGENTS.md",
  cancelStub: "internal/handlers/orders/rpc_cancel_order.go",
} as const;
