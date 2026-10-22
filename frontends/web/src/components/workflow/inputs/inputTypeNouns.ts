// Copyright (c) 2025 Reliant Labs

/**
 * The words a typed input's picker uses, derived from the type ref alone so
 * a new integration type needs no copy here: `github.repository` reads as
 * "repository" / "repositories", `github.pull_request` as "pull request".
 */

export interface InputTypeNouns {
  singular: string;
  plural: string;
  /** "GitHub" for `github.repository`, when the host does not say. */
  integration: string;
}

const KNOWN_INTEGRATION_NAMES: Record<string, string> = {
  github: "GitHub",
  gitlab: "GitLab",
};

export function pluralize(noun: string): string {
  if (/[^aeiou]y$/.test(noun)) return `${noun.slice(0, -1)}ies`;
  if (/(s|x|z|ch|sh)$/.test(noun)) return `${noun}es`;
  return `${noun}s`;
}

export function integrationDisplayName(id: string): string {
  return KNOWN_INTEGRATION_NAMES[id] ?? (id ? id.charAt(0).toUpperCase() + id.slice(1) : "");
}

export function inputTypeNouns(type: string, integrationName?: string): InputTypeNouns {
  const dot = type.indexOf(".");
  const integration = dot > 0 ? type.slice(0, dot) : "";
  const singular = (dot > 0 ? type.slice(dot + 1) : type).replace(/[_-]+/g, " ").trim() || "value";
  return { singular, plural: pluralize(singular), integration: integrationName || integrationDisplayName(integration) };
}
