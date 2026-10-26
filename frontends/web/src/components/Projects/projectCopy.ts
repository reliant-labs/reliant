// Copyright (c) 2025 Reliant Labs

/**
 * What a project is, and the two ways to add one — the one wording used by
 * every surface that talks about projects (the Add project dialog, the
 * first-run state, Settings → Projects, the sidebar's project menu, mobile).
 *
 * The model it describes (internal/db/core/project_worktree.go): a project is
 * a code repository (a git remote or a local path), may hold several nested
 * repositories, owns its chats and workspaces (git worktrees), and can be
 * checked out on several machines. "Workspace" means a worktree here and
 * nowhere else; the computer a project runs on is a "machine".
 */

/** The definition, shown under every "Projects" heading. */
export const PROJECT_DEFINITION =
  "A project is a codebase Reliant works in: one Git repository, or a folder that holds several. " +
  "Its chats and workspaces live inside it, and it can be checked out on any of your machines.";

/** The definition's first sentence, for tight spots (a menu footer). */
export const PROJECT_DEFINITION_SHORT =
  "A project is a codebase Reliant works in: one Git repository, or a folder that holds several.";

/** The empty state, wherever there are no projects yet. */
export const NO_PROJECTS_COPY =
  "Add a codebase — clone a repository or open a folder — and Reliant will start working in it.";

/** The umbrella action, and its two methods. "New project" is reserved for creating an empty one. */
export const ADD_PROJECT_LABEL = "Add project";
export const CLONE_REPOSITORY_LABEL = "Clone a repository";
export const OPEN_FOLDER_LABEL = "Open a folder";

/** Removing a project forgets the record only. */
export const REMOVE_PROJECT_HINT = "Remove from Reliant (files stay on disk)";
