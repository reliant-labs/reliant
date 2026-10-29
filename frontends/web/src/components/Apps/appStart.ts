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

/**
 * The composer's starter text. It names forge and hosting so the agent takes
 * the deploy path, and ends mid-sentence so the user's cursor lands where the
 * idea goes.
 */
export const BUILD_APP_PROMPT =
  "Build me a web app with forge and deploy it to Reliant hosting so I get a live URL. It should ";
