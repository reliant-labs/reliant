import { Code, ConnectError } from "@connectrpc/connect";
import { expect, it } from "vitest";
import { describeMachineFolderError } from "../machineFolderErrors";
it("explains offline machines and preserves specific server refusals", () => {
  expect(describeMachineFolderError(new ConnectError("no daemon connected", Code.Unavailable), "Laptop")).toContain("Laptop is offline");
  expect(describeMachineFolderError(new ConnectError("Folder belongs to another project", Code.AlreadyExists), "Laptop")).toBe("Folder belongs to another project");
  expect(describeMachineFolderError(new ConnectError("Folder does not exist", Code.NotFound), "Laptop")).toBe("Folder does not exist");
});
