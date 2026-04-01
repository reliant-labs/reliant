import type { PackageCommand } from "../../api/package-commands-grpc";

interface ProcessLike {
  command: string;
  working_dir: string;
}

function normalizePath(path?: string): string {
  return (path || "").replace(/\/+$/, "").toLowerCase();
}

function normalizeCommand(command: string): string {
  return command.trim().replace(/\s+/g, " ").toLowerCase();
}

function tokenizeCommand(command: string): string[] {
  return normalizeCommand(command).split(" ").filter(Boolean);
}

function workingDirMatches(process: ProcessLike, command: PackageCommand): boolean {
  if (!command.working_dir || !process.working_dir) return true;
  return normalizePath(command.working_dir) === normalizePath(process.working_dir);
}

function fullCommandMatches(process: ProcessLike, command: PackageCommand): boolean {
  return normalizeCommand(process.command) === normalizeCommand(command.command);
}

/**
 * Strict command matcher for mapping process rows back to package commands.
 *
 * Important: this must avoid substring matching (e.g. `dev` matching `dev:pg`).
 */
export function processMatchesCommand(process: ProcessLike, command: PackageCommand): boolean {
  if (!workingDirMatches(process, command)) {
    return false;
  }

  if (fullCommandMatches(process, command)) {
    return true;
  }

  const tokens = tokenizeCommand(process.command);
  const cmdName = command.name.toLowerCase();

  if (command.package_type === "npm") {
    return (
      (tokens.length >= 3 && tokens[0] === "npm" && tokens[1] === "run" && tokens[2] === cmdName) ||
      (tokens.length >= 2 && tokens[0] === "npm" && tokens[1] === cmdName)
    );
  }

  if (command.package_type === "makefile") {
    return tokens.length >= 2 && tokens[0] === "make" && tokens[1] === cmdName;
  }

  if (command.package_type === "taskfile") {
    return tokens.length >= 2 && tokens[0] === "task" && tokens[1] === cmdName;
  }

  return false;
}

/**
 * Resolve the best command match for a process.
 * Prefers exact command-string matches first, then strict token matches.
 */
export function findCommandForProcess(
  process: ProcessLike,
  commandsByType: Record<string, PackageCommand[]>,
  detectedTypes: string[]
): PackageCommand | undefined {
  const orderedCommands: PackageCommand[] = [];
  for (const type of detectedTypes) {
    orderedCommands.push(...(commandsByType[type] || []));
  }

  const exactMatch = orderedCommands.find(
    (cmd) => workingDirMatches(process, cmd) && fullCommandMatches(process, cmd)
  );
  if (exactMatch) {
    return exactMatch;
  }

  return orderedCommands.find((cmd) => processMatchesCommand(process, cmd));
}
