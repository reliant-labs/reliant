import { describe, expect, it } from "vitest";
import type { PackageCommand, PackageType } from "../../../api/package-commands-grpc";
import { findCommandForProcess, processMatchesCommand } from "../commandMatching";

function makeCommand(overrides: Partial<PackageCommand> = {}): PackageCommand {
  const packageType = (overrides.package_type || "npm") as PackageType;
  const name = overrides.name || "dev";

  return {
    name,
    description: overrides.description,
    command:
      overrides.command ||
      (packageType === "npm"
        ? `npm run ${name}`
        : packageType === "makefile"
          ? `make ${name}`
          : `task ${name}`),
    package_type: packageType,
    source: overrides.source || "package.json",
    category: overrides.category,
    dependencies: overrides.dependencies,
    working_dir: overrides.working_dir || "/repo",
    relative_path: overrides.relative_path,
  };
}

describe("commandMatching", () => {
  it("does not match npm command prefixes (dev should not match dev:pg)", () => {
    const process = {
      command: "npm run dev:pg",
      working_dir: "/repo",
    };

    const devCommand = makeCommand({ name: "dev", command: "npm run dev" });
    const devPgCommand = makeCommand({ name: "dev:pg", command: "npm run dev:pg" });

    expect(processMatchesCommand(process, devCommand)).toBe(false);
    expect(processMatchesCommand(process, devPgCommand)).toBe(true);
  });

  it("respects working directory matching", () => {
    const process = {
      command: "npm run dev",
      working_dir: "/repo/web",
    };

    const rootCommand = makeCommand({ name: "dev", working_dir: "/repo" });
    const webCommand = makeCommand({ name: "dev", working_dir: "/repo/web" });

    expect(processMatchesCommand(process, rootCommand)).toBe(false);
    expect(processMatchesCommand(process, webCommand)).toBe(true);
  });

  it("matches make and task commands exactly by command token", () => {
    const makeProcess = { command: "make test", working_dir: "/repo" };
    const taskProcess = { command: "task lint", working_dir: "/repo" };

    expect(processMatchesCommand(makeProcess, makeCommand({ package_type: "makefile", name: "test" }))).toBe(true);
    expect(processMatchesCommand(makeProcess, makeCommand({ package_type: "makefile", name: "tes" }))).toBe(false);

    expect(processMatchesCommand(taskProcess, makeCommand({ package_type: "taskfile", name: "lint" }))).toBe(true);
    expect(processMatchesCommand(taskProcess, makeCommand({ package_type: "taskfile", name: "lin" }))).toBe(false);
  });

  it("finds the exact command match for rerun when similar commands exist", () => {
    const commandsByType: Record<string, PackageCommand[]> = {
      npm: [
        makeCommand({ name: "dev", command: "npm run dev" }),
        makeCommand({ name: "dev:pg", command: "npm run dev:pg" }),
      ],
    };

    const process = {
      command: "npm run dev:pg",
      working_dir: "/repo",
    };

    const match = findCommandForProcess(process, commandsByType, ["npm"]);
    expect(match?.name).toBe("dev:pg");
  });
});
