// Copyright (c) 2025 Reliant Labs

/**
 * "How this app was built" renders from the committed fixtures alone: the
 * totals on screen are the manifest's, each category row is its category,
 * the entity chain clicks through every excerpt, and every claim the copy
 * makes (a file it names, a fact about an excerpt) is in the recorded run.
 */

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";

import { CATEGORY_COPY, CHAIN_COPY, NAMED_PATHS } from "../copy";
import { scaffoldFixtures } from "../fixtures";
import { HowThisAppWasBuilt } from "../HowThisAppWasBuilt";
import { summarize } from "../ScaffoldPanel";

const { cast, manifest } = scaffoldFixtures();
const n = (value: number) => value.toLocaleString("en-US");
const allPaths = manifest.categories.flatMap((c) => c.entries.map((e) => e[0]));
const castText = (command: string) => {
  const step = cast.steps.find((s) => s.kind === "command" && s.command === command);
  return step?.kind === "command" ? step.events.map((e) => e[1]).join("") : "";
};

describe("<HowThisAppWasBuilt>", () => {
  it("states the manifest's totals", () => {
    render(<HowThisAppWasBuilt />);
    const t = manifest.totals;
    expect(screen.getByTestId("scaffold-headline")).toHaveTextContent(
      `You wrote ${n(t.you_wrote_lines)} lines of proto in ${t.you_wrote_files} files. forge generated ${n(t.forge_lines)} lines across ${n(t.files)} files.`
    );
    const totals = screen.getByTestId("scaffold-totals");
    const s = summarize(manifest, cast);
    for (const value of [s.newFiles, s.scaffoldAdded, s.scaffoldChanged, s.regenLines, s.machineLines, s.onceLines]) {
      expect(totals).toHaveTextContent(n(value));
    }
    expect(s.onceLines + s.regenLines).toBe(t.forge_lines);
    expect(s.newFiles + s.scaffoldAdded).toBe(t.files);
    expect(screen.getByTestId("how-built")).toHaveTextContent(manifest.forge_version);
  });

  it("lists every category, its lines and files as counted, and its files when opened", () => {
    render(<HowThisAppWasBuilt />);
    // Every category the manifest has is labelled (none falls off the panel).
    expect(manifest.categories.map((c) => c.id).sort()).toEqual(
      CATEGORY_COPY.map((c) => c.id).filter((id) => manifest.categories.some((c) => c.id === id)).sort()
    );
    for (const cat of manifest.categories) {
      const row = screen.getByTestId(`scaffold-category-${cat.id}`);
      expect(within(row).getByTestId("scaffold-category-count")).toHaveTextContent(`${n(cat.lines)} lines · ${cat.files} file`);
    }
    const sum = manifest.categories.reduce((acc, c) => acc + c.lines, 0);
    expect(sum).toBe(manifest.totals.lines);

    const deploy = screen.getByTestId("scaffold-category-deploy");
    fireEvent.click(within(deploy).getByRole("button"));
    const files = within(deploy).getByTestId("scaffold-category-files");
    expect(within(files).getAllByRole("listitem")).toHaveLength(manifest.categories.find((c) => c.id === "deploy")!.files);
    // The KCL environments are there: dev, staging and prod.
    for (const env of ["dev", "staging", "prod"]) expect(files).toHaveTextContent(`deploy/kcl/${env}/main.k`);
  });

  it("clicks through one entity, Order, from its proto to its test", () => {
    render(<HowThisAppWasBuilt />);
    const panel = () => screen.getByTestId("scaffold-chain-panel");
    expect(manifest.chain.map((e) => e.id)).toEqual(["proto", "migration", "orm", "handler", "hook", "test"]);
    manifest.chain.forEach((excerpt, i) => {
      expect(panel()).toHaveAttribute("data-excerpt", excerpt.id);
      expect(screen.getByTestId("scaffold-chain-path")).toHaveTextContent(`${excerpt.path}:${excerpt.start_line}–${excerpt.end_line}`);
      expect(panel()).toHaveTextContent(CHAIN_COPY[excerpt.id].body);
      if (i < manifest.chain.length - 1) fireEvent.click(screen.getByTestId("scaffold-chain-next"));
    });
    expect(screen.getByTestId("scaffold-chain-next")).toBeDisabled();
    fireEvent.click(screen.getByTestId("scaffold-chain-tab-migration"));
    expect(panel()).toHaveAttribute("data-excerpt", "migration");
  });

  it("offers Back only when it is given somewhere to go", () => {
    const onBack = vi.fn();
    const { unmount } = render(<HowThisAppWasBuilt />);
    expect(screen.queryByTestId("how-built-back")).toBeNull();
    unmount();
    render(<HowThisAppWasBuilt onBack={onBack} />);
    fireEvent.click(screen.getByTestId("how-built-back"));
    expect(onBack).toHaveBeenCalledTimes(1);
  });
});

describe("what the copy claims is in the recorded run", () => {
  const excerpt = (id: string) => manifest.chain.find((e) => e.id === id)!;

  it("names only files the manifest has", () => {
    for (const path of Object.values(NAMED_PATHS)) expect(allPaths, path).toContain(path);
    for (const excerptPath of manifest.chain.map((e) => e.path)) expect(allPaths).toContain(excerptPath);
  });

  it("the proto: you wrote the message and marker; forge added id and the timestamps, and the CRUD rpcs", () => {
    const proto = excerpt("proto");
    const lines = proto.code.split("\n");
    expect(lines[0]).toBe("// forge:entity");
    expect(proto.yours).toContain(1);
    for (const managed of ["string id =", "created_at =", "updated_at ="]) {
      const at = lines.findIndex((line) => line.includes(managed)) + 1;
      expect(at, managed).toBeGreaterThan(0);
      expect(proto.yours, managed).not.toContain(at);
    }
    expect(castText("forge scaffold")).toMatch(/quintets completed:\s+2/);
  });

  it("the migration is written once and yours; the ORM row is projected from the applied schema and regenerated", () => {
    expect(excerpt("migration").code).toMatch(/YOURS from birth/);
    expect(excerpt("migration").code).toMatch(/CREATE TABLE orders/);
    expect(excerpt("orm").code).toMatch(/APPLIED schema/);
    const orm = manifest.categories.flatMap((c) => c.entries).find((e) => e[0] === excerpt("orm").path)!;
    expect(orm[3]).toBe("regen");
  });

  it("the handler hands off to forge's CRUD code; CancelOrder is an Unimplemented stub", () => {
    expect(excerpt("handler").code).toMatch(/crud\.HandleCreate/);
    expect(castText("forge scaffold")).toMatch(/handler stub\(s\).*CancelOrder/);
  });

  it("the hooks are React Query hooks over the proto's types; the test runs on real Postgres", () => {
    expect(excerpt("hook").code).toMatch(/useCreateOrder = createMutationHook/);
    expect(excerpt("hook").code).toMatch(/useListOrders = createQueryHook/);
    expect(excerpt("test").code).toMatch(/crudTestDB/);
  });

  it("the lint count is the cast's", () => {
    const s = summarize(manifest, cast);
    expect(s.gatingLinters).not.toBeNull();
    expect(castText("forge lint --quiet")).toContain(`All ${s.gatingLinters} gating linters passed`);
  });
});
