/** Example prompts for the composer placeholder; examplePromptFor picks one. */
export const EXAMPLE_PROMPTS = [
  "Add authentication to my API",
  "Refactor this component to use hooks",
  "Fix the bug in the checkout flow",
  "Write tests for the user service",
  "Optimize this database query",
  "Add dark mode to my app",
  "Create a REST API for users",
  "Set up CI/CD pipeline",
  "Improve the error handling",
  "Add input validation to this form",
  "Convert this to TypeScript",
  "Make this component responsive",
  "Add logging to track errors",
  "Implement caching for this endpoint",
  "Review and improve this code",
  "Build a user dashboard",
  "Add pagination to this list",
  "Create a file upload feature",
  "Implement real-time notifications",
  "Add search functionality",
  "Build a payment integration",
  "Create an admin panel",
  "Add email verification",
  "Implement password reset",
  "Build a comment system",
  "Add rate limiting",
  "Create a webhook handler",
  "Implement OAuth login",
  "Add database migrations",
  "Build a chat feature",
  "Create export to CSV",
  "Add image compression",
  "Implement lazy loading",
  "Build a filter system",
  "Add analytics tracking",
  "Create a scheduling system",
  "Implement drag and drop",
  "Add multi-language support",
  "Build a notification system",
  "Create API documentation",
  "Add error monitoring",
  "Implement data validation",
  "Build a settings page",
  "Add role-based permissions",
  "Create a backup system",
  "Implement infinite scroll",
  "Add keyboard shortcuts",
  "Build a reporting dashboard",
  "Create a mobile app version",
  "Add WebSocket support",
];

/** FNV-1a: a small, stable string hash, the same in every browser. */
function hashString(s: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

/**
 * The example prompt for a chat's composer: a function of the chat, not of
 * Math.random, so a chat shows the same example every time it is opened and
 * the choice cannot shift when some unrelated code draws a random number
 * first. (It used to be Math.random(), which made the visual baseline move
 * whenever a route change reordered the draws before the composer mounted.)
 *
 * A new chat has no id yet, so it is keyed by the UTC day: its example still
 * rotates, once a day.
 */
export function examplePromptFor(chatId: string | null | undefined, now: Date = new Date()): string {
  const key = chatId || `new-chat:${now.toISOString().slice(0, 10)}`;
  return EXAMPLE_PROMPTS[hashString(key) % EXAMPLE_PROMPTS.length];
}
