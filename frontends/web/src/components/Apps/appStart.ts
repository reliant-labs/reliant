// Copyright (c) 2025 Reliant Labs

/**
 * "Build an app" is a chat: the user describes the app, and the agent builds
 * it with forge and deploys it to Reliant hosting. Every entry point to it —
 * onboarding's choice, the Apps page, the home view — lands on the same new
 * chat with the same starter text in the composer, marked by `?start=app` on
 * the app route (HomeNewChat applies it once and drops the param).
 *
 * Constants only, with no imports, so onboarding's exit can name the param
 * without pulling the app's stores into its module graph.
 */

/** The `start` search value that opens the app-building composer. */
export const START_APP = "app" as const;

/** `?start=repo`: a new chat whose composer offers to deploy the user's existing repository. */
export const START_REPO = "repo" as const;

/** `?start=chat`: a new chat whose composer offers a first question about the user's project. */
export const START_CHAT = "chat" as const;

/** Every `start` value the app route accepts; routeSchemas.ts validates against this. */
export const START_VALUES = [START_APP, START_REPO, START_CHAT] as const;

export type StartValue = (typeof START_VALUES)[number];

export function isStartValue(value: unknown): value is StartValue {
  return typeof value === "string" && (START_VALUES as readonly string[]).includes(value);
}

/**
 * The composer's starter text. It names forge and hosting so the agent takes
 * the deploy path, and ends mid-sentence so the user's cursor lands where the
 * idea goes.
 */
export const BUILD_APP_PROMPT =
  "Build me a web app with forge and deploy it to Reliant hosting so I get a live URL. It should ";

/**
 * The first message onboarding offers, by what the user said they came to do.
 * It is text in the composer, never sent for them: they read it, change it, and
 * press send. Naming the project (when there is one) is what makes it a
 * question about THEIR code rather than a generic greeting.
 */
export function starterPrompt(start: StartValue, projectName?: string): string {
  const project = projectName?.trim() || "this project";
  switch (start) {
    case START_APP:
      return BUILD_APP_PROMPT;
    case START_REPO:
      return `Deploy ${project} with forge and Reliant hosting so I get a live URL. Tell me what you change and why.`;
    case START_CHAT:
      return `Look through ${project} and tell me what it does, how it is put together, and what I should work on first.`;
  }
}

/** Which starter an onboarding intent lands on. */
export function startForIntent(intent: "build_app" | "existing_codebase" | "chat" | undefined): StartValue {
  switch (intent) {
    case "build_app":
      return START_APP;
    case "existing_codebase":
      return START_REPO;
    default:
      return START_CHAT;
  }
}
