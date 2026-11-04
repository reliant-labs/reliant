// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { Span } from "@/services/controlPlane/observability/types";

import { operationName, traceTitle } from "../traceTitle";

const span = (spanId: string, parentSpanId: string, name: string, service: string, extra: Partial<Span> = {}): Span => ({
  spanId,
  parentSpanId,
  name,
  service,
  kind: "internal",
  startMs: 0,
  durationMs: 1,
  status: "unset",
  statusMessage: "",
  attributes: {},
  resource: {},
  events: [],
  source: "",
  operatorData: false,
  ...extra,
});

const browser = { "reliant.ingest.class": "browser", "process.runtime.name": "browser" };

describe("operationName", () => {
  it("names an RPC Service.Method, from its server span or a fetch to its procedure", () => {
    expect(operationName("reliant.v1.ChatService/SendMessage")).toBe("ChatService.SendMessage");
    expect(operationName("/reliant.v1.ChatService/SendMessage")).toBe("ChatService.SendMessage");
    expect(operationName("POST /reliant.v1.ChatService/SendMessage")).toBe("ChatService.SendMessage");
    expect(operationName("HTTP POST", { "http.method": "POST", "http.url": "https://api.reliantapi.com/reliant.v1.SettingsService/GetConfigHealth" })).toBe(
      "SettingsService.GetConfigHealth"
    );
  });

  it("names a package-less RPC Service.Method, and leaves a package-less path alone", () => {
    expect(operationName("ChatService/SendMessage")).toBe("ChatService.SendMessage");
    expect(operationName("/ChatService/SendMessage")).toBe("ChatService.SendMessage");
    expect(operationName("POST /ChatService/SendMessage")).toBe("ChatService.SendMessage");
    expect(operationName("HTTP POST", { "http.method": "POST", "http.url": "https://api.example/ChatService/SendMessage" })).toBe("ChatService.SendMessage");
    expect(operationName("GET /v1/items")).toBe("GET /v1/items");
    expect(operationName("/v1/items")).toBe("/v1/items");
    expect(operationName("HTTP GET", { "url.path": "/v1/items" })).toBe("GET /v1/items");
    expect(operationName("HTTP GET", { "url.path": "/Users/Profile" })).toBe("GET /Users/Profile");
  });

  it("names an HTTP span by method and route, never a bare HTTP POST", () => {
    expect(operationName("HTTP POST", { "http.method": "POST", "http.url": "https://dash.reliantlabs.io/auth/v1/token?x=1" })).toBe("POST /auth/v1/token");
    expect(operationName("GET", { "http.route": "/v1/items/{id}", "url.path": "/v1/items/42" })).toBe("GET /v1/items/{id}");
    expect(operationName("HTTP GET", { "url.full": "https://x.example/v1/items/42/notes/7f3c9a1b-0d2e-4f5a-8b6c-1d2e3f4a5b6c" })).toBe("GET /v1/items/{id}/notes/{id}");
    // No URL to say more with: the name stays.
    expect(operationName("HTTP POST")).toBe("HTTP POST");
  });

  it("leaves a name that already says what it did", () => {
    expect(operationName("GET /v1/items")).toBe("GET /v1/items");
    expect(operationName("SELECT orders")).toBe("SELECT orders");
    expect(operationName("GetOrder")).toBe("GetOrder");
    expect(operationName("")).toBe("");
  });
});

describe("traceTitle", () => {
  it("titles a browser-started trace by the server span it caused, from the browser", () => {
    const spans = [
      span("r", "", "HTTP POST", "reliant-web", { kind: "client", resource: browser, attributes: { "http.url": "https://api.reliantapi.com/reliant.v1.ChatService/SendMessage" } }),
      span("s", "r", "reliant.v1.ChatService/SendMessage", "reliant-api-server", { kind: "server", startMs: 5 }),
      span("d", "s", "reliant.v1.DaemonService/Run", "daemon-gateway", { kind: "server", startMs: 9 }),
    ];
    expect(traceTitle(spans)).toEqual({ service: "reliant-api-server", operation: "ChatService.SendMessage", from: "reliant-web", raw: "HTTP POST" });
  });

  it("titles a browser-only trace by the call it made", () => {
    const spans = [span("r", "", "HTTP POST", "reliant-web", { kind: "client", resource: browser, attributes: { "http.method": "POST", "http.url": "https://api.reliantapi.com/reliant.v1.SettingsService/GetConfigHealth" } })];
    expect(traceTitle(spans)).toEqual({ service: "reliant-web", operation: "SettingsService.GetConfigHealth", raw: "HTTP POST" });
  });

  it("titles a server-rooted trace by its root", () => {
    const spans = [span("a", "", "orders.v1.OrdersService/CreateOrder", "api", { kind: "server" }), span("b", "a", "SELECT orders", "api")];
    expect(traceTitle(spans)).toEqual({ service: "api", operation: "OrdersService.CreateOrder", raw: "orders.v1.OrdersService/CreateOrder" });
    expect(traceTitle([])).toBeNull();
  });
});
